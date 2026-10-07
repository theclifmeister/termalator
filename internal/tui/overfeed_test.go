package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/emu"
)

// TestDashboardOverLive: the popup over a session follows its output:
// what the session draws shows under the popup, placed as in the attach,
// and a synchronized update shows once it ends, not half drawn.
func TestDashboardOverLive(t *testing.T) {
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30,
		Over: &Over{Key: "i", Project: "alpha", Session: "s-5", Screen: make([]string, 28), X: 2, Y: 0, H: 28}})
	m.setData(src.data)
	mirror, err := emu.New(60, 28)
	if err != nil {
		t.Fatal(err)
	}
	f := newFeed(nil)
	f.mu.Lock()
	f.setMirror(mirror)
	f.mu.Unlock()
	m.feed = f
	defer f.close()

	draw := func(s string) {
		t.Helper()
		f.write([]byte(s))
		f.changed()
		select {
		case <-f.wake:
		default:
			t.Fatalf("no wake after %q", s)
		}
		m.Update(feedMsg{})
	}
	draw("\r\nfirst output")
	if out := screen(m); !strings.Contains(out, "  first output") {
		t.Fatalf("the output isn't under the popup:\n%s", out)
	}
	if _, ok := m.top().(*projectView); !ok {
		t.Fatalf("the popup closed: %T", m.top())
	}

	f.write([]byte("\x1b[?2026h\r\nhalf drawn"))
	f.changed()
	if len(f.wake) != 0 {
		t.Fatal("a held frame woke the popup")
	}
	draw(" and done\x1b[?2026l")
	if out := screen(m); !strings.Contains(out, "half drawn and done") {
		t.Fatalf("the finished frame isn't under the popup:\n%s", out)
	}

	// prefix d: the dashboard, which stops following the session.
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.feed != nil || f.screen() != nil {
		t.Fatal("the feed outlived the popup")
	}
	select {
	case <-f.end:
	default:
		t.Fatal("the feed didn't end")
	}
}

// TestDashboardOverMessage: a key that can't open its popup over a
// session goes back to the session with the reason, for its status bar.
func TestDashboardOverMessage(t *testing.T) {
	src := &fakeSource{data: Data{ServerOK: true}}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30,
		Over: &Over{Key: "a", Session: "s-5", Screen: []string{"a shell"}}})
	m.setData(src.data)
	m.Update(tickMsg{})
	if m.result.Attach != "s-5" || m.result.Message != "no project; n creates one" {
		t.Fatalf("prefix a without a project: result %+v", m.result)
	}
}

// TestOverPlace: the pane's rows go where the attach draws them.
func TestOverPlace(t *testing.T) {
	o := &Over{X: 1, Y: 1, H: 2, Top: 1}
	got := o.place([]string{"r0", "r1", "r2", "r3"}, 4)
	want := []string{"", " r1", " r2", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("place = %q, want %q", got, want)
	}
}
