// Package upload sends local files to Slack with the two-step external
// upload flow: files.getUploadURLExternal reserves a file ID and a
// pre-signed URL, the bytes are POSTed to that URL, and
// files.completeUploadExternal finalises the files (optionally sharing
// them to a conversation).
//
// Every function validates all paths and stages all files before
// completing anything: Slack discards staged-but-uncompleted uploads, so a
// failure half-way through leaves nothing behind.
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// MaxFileSize is Slack's documented per-file upload limit (1 GB).
const MaxFileSize int64 = 1 << 30

// cleanupTimeout bounds DeleteFiles, which deliberately outlives the
// caller's (possibly already cancelled) context.
const cleanupTimeout = 15 * time.Second

// File is a file uploaded to Slack.
type File struct {
	// ID is Slack's file ID (F…).
	ID string `json:"id"`
	// Name is the file name Slack was given (base name of the local path,
	// symlinks resolved).
	Name string `json:"name"`
	// Size is the number of bytes uploaded.
	Size int64 `json:"size"`
}

// IDs returns the Slack file IDs of files, in order.
func IDs(files []File) []string {
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	return ids
}

// Destination is where ToChannel shares the uploaded files.
type Destination struct {
	// ChannelID is the conversation ID (C…, D…, G…). Required.
	ChannelID string
	// ThreadTS, when set, posts the files as a reply in that thread.
	ThreadTS string
	// InitialComment is the message text posted with the files (plain
	// text/mrkdwn; Slack does not accept blocks here). Optional.
	InitialComment string
}

// Check validates paths without talking to Slack: each must exist, be a
// regular non-empty file and be at most MaxFileSize. Commands call it
// before doing any other work (resolving channels, opening DMs …) so that a
// bad --attach fails fast. ToChannel and ForDraft run the same checks
// again.
func Check(paths []string) error {
	for _, p := range paths {
		if _, err := inspect(p); err != nil {
			return err
		}
	}
	return nil
}

// ToChannel uploads the files at paths and shares them to dest as ONE
// message carrying dest.InitialComment. Nothing is posted unless every
// file was validated and staged successfully.
func ToChannel(ctx context.Context, client *slackapi.Client, dest Destination, paths []string) ([]File, error) {
	if dest.ChannelID == "" {
		return nil, errors.New("uploading files: no destination channel ID")
	}
	params := map[string]any{"channel_id": dest.ChannelID}
	if dest.ThreadTS != "" {
		params["thread_ts"] = dest.ThreadTS
	}
	if c := strings.TrimSpace(dest.InitialComment); c != "" {
		params["initial_comment"] = c
	}
	return uploadAll(ctx, client, paths, params)
}

// ForDraft uploads the files at paths without sharing them anywhere: they
// stay private to the user until a draft referencing their IDs is sent.
// If the draft call that was meant to reference them then fails, pass
// IDs(files) to DeleteFiles so they do not linger as orphans.
func ForDraft(ctx context.Context, client *slackapi.Client, paths []string) ([]File, error) {
	return uploadAll(ctx, client, paths, map[string]any{})
}

