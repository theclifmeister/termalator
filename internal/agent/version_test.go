package agent

import "testing"

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		v, min string
		want   bool
	}{
		{"2.1.289 (Claude Code)", "2.1.289", true},
		{"2.1.291 (Claude Code)", "2.1.289", true},
		{"2.1.288", "2.1.289", false},
		{"2.1.1000", "2.1.289", true},
		{"2.2", "2.1.289", true},
		{"2.1", "2.1.289", false},
		{"10.0.0", "9.9.9", true},
		{"", "2.1.289", false},
		{"Claude Code", "2.1.289", false},
	} {
		if got := VersionAtLeast(c.v, c.min); got != c.want {
			t.Errorf("VersionAtLeast(%q, %q) = %v", c.v, c.min, got)
		}
	}
}
