package e2e

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
// A masked part reads <name> in the golden file.
func Golden(t testing.TB, screen, name string, masks ...Mask) {
	t.Helper()
	got := screen
	for _, m := range append(append([]Mask{}, DefaultMasks...), masks...) {
		got = m.Re.ReplaceAllString(got, "<"+m.Name+">")
	}
	path := filepath.Join("testdata", "golden", name)
	if *Update {
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
	if w := strings.TrimSuffix(string(want), "\n"); got != w {
		t.Fatalf("screen differs from %s (run with -update to accept):\n--- got\n%s\n--- want\n%s", path, got, w)
	}
}
