package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/thread"
)

func TestRemoteMarkers(t *testing.T) {
	coord := proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo", State: "idle", RemoteControl: true}
	if line := statusLine(coord, nil, false, 120, ""); !strings.Contains(line, "remote control on") {
		t.Fatalf("status bar without the marker: %q", line)
	}
	coord.RemoteControl = false
	if line := statusLine(coord, nil, false, 120, ""); strings.Contains(line, "remote") {
		t.Fatalf("status bar with a marker while off: %q", line)
	}

	rows := buildTree([]ProjectData{{Slug: "demo", Threads: []ThreadRow{{Record: &thread.Record{ID: "t-0001"}}}}, {Slug: "other"}},
		[]proto.SessionInfo{{Role: proto.RoleCoordinator, Project: "demo", State: "idle", RemoteControl: true}},
		treeIn{current: "demo"})
	if len(rows) != 4 || !rows[0].remote || !rows[1].remote || rows[3].remote {
		t.Fatalf("rows = %+v", rows)
	}
	for _, slim := range []bool{false, true} {
		w := 20
		if slim {
			w = sideSlim - 1
		}
		on, off := ansi.Strip(treeLine(rows[0], w, slim, false)), ansi.Strip(treeLine(rows[3], w, slim, false))
		if !strings.Contains(on, remoteMark) || strings.Contains(off, remoteMark) {
			t.Errorf("slim %v: %q / %q", slim, on, off)
		}
		if ansi.StringWidth(on) != w {
			t.Errorf("slim %v: %q is %d wide, want %d", slim, on, ansi.StringWidth(on), w)
		}
	}
	if c := ansi.Strip(treeLine(rows[1], 20, false, false)); !strings.Contains(c, "coordinator"+remoteMark) {
		t.Errorf("coordinator row %q", c)
	}
}

// The user never reads setting names or files in these (only the docs
// describe where the setting lives).
func TestRemoteWording(t *testing.T) {
	var texts []string
	for _, how := range []string{proto.RemoteUnchanged, proto.RemotePrompted, proto.RemoteRestarted} {
		for _, on := range []bool{true, false} {
			texts = append(texts, RemoteMessage("demo", proto.SessionRemoteResult{RemoteControl: on, How: how}))
		}
	}
	texts = append(texts, remoteQuestion(proto.SessionInfo{Project: "demo"}), remoteQuestion(proto.SessionInfo{Project: "demo", RemoteControl: true}))
	texts = append(texts, remoteNote(false, []proto.SessionInfo{{Role: proto.RoleCoordinator, Project: "demo", RemoteControl: true}}, "demo")...)
	for _, s := range texts {
		for _, bad := range []string{"config", "toml", "coordinator_remote_control", "Claude"} {
			if strings.Contains(s, bad) {
				t.Errorf("%q mentions %q", s, bad)
			}
		}
	}
	if note := remoteNote(true, nil, "demo"); note != nil {
		t.Errorf("a note without a running coordinator: %q", note)
	}
}
