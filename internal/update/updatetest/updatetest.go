// Package updatetest fakes the two GitHub endpoints used by the updater
// (the releases API and release-asset downloads) for tests.
package updatetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Server is a fake GitHub serving one repository. Its URL is both the API
// base URL and the download base URL.
type Server struct {
	*httptest.Server
	repo string

	mu        sync.Mutex
	tag       string
	assets    map[string][]byte
	apiStatus int
	apiHeader http.Header
	apiGate   chan struct{}
	apiCalls  int
	downloads []string
}

// NewServer starts a fake GitHub for repo ("owner/name") with no release.
func NewServer(t testing.TB, repo string) *Server {
	t.Helper()
	s := &Server{repo: repo}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/"+repo+"/releases/latest", s.latest)
	mux.HandleFunc("GET /"+repo+"/releases/download/{tag}/{asset}", s.download)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// Publish makes tag the latest release, with the given assets.
func (s *Server) Publish(tag string, assets map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tag, s.assets = tag, assets
}

// FailAPI makes the releases API answer with status and headers instead
// of the release. A zero status restores normal behaviour.
func (s *Server) FailAPI(status int, header http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiStatus, s.apiHeader = status, header
}

// BlockAPI makes the releases API hang until the returned function is
// called.
func (s *Server) BlockAPI() (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.apiGate = gate
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// APICalls returns how many times the releases API was requested.
func (s *Server) APICalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiCalls
}

// Downloads returns the names of the assets requested so far.
func (s *Server) Downloads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.downloads...)
}

func (s *Server) latest(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.apiCalls++
	tag, status, header, gate := s.tag, s.apiStatus, s.apiHeader, s.apiGate
	s.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	switch {
	case status != 0:
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"message":"nope"}`)
	case tag == "":
		http.NotFound(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[]}`, tag)
	}
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.downloads = append(s.downloads, r.PathValue("asset"))
	body, ok := s.assets[r.PathValue("asset")]
	tag := s.tag
	s.mu.Unlock()
	if !ok || r.PathValue("tag") != tag {
		http.NotFound(w, r)
		return
	}
	w.Write(body)
}

// TarGz returns a gzipped tar archive holding the given files.
func TarGz(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(files[name])), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Zip returns a zip archive holding the given files.
func Zip(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range sortedKeys(files) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Checksums returns a sha256sum-style listing of the given assets, in the
// format goreleaser writes.
func Checksums(assets map[string][]byte) []byte {
	var b strings.Builder
	for _, name := range sortedKeys(assets) {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(assets[name]), name)
	}
	return []byte(b.String())
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
