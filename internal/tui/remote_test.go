package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/termalator/internal/proto"
)

func TestRemoteMarkers(t *testing.T) {
	coord := proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo", State: "idle", RemoteControl: true}
	if line := statusLine(coord, false, 120, ""); !strings.Contains(line, "remote control on") {
		t.Fatalf("status bar without the marker: %q", line)
	}
	coord.RemoteControl = false
	if line := statusLine(coord, false, 120, ""); strings.Contains(line, "remote") {
		t.Fatalf("status bar with a marker while off: %q", line)
	}

	items := sideItems([]sideProject{{slug: "demo", threads: 2}, {slug: "other"}},
		[]proto.SessionInfo{{Role: proto.RoleCoordinator, Project: "demo", State: "idle", RemoteControl: true}})
	if !items[0].remote || items[1].remote {
		t.Fatalf("items = %+v", items)
	}
	for _, slim := range []bool{false, true} {
		w := 20
		if slim {
			w = sideSlim - 1
		}
		on, off := ansi.Strip(sideLine(items[0], false, w, slim)), ansi.Strip(sideLine(items[1], false, w, slim))
		if !strings.Contains(on, remoteMark) || strings.Contains(off, remoteMark) {
			t.Errorf("slim %v: %q / %q", slim, on, off)
		}
		if ansi.StringWidth(on) != w {
			t.Errorf("slim %v: %q is %d wide, want %d", slim, on, ansi.StringWidth(on), w)
		}
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
	texts = append(texts, remoteRow(false, []proto.SessionInfo{{Role: proto.RoleCoordinator, Project: "demo", RemoteControl: true}}, "demo")...)
	for _, s := range texts {
		for _, bad := range []string{"config", "toml", "coordinator_remote_control", "Claude"} {
			if strings.Contains(s, bad) {
				t.Errorf("%q mentions %q", s, bad)
			}
		}
	}
	if row := ansi.Strip(strings.Join(remoteRow(true, nil, "demo"), "\n")); !strings.HasPrefix(row, "Remote control: on") {
		t.Errorf("settings row = %q", row)
	}
}
