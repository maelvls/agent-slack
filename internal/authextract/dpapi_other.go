//go:build !windows

package authextract

import "errors"

// dpapiUnprotect only exists on Windows.
func dpapiUnprotect([]byte) ([]byte, error) {
	return nil, errors.New("DPAPI is only available on Windows")
}
