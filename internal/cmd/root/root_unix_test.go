//go:build unix

package root

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// Ctrl-C cancels the command's context; the run ends with exit code 130
// and "interrupted", not with the command's own error.
func TestRunInterrupt(t *testing.T) {
	waiting := &cobra.Command{Use: "wait", RunE: func(cmd *cobra.Command, _ []string) error {
		// Run has installed its signal handler by now, so the signal
		// reaches it instead of killing the test binary.
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			return err
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(10 * time.Second):
			return nil
		}
	}}
	got := runWith(t, time.Second, one(waiting), "wait")
	if got.code != 130 || got.stderr != "interrupted\n" {
		t.Errorf("got %+v", got)
	}
}
