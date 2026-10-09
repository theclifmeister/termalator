//go:build !unix && !windows

package term

import (
	"context"
	"os"
	"os/signal"
)

// No port: RawMode never sees raw mode, and only an interrupt arrives, as
// Detach.

func enableVT(f *os.File) func() { return func() {} }

func RawMode(f *os.File) bool { return false }

func events(ctx context.Context, ch chan<- Event) {
	defer close(ch)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigs:
			select {
			case ch <- Event{Kind: Detach, Why: sig.String()}:
			case <-ctx.Done():
				return
			}
		}
	}
}
