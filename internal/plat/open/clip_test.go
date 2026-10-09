package open

import (
	"bytes"
	"testing"
)

func TestClipCommand(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	for _, c := range []struct {
		goos string
		wsl  bool
		have func(string) bool
		name string
	}{
		{"darwin", false, no, "pbcopy"},
		{"windows", false, no, "clip"},
		{"linux", true, yes, "clip"},
		{"linux", false, yes, "wl-copy"},
		{"linux", false, no, ""},
	} {
		if name, _, _ := clipCommand(c.goos, "x", c.wsl, c.have); name != c.name {
			t.Errorf("%s wsl=%v: %q, want %q", c.goos, c.wsl, name, c.name)
		}
	}
	_, _, in := clipCommand("windows", "aé😀", false, no)
	if want := []byte{0xFF, 0xFE, 'a', 0, 0xE9, 0, 0x3D, 0xD8, 0x00, 0xDE}; !bytes.Equal(in, want) {
		t.Errorf("clip input %x, want %x", in, want)
	}
}
