package open

import (
	"reflect"
	"testing"
)

func TestCommand(t *testing.T) {
	const u = "https://x.test/a?b=1&c=2"
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	for _, c := range []struct {
		goos string
		wsl  bool
		have func(string) bool
		name string
		args []string
	}{
		{"darwin", false, no, "open", []string{u}},
		{"linux", false, yes, "xdg-open", []string{u}},
		{"linux", true, yes, "wslview", []string{u}},
		{"linux", true, no, "explorer.exe", []string{u}},
		{"windows", false, no, "rundll32", []string{"url.dll,FileProtocolHandler", u}},
	} {
		name, args := command(c.goos, u, c.wsl, c.have)
		if name != c.name || !reflect.DeepEqual(args, c.args) {
			t.Errorf("%s wsl=%v: %q %q, want %q %q", c.goos, c.wsl, name, args, c.name, c.args)
		}
	}
}
