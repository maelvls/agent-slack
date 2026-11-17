package authextract

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/slackapi"
)

// xoxdPattern finds the session cookie inside a decrypted cookie value.
// Recent Chromium versions prepend a 32-byte hash of the cookie's domain
// to the plaintext, so the value cannot be assumed to start with "xoxd-".
var xoxdPattern = regexp.MustCompile(`xoxd-[A-Za-z0-9%/+_=.-]+`)

// cookieFromPlaintext returns the URL-decoded xoxd cookie found in plain.
func cookieFromPlaintext(plain []byte) (string, bool) {
	m := xoxdPattern.Find(plain)
	if m == nil {
		return "", false
	}
	return slackapi.DecodeCookieD(string(m)), true
}

// decryptCBC decrypts a Chromium cookie value as encrypted on macOS and
// Linux: AES-128-CBC with a key derived from the Safe Storage password.
// data excludes the "v10"/"v11" version prefix.
func decryptCBC(data []byte, password string, iterations int) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("encrypted cookie has an invalid length")
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), iterations, 16)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := []byte(strings.Repeat(" ", aes.BlockSize))
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)

	// PKCS#7 padding; a wrong password almost always fails here.
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, errors.New("wrong password (invalid padding)")
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, errors.New("wrong password (invalid padding)")
		}
	}
	return plain[:len(plain)-pad], nil
}

// decryptGCMWindows decrypts a Chromium cookie value as encrypted on
// Windows: "v10" + 12-byte nonce + AES-256-GCM ciphertext and tag, with a
// key that is stored DPAPI-protected in the application's "Local State".
func decryptGCMWindows(encrypted []byte, dataDir string) ([]byte, error) {
	localStatePath := filepath.Join(dataDir, "Local State")
	raw, err := os.ReadFile(localStatePath)
	if err != nil {
		return nil, fmt.Errorf("reading the cookie encryption key: %w", err)
	}
	var localState struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(raw, &localState); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", localStatePath, err)
	}
	protected, err := base64.StdEncoding.DecodeString(localState.OSCrypt.EncryptedKey)
	if err != nil || len(protected) == 0 {
		return nil, fmt.Errorf("%s has no usable os_crypt.encrypted_key", localStatePath)
	}
	const dpapiPrefix = "DPAPI"
	if !strings.HasPrefix(string(protected), dpapiPrefix) {
		return nil, fmt.Errorf("%s: os_crypt.encrypted_key is not DPAPI-protected", localStatePath)
	}
	key, err := dpapiUnprotect(protected[len(dpapiPrefix):])
	if err != nil {
		return nil, fmt.Errorf("unprotecting the cookie encryption key with DPAPI: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cookie encryption key: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	const versionLen = 3
	if len(encrypted) < versionLen+gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("encrypted cookie is too short")
	}
	nonce := encrypted[versionLen : versionLen+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, encrypted[versionLen+gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting the cookie: %w", err)
	}
	return plain, nil
}

// chromiumCookie is one "d" cookie row of a Chromium cookie database.
type chromiumCookie struct {
	value     string // plaintext value; empty when encrypted
	encrypted []byte
}

// readChromiumCookies returns the Slack "d" cookies of a Chromium cookie
// database, the most specific candidate first.
func readChromiumCookies(ctx context.Context, dbPath string) ([]chromiumCookie, error) {
	// The host is matched exactly or as a subdomain: a bare "%slack.com"
	// pattern would also match look-alike domains such as "notslack.com".
	const query = `select value, encrypted_value from cookies
		where name = 'd'
		  and (host_key in ('.slack.com', 'slack.com', '.slack-gov.com', 'slack-gov.com')
		       or host_key like '%.slack.com' or host_key like '%.slack-gov.com')
		order by host_key in ('.slack.com', '.slack-gov.com') desc, length(encrypted_value) desc`
	var cookies []chromiumCookie
	err := querySQLiteSnapshot(ctx, dbPath, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value sql.NullString
			var c chromiumCookie
			if err := rows.Scan(&value, &c.encrypted); err != nil {
				return err
			}
			c.value = value.String
			cookies = append(cookies, c)
		}
		return rows.Err()
	})
	return cookies, err
}

// chromiumCookieD returns the decoded xoxd cookie stored in the Chromium
// cookie database at dbPath. dataDir is the application's user data
// directory (it holds "Local State" on Windows) and keys finds the Safe
// Storage password on macOS and Linux.
//
// The error wraps ErrNotFound when the database has no Slack cookie at all.
func (h *host) chromiumCookieD(ctx context.Context, dbPath, dataDir string, keys *safeStorage) (string, error) {
	if !exists(dbPath) {
		return "", notFoundf("no cookie database at %s", dbPath)
	}
	cookies, err := readChromiumCookies(ctx, dbPath)
	if err != nil {
		return "", err
	}
	if len(cookies) == 0 {
		return "", notFoundf("no Slack \"d\" cookie in %s", dbPath)
	}
	var lastErr error
	for _, c := range cookies {
		if strings.HasPrefix(c.value, "xoxd-") {
			return slackapi.DecodeCookieD(c.value), nil
		}
		if len(c.encrypted) == 0 {
			continue
		}
		cookie, err := h.decryptChromiumCookie(ctx, c.encrypted, dataDir, keys)
		if err == nil {
			return cookie, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("the cookie is empty")
	}
	return "", fmt.Errorf("could not decrypt the Slack \"d\" cookie in %s: %w", dbPath, lastErr)
}

func (h *host) decryptChromiumCookie(ctx context.Context, encrypted []byte, dataDir string, keys *safeStorage) (string, error) {
	prefix := ""
	if len(encrypted) >= 3 && (string(encrypted[:3]) == "v10" || string(encrypted[:3]) == "v11") {
		prefix = string(encrypted[:3])
	}

	if h.goos == "windows" {
		if prefix == "" {
			return "", errors.New("unsupported cookie encryption (expected a v10/v11 value)")
		}
		plain, err := decryptGCMWindows(encrypted, dataDir)
		if err != nil {
			return "", err
		}
		if cookie, ok := cookieFromPlaintext(plain); ok {
			return cookie, nil
		}
		return "", errors.New("the decrypted cookie holds no xoxd- value")
	}

	// Chromium uses 1003 PBKDF2 iterations on macOS and 1 on Linux.
	iterations := 1003
	if h.goos == "linux" {
		iterations = 1
	}
	data := encrypted[len(prefix):]
	for password := range keys.passwords(ctx, prefix) {
		plain, err := decryptCBC(data, password, iterations)
		if err != nil {
			continue
		}
		if cookie, ok := cookieFromPlaintext(plain); ok {
			return cookie, nil
		}
	}
	return "", errors.New(keys.failureSummary())
}
