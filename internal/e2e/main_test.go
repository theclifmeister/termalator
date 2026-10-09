package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain cleans up after the whole run: it first clears what an earlier
// run that died (a test timeout, ^C) left behind, and at the end kills
// anything still running from this run's binaries, removes them, and
// names every failed scenario once more with its artifacts, so a failure
// is never lost in a long log.
func TestMain(m *testing.M) {
	flag.Parse()
	sweepStale()
	// go test -timeout panics without running any cleanup: shortly
	// before, kill what the run started, so its servers don't outlive it.
	if d, ok := flag.Lookup("test.timeout").Value.(flag.Getter).Get().(time.Duration); ok && d > time.Minute {
		time.AfterFunc(d-10*time.Second, func() {
			fmt.Fprintln(os.Stderr, "e2e: the test timeout is near; stopping this run's processes")
			finish(false)
		})
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		finish(true)
		os.Exit(1)
	}()
	code := m.Run()
	if !finish(true) && code == 0 {
		code = 1
	}
	os.Exit(code)
}

// finish ends the run; false if processes had to be killed. The
// watchdog passes removeBin=false: the tests still run for the seconds
// before go test panics, and without the binary every one of them fails
// with "fork/exec: no such file", burying the real failure. The next
// run's sweepStale removes what the panic leaves.
func finish(removeBin bool) bool {
	ok := true
	// Waits for a build in progress, and orders the read of binDir
	// after it (finish may run on the watchdog's or a signal's goroutine).
	buildOnce.Do(func() {})
	if binDir != "" {
		if left := append(killUnder(binDir), killUnderRuns()...); len(left) > 0 {
			fmt.Fprintf(os.Stderr, "e2e: killed processes left running: %s\n", strings.Join(left, "; "))
			ok = false
		}
		if removeBin {
			os.RemoveAll(binDir)
		}
	}
	failedMu.Lock()
	defer failedMu.Unlock()
	if len(failed) > 0 {
		msg := fmt.Sprintf("e2e: %d scenario(s) failed:\n  %s\n", len(failed), strings.Join(failed, "\n  "))
		fmt.Fprint(os.Stderr, "\n"+msg)
		if base := os.Getenv("E2E_ARTIFACTS"); base != "" {
			os.WriteFile(filepath.Join(base, "FAILED.txt"), []byte(msg), 0o644)
		}
	}
	return ok
}
