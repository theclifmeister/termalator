//go:build unix

package term

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

func enableVT(f *os.File) func() { return func() {} }

// RawMode reports whether f is a terminal with echo and line editing off.
func RawMode(f *os.File) bool {
	t, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil && t.Lflag&(unix.ICANON|unix.ECHO) == 0
}

// events turns SIGWINCH into Resize and SIGHUP, SIGTERM and SIGINT into
// Detach.
func events(ctx context.Context, ch chan<- Event) {
	defer close(ch)
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, unix.SIGWINCH, unix.SIGHUP, unix.SIGTERM, unix.SIGINT)
	defer signal.Stop(sigs)
	for {
		var ev Event
		select {
		case <-ctx.Done():
			return
		case sig := <-sigs:
			ev = Event{Kind: Detach, Why: sig.String()}
			if sig == unix.SIGWINCH {
				ev = Event{Kind: Resize}
			}
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return
		}
	}
}
