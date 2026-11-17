package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// Timings of the advisory lock on the credentials file; variables so that
// tests can shorten them.
var (
	// lockWait is how long a writer waits for another one to finish.
	lockWait = 10 * time.Second
	// lockStale is the age after which a lock file is taken to have been
	// left behind by a process that died, and is removed. A save talks to
	// the keychain a few times at most, so a live lock is far younger.
	lockStale = 30 * time.Second
	lockPoll  = 20 * time.Millisecond
)

// lockFile takes the advisory lock that serialises read-modify-write
// cycles on the credentials file across processes: a "<path>.lock" file
// created with O_EXCL, which works the same on every OS and file system.
// The returned function releases it.
func lockFile(path string) (unlock func(), err error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		// Any failure is retried: on Windows, creating a file that is
		// being deleted fails with "access denied" rather than "exists".
		if fi, statErr := os.Stat(lock); statErr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the credentials file is locked by another %s process (%s, waited %s: %v): retry, or delete that lock file if no other %s is running",
				appinfo.Name, lock, lockWait, err, appinfo.Name)
		}
		time.Sleep(lockPoll)
	}
}
