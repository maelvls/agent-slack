package composecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// browserCommand returns the program that opens a URL in the user's
// default browser on goos. The URL is always passed as one argument of its
// own, never through a shell.
func browserCommand(goos, url string) (name string, args []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		// Not `cmd /c start`: cmd.exe would re-parse the URL (&, ^, %).
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}

// launcherGrace is how long openBrowser watches the launcher for an early
// failure. Some launchers (xdg-open starting a browser that was not yet
// running) only exit when the browser does.
const launcherGrace = 3 * time.Second

// openBrowser opens url in the user's default browser. It fails when
// there is evidently no browser to open: no launcher, no graphical
// session, or a launcher that exits with an error.
func openBrowser(ctx context.Context, url string) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" &&
		os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		// xdg-open would fall back to a terminal browser, on a terminal
		// that may well be an agent's.
		return errors.New("no graphical session (DISPLAY and WAYLAND_DISPLAY are unset)")
	}
	name, args := browserCommand(runtime.GOOS, url)
	// Not CommandContext: the browser must survive this command.
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	grace := time.NewTimer(launcherGrace)
	defer grace.Stop()
	select {
	case err := <-exited:
		if err != nil {
			return fmt.Errorf("%s failed: %w", name, err)
		}
	case <-grace.C:
	case <-ctx.Done():
	}
	return nil
}
