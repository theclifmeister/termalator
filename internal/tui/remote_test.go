package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/view"
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
	if len(rows) != 5 || rows[0].remote || !rows[1].remote || rows[2].remote || rows[3].remote || rows[4].remote {
		t.Fatalf("rows = %+v", rows)
	}
	defer setIcons(IconsUnicode)
	for _, name := range IconChoices[1:] {
		setIcons(name)
		set := ic()
		// Never on a project's row, in either width.
		for _, slim := range []bool{false, true} {
			w := sideDefault - 1
			if slim {
				w = sideSlim - 1
			}
			if c := ansi.Strip(treeLine(rows[0], w, slim, false)); strings.Contains(c, set.remote) {
				t.Errorf("%s slim %v: project row %q", set.name, slim, c)
			}
		}
		// On the coordinator's row, one blank before its state glyph, in
		// every width the sidebar takes.
		g, _ := coordLook("idle")
		for w := view.SideMin - 1; w <= 60; w++ {
			for _, here := range []bool{false, true} {
				r := rows[1]
				r.here = here
				c := ansi.Strip(treeLine(r, w, false, false))
				// A Nerd Font icon draws two cells wide, so that set keeps
				// two blanks and one still shows before the state glyph.
				gap := " "
				if set.name == IconsNerd {
					gap = "  "
				}
				if !strings.HasSuffix(c, set.remote+gap+g+" ") {
					t.Errorf("%s w %d here %v: coordinator row %q", set.name, w, here, c)
				}
				if ansi.StringWidth(c) != w {
					t.Errorf("%s w %d: %q is %d wide", set.name, w, c, ansi.StringWidth(c))
				}
				off := rows[1]
				off.remote, off.here = false, here
				if c := ansi.Strip(treeLine(off, w, false, false)); strings.Contains(c, set.remote) || !strings.HasSuffix(c, " "+g+" ") {
					t.Errorf("%s w %d: coordinator row without remote control %q", set.name, w, c)
				}
			}
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
			if !on {
				held := RemoteMessage("demo", proto.SessionRemoteResult{How: how, Held: true})
				if !strings.Contains(held, "stays off until") {
					t.Errorf("%s, held: %q doesn't say it holds", how, held)
				}
				texts = append(texts, held)
			}
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
	off := []proto.SessionInfo{{Role: proto.RoleCoordinator, Project: "demo"}}
	if n := remoteNote(true, off, "demo"); len(n) != 1 || !strings.Contains(n[0], "tm turns it on") {
		t.Errorf("dropped: %q", n)
	}
	off[0].RemoteHeld = true
	if n := remoteNote(true, off, "demo"); len(n) != 1 || !strings.Contains(n[0], "until it is started anew") {
		t.Errorf("held: %q", n)
	}
	if note := remoteNote(true, nil, "demo"); note != nil {
		t.Errorf("a note without a running coordinator: %q", note)
	}
}
