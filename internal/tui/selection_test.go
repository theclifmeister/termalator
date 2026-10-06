package tui

import (
	"io"
	"log"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/view"
)

// selClient is an attach client with the sidebar, showing session s-1
// whose program wrote out; the pane has no connection, so anything sent
// to the program would panic.
func selClient(t *testing.T, out string) (*client, *pane) {
	t.Helper()
	c, err := newClient(server.Paths{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	c.vc = &ViewConn{}
	c.side = &sidebar{}
	c.v = view.View{Name: view.Main, Mode: view.ModeLayout, Focus: "s-1", Cols: 100, Rows: 30}
	c.v.Normalize()
	c.setWindow(100, 30)
	c.relayout()
	a := c.geo.Area
	src, err := emu.New(uint16(a.W), uint16(a.H))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.Write([]byte(out))
	snap, err := src.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, err := emu.NewRenderer(uint16(a.W), uint16(a.H))
	if err != nil {
		t.Fatal(err)
	}
	p := &pane{r: r, info: proto.SessionInfo{ID: "s-1"}}
	t.Cleanup(func() { // before c.close, which would close its connection
		delete(c.panes, "s-1")
		p.mirror.Close()
		r.Close()
	})
	c.mu.Lock()
	err = c.loadSnapshot(p, snap)
	c.panes["s-1"] = p
	c.relayout()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if c.focus != p || p.rect.W == 0 {
		t.Fatalf("pane not shown: focus %v rect %+v", c.focus, p.rect)
	}
	return c, p
}

// TestDragSelectsAndCopies: over a shell (no mouse tracking) a left drag
// selects in tm, follows the mouse out of the pane, and its release puts
// the text on the outer terminal's clipboard with OSC 52; a click clears
// it without copying.
func TestDragSelectsAndCopies(t *testing.T) {
	c, p := selClient(t, "$ echo hello\r\nhello\r\n$ ")
	x, y := p.rect.X, p.rect.Y
	c.mouse(uv.MouseClickEvent{X: x + 2, Y: y, Button: uv.MouseLeft})
	c.mouse(uv.MouseMotionEvent{X: x + 4, Y: y + 1, Button: uv.MouseLeft})
	if got, _ := p.mirror.SelectionText(); got != "echo hello\nhello" {
		t.Fatalf("while dragging: %q", got)
	}
	if len(c.clip) != 0 {
		t.Fatal("copied before the release")
	}
	// Into the sidebar: the selection clamps to the pane's first column.
	c.mouse(uv.MouseMotionEvent{X: 0, Y: y + 1, Button: uv.MouseLeft})
	c.mouse(uv.MouseReleaseEvent{X: 0, Y: y + 1, Button: uv.MouseLeft})
	want := string(emu.OSC52('c', []byte("echo hello\nh")))
	if string(c.clip) != want {
		t.Fatalf("clip %q, want %q", c.clip, want)
	}
	if c.flash != "copied 2 lines" {
		t.Fatalf("flash %q", c.flash)
	}
	if c.side.drag {
		t.Fatal("the drag reached the sidebar")
	}
	if got, _ := p.mirror.SelectionText(); got == "" {
		t.Fatal("the selection went away on release")
	}
	c.clip = nil
	c.mouse(uv.MouseClickEvent{X: x + 1, Y: y + 1, Button: uv.MouseLeft})
	c.mouse(uv.MouseReleaseEvent{X: x + 1, Y: y + 1, Button: uv.MouseLeft})
	if got, _ := p.mirror.SelectionText(); got != "" || len(c.clip) != 0 {
		t.Fatalf("a click: selection %q, clip %q", got, c.clip)
	}
}

// TestPaneClipboardForwarded: the focused program's OSC 52 goes to the
// outer terminal, from this console only while it is the view's latest
// typist; reads never do.
func TestPaneClipboardForwarded(t *testing.T) {
	c, p := selClient(t, "")
	c.mu.Lock()
	defer c.mu.Unlock()
	p.mirror.Write([]byte("\x1b]52;c;aGk=\a\x1b]52;c;?\a"))
	if want := string(emu.OSC52('c', []byte("hi"))); string(c.clip) != want {
		t.Fatalf("clip %q, want %q", c.clip, want)
	}
	c.clip = nil
	c.me, c.v.Latest = "c-1", "c-2"
	p.mirror.Write([]byte("\x1b]52;c;aGk=\a"))
	if len(c.clip) != 0 {
		t.Fatalf("another console types here, yet clip %q", c.clip)
	}
	c.v.Latest = "c-1"
	p.mirror.Write([]byte("\x1b]52;p;aGk=\x1b\\"))
	if want := string(emu.OSC52('p', []byte("hi"))); string(c.clip) != want {
		t.Fatalf("clip %q, want %q", c.clip, want)
	}
}
