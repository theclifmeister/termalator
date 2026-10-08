package proc

import (
	"testing"
	"time"
)

func TestParentOf(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	child := Info{PID: 20, PPID: 10, Started: t0}
	for _, c := range []struct {
		name   string
		parent Info
		want   bool
	}{
		{"older", Info{PID: 10, Started: t0.Add(-time.Second)}, true},
		{"same time", Info{PID: 10, Started: t0}, true},
		{"unknown time", Info{PID: 10}, true},
		{"younger: the pid was reused", Info{PID: 10, Started: t0.Add(time.Second)}, false},
		{"another pid", Info{PID: 11, Started: t0.Add(-time.Second)}, false},
	} {
		if got := c.parent.ParentOf(child); got != c.want {
			t.Errorf("%s: ParentOf = %v, want %v", c.name, got, c.want)
		}
	}
	if !(Info{PID: 10, Started: t0.Add(time.Second)}).ParentOf(Info{PPID: 10}) {
		t.Error("a child without a start time has no parent")
	}
}
