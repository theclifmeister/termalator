//go:build unix

package e2e

import (
	"bytes"
	"strings"
	"testing"
)

// TestSmokeWindowWholeFrames: the window shows what a terminal would.
// Inside a mode-2026 synchronized update it keeps the last complete
// frame; TextAt waits for text the program hasn't drawn yet, as a frame
// that isn't synchronized comes in pieces (TestSmokeMouseDashboard found
// the dashboard's top rows, not yet its footer).
func TestSmokeWindowWholeFrames(t *testing.T) {
	env := New(t)
	w := env.WindowCmd(40, 5, "/bin/sh", "-c",
		`printf 'before'; sleep 0.3; printf '\033[?2026h\r\033[Khalf'; sleep 0.5; printf ' done\033[?2026l'; `+
			`sleep 0.3; printf '\r\ntop'; sleep 0.5; printf '\r\nfoot'; sleep 10`)
	w.WaitFor("before", wait)
	if !Poll(wait, func() bool { return bytes.Contains(w.Raw(), []byte("half")) }) {
		t.Fatal("the program never started its update")
	}
	if sc := w.Screen(); strings.Contains(sc, "half") || !strings.Contains(sc, "before") {
		t.Fatalf("the window shows a torn frame:\n%s", sc)
	}
	w.WaitFor("half done", wait)
	w.WaitFor("top", wait)
	if x, y := w.TextAt("foot", 0); x != 0 || y != 2 {
		t.Fatalf("foot at %d,%d:\n%s", x, y, w.Screen())
	}
}
