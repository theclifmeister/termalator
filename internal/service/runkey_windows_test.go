//go:build windows

package service

import (
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/plat/autostart"
)

// Install writes the real HKCU Run value (for a throwaway home, so the
// user's own entry is untouched), and Uninstall removes it.
func TestRunKeyRegistry(t *testing.T) {
	c := Config{GOOS: "windows", Bin: `C:\tm\tm.exe`, Home: `C:\tm-test-home-` + t.Name()}
	t.Cleanup(func() { c.Uninstall() })
	if _, err := c.Install(); err != nil {
		t.Fatal(err)
	}
	got, ok, err := autostart.User().Get(c.JobLabel())
	if err != nil || !ok || !strings.HasSuffix(got, `"C:\tm\tm.exe" server start"`) {
		t.Fatalf("Run value = %q %v %v", got, ok, err)
	}
	if _, err := c.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := autostart.User().Get(c.JobLabel()); ok {
		t.Fatal("value left")
	}
}
