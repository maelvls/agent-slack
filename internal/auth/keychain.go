package auth

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// Keychain stores secrets outside the credentials file. The only real
// implementation is the macOS login keychain; elsewhere secrets stay in the
// 0600 credentials file.
type Keychain interface {
	// Get returns the secret, or "" if absent or unavailable.
	Get(service, account string) string
	// Set stores the secret and reports whether it succeeded.
	Set(service, account, value string) bool
	// Delete removes the secret (best effort).
	Delete(service, account string)
}

// SystemKeychain returns the platform keychain, or nil when there is none.
func SystemKeychain() Keychain {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if _, err := exec.LookPath("security"); err != nil {
		return nil
	}
	return macKeychain{}
}

type macKeychain struct{}

func keychainTimeout() time.Duration {
	return appinfo.EnvDurationMS("KEYCHAIN_TIMEOUT_MS", 3*time.Second, false)
}

func (macKeychain) Get(service, account string) string {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Set feeds the command to `security -i` on stdin so that the secret never
// appears in the process list (argv is world-readable via ps).
func (macKeychain) Set(service, account, value string) bool {
	if strings.ContainsAny(service+account+value, "\n\r\x00") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s " + securityQuote(service) +
		" -a " + securityQuote(account) + " -w " + securityQuote(value) + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false
	}
	// `security -i` exits 0 even when the sub-command fails; it reports the
	// failure on stderr.
	return stderr.Len() == 0
}

func (macKeychain) Delete(service, account string) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout())
	defer cancel()
	_ = exec.CommandContext(ctx, "security", "delete-generic-password", "-s", service, "-a", account).Run()
}

// securityQuote quotes an argument for security(1)'s interactive parser.
func securityQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