// DeleteFiles deletes uploaded files that ended up unused (see ForDraft).
// Because it typically runs after another call failed — possibly because
// ctx was cancelled or timed out — it detaches from ctx's cancellation and
// applies its own short timeout. Files that are already gone are not an
// error. The returned error joins the individual failures; callers for
// whom this is best-effort cleanup should report it as a hint rather than
// let it mask the original error.
func DeleteFiles(ctx context.Context, client *slackapi.Client, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	errs := make([]error, len(fileIDs))
	var wg sync.WaitGroup
	for i, id := range fileIDs {
		wg.Go(func() {
			_, err := client.API(ctx, "files.delete", map[string]any{"file": id})
			if err != nil && !slackapi.IsErrorCode(err, "file_not_found", "file_deleted") {
				errs[i] = fmt.Errorf("deleting uploaded file %s: %w", id, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// uploadAll validates and stages every path, then finalises them with a
// single files.completeUploadExternal call carrying completeParams.
func uploadAll(ctx context.Context, client *slackapi.Client, paths []string, completeParams map[string]any) ([]File, error) {
	if len(paths) == 0 {
		return nil, errors.New("uploading files: no file paths given")
	}
	// Validate everything first: a typo in the last path must not cost the
	// upload of all the files before it.
	locals := make([]localFile, len(paths))
	for i, p := range paths {
		l, err := inspect(p)
		if err != nil {
			return nil, err
		}
		locals[i] = l
	}

	files := make([]File, len(locals))
	for i, l := range locals {
		f, err := stage(ctx, client, l)
		if err != nil {
			return nil, err
		}
		files[i] = f
	}

	refs := make([]map[string]any, len(files))
	for i, f := range files {
		refs[i] = map[string]any{"id": f.ID, "title": f.Name}
	}
	completeParams["files"] = refs
	resp, err := client.API(ctx, "files.completeUploadExternal", completeParams)
	if err != nil {
		return nil, fmt.Errorf("finalising upload of %s: %w", describe(files), err)
	}

	// The completion response is authoritative for the final IDs, but only
	// positionally, and only when it lines up 1:1 with what was sent:
	// guessing otherwise could attach the wrong file to a draft.
	if completed := jsonx.Slice(resp["files"]); len(completed) == len(files) {
		for i, c := range completed {
			if id := jsonx.GetStr(c, "id"); id != "" {
				files[i].ID = id
			}
		}
	}
	return files, nil
}

type localFile struct {
	// given is the path as the user wrote it (for error messages);
	// resolved has symlinks followed.
	given, resolved string
}

func inspect(path string) (localFile, error) {
	if strings.TrimSpace(path) == "" {
		return localFile{}, errors.New("attachment path is empty")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return localFile{}, pathError(path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return localFile{}, pathError(path, err)
	}
	if err := checkInfo(path, info); err != nil {
		return localFile{}, err
	}
	return localFile{given: path, resolved: resolved}, nil
}

func checkInfo(path string, info fs.FileInfo) error {
	switch {
	case info.IsDir():
		return fmt.Errorf("attachment path is a directory, not a file: %s (attach files one by one, or archive the directory first)", path)
	case !info.Mode().IsRegular():
		return fmt.Errorf("attachment path is not a regular file: %s", path)
	case info.Size() == 0:
		return fmt.Errorf("attachment is empty (0 bytes): %s (Slack rejects empty files)", path)
	case info.Size() > MaxFileSize:
		return fmt.Errorf("attachment is too large: %s is %s, Slack allows up to %s per file", path, humanSize(info.Size()), humanSize(MaxFileSize))
	}
	return nil
}

func pathError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("attachment not found: %s (relative paths resolve against the current directory)", path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("attachment is not readable (permission denied): %s", path)
	}
	return fmt.Errorf("reading attachment %s: %w", path, err)
}

// stage reserves an upload URL for l and streams its bytes there.
func stage(ctx context.Context, client *slackapi.Client, l localFile) (File, error) {
	f, err := os.Open(l.resolved)
	if err != nil {
		return File{}, pathError(l.given, err)
	}
	defer f.Close()
	// Re-check on the open handle: the size announced to Slack must be the
	// size of the file actually streamed.
	info, err := f.Stat()
	if err != nil {
		return File{}, pathError(l.given, err)
	}
	if err := checkInfo(l.given, info); err != nil {
		return File{}, err
	}
	name, size := filepath.Base(l.resolved), info.Size()

	resp, err := client.API(ctx, "files.getUploadURLExternal", map[string]any{
		"filename": name,
		"length":   size,
	})
	if err != nil {
		return File{}, fmt.Errorf("reserving an upload for %s: %w", l.given, err)
	}
	uploadURL, fileID := jsonx.Str(resp["upload_url"]), jsonx.Str(resp["file_id"])
	if uploadURL == "" || fileID == "" {
		return File{}, fmt.Errorf("reserving an upload for %s: Slack returned no upload URL or file ID", l.given)
	}
	if err := postBytes(ctx, client, uploadURL, f, size); err != nil {
		return File{}, fmt.Errorf("uploading %s: %w", l.given, err)
	}
	return File{ID: fileID, Name: name, Size: size}, nil
}

// postBytes streams size bytes of body to the pre-signed uploadURL. The URL
// itself authorises the upload, so no token or cookie is attached: they
// must never leave Slack's API hosts, whatever host the response named.
func postBytes(ctx context.Context, client *slackapi.Client, uploadURL string, body io.Reader, size int64) error {
	u, err := url.Parse(uploadURL)
	if err != nil || u.Host == "" {
		return errors.New("Slack returned an invalid upload URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing to upload over %q: Slack returned a non-HTTPS upload URL", u.Scheme)
	}
	// NopCloser: the transport closes request bodies, and the caller owns
	// the file.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, io.NopCloser(body))
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", appinfo.UserAgent())

	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		// *url.Error embeds the full pre-signed URL; keep it out of
		// messages that end up in agent transcripts.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("sending file bytes to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := fmt.Sprintf("sending file bytes to %s failed with HTTP %d", u.Host, resp.StatusCode)
		if s := strings.TrimSpace(string(snippet)); s != "" {
			msg += ": " + s
		}
		return errors.New(msg)
	}
	return nil
}

func describe(files []File) string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}
