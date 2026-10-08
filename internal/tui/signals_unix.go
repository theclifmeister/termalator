//go:build unix

package tui

import (
	"os"
	"syscall"
)

// The attach client's signals: SIGWINCH resizes, SIGUSR1 asks for a
// digest check (e2e), the rest detach.
var (
	sigResize     os.Signal = syscall.SIGWINCH
	sigDigest     os.Signal = syscall.SIGUSR1
	attachSignals           = []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, sigResize, sigDigest}
)
