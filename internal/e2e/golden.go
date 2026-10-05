package e2e

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Update makes Golden rewrite golden files instead of comparing.
var Update = flag.Bool("update", false, "rewrite golden screens in testdata/golden/")

// Mask replaces the volatile parts of a screen (ids, pids, times) before
// a golden comparison, so goldens stay stable between runs.
type Mask struct {
	Name string
	Re   *regexp.Regexp
}

// DefaultMasks hide what changes from run to run.
var DefaultMasks = []Mask{
	{"session", regexp.MustCompile(`\bs-\d+\b`)},
	{"pid", regexp.MustCompile(`\bpid \d+\b`)},
	{"duration", regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|ms|s|m|h)\b`)},
}

// Golden compares screen with testdata/golden/<name>, after applying
// DefaultMasks and any extra masks; with -update it rewrites the file.
// A masked part reads <name> in the golden file. For a screen that is
// still being drawn, use WaitGolden.
func Golden(t testing.TB, screen, name string, masks ...Mask) {
	t.Helper()
	WaitGolden(t, 0, func() string { return screen }, name, masks...)
}

// WaitGolden is Golden for a live screen: it compares what screen returns
// until it matches, for up to timeout, so a frame caught half drawn (the
// wait before it saw only its first rows) doesn't fail the test. With
// -update it lets the screen settle first.
func WaitGolden(t testing.TB, timeout time.Duration, screen func() string, name string, masks ...Mask) {
	t.Helper()
	mask := func(s string) string {
		for _, m := range append(append([]Mask{}, DefaultMasks...), masks...) {
			s = m.Re.ReplaceAllString(s, "<"+m.Name+">")
		}
		return s
	}
	path := filepath.Join("testdata", "golden", name)
	if *Update {
		got := mask(screen())
		// Settled: the same screen twice, 300 ms apart.
		for end := time.Now().Add(timeout); time.Now().Before(end); {
			time.Sleep(300 * time.Millisecond)
			next := mask(screen())
			if next == got {
				break
			}
			got = next
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	w := strings.TrimSuffix(string(want), "\n")
	var got string
	if !Poll(timeout, func() bool { got = mask(screen()); return got == w }) {
		t.Fatalf("screen differs from %s (run with -update to accept):\n--- got\n%s\n--- want\n%s", path, got, w)
	}
}

// Golden waits until the window's screen matches testdata/golden/<name>
// (WaitGolden).
func (w *Window) Golden(name string, masks ...Mask) {
	w.env.T.Helper()
	WaitGolden(w.env.T, DefaultTimeout, w.Screen, name, masks...)
}

// GoldenPane is Golden for PaneScreen.
func (w *Window) GoldenPane(name string, masks ...Mask) {
	w.env.T.Helper()
	WaitGolden(w.env.T, DefaultTimeout, w.PaneScreen, name, masks...)
}
