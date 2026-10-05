package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/termilator/internal/emu"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
)

// feedInterval caps how often the pane under a popup is drawn again.
const feedInterval = time.Second / 30

// paneFeed follows a session's screen for a popup over it (Over): a
// stream of its own that sends nothing, neither input nor a size, so the
// pane under the popup keeps updating while the popup is open
// (docs/SPEC.md §4).
type paneFeed struct {
	conn *server.Client // nil in tests

	mu     sync.Mutex
	mirror *emu.Terminal
	held   bool // inside the program's 2026 hold: the screen is half drawn
	closed bool

	wake chan struct{} // the screen changed; capacity 1
	end  chan struct{} // the stream ended or the feed was closed
	once sync.Once
}

// followPane attaches to session id and starts following it.
func followPane(p server.Paths, id string) (*paneFeed, error) {
	conn, _, err := server.Attach(p, proto.AttachParams{Session: id})
	if err != nil {
		return nil, err
	}
	f := newFeed(conn)
	typ, payload, err := conn.ReadFrame()
	if err == nil && typ != proto.FrameSnapshot {
		err = fmt.Errorf("stream starts with frame %d, not a snapshot", typ)
	}
	if err == nil {
		err = f.load(payload)
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("follow %s: %w", id, err)
	}
	go f.run()
	return f, nil
}

func newFeed(conn *server.Client) *paneFeed {
	return &paneFeed{conn: conn, wake: make(chan struct{}, 1), end: make(chan struct{})}
}

// load replaces the mirror with a snapshot (on attach, and when the
// server resyncs a stream that fell behind).
func (f *paneFeed) load(snapshot []byte) error {
	mirror, err := emu.Decode(snapshot)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		mirror.Close()
		return nil
	}
	f.setMirror(mirror)
	return nil
}

// setMirror makes t the feed's mirror. f.mu held.
func (f *paneFeed) setMirror(t *emu.Terminal) {
	if f.mirror != nil {
		f.mirror.Close()
	}
	f.mirror, f.held = t, false
	// Runs inside Write, f.mu held.
	t.OnRenderHold(func(held bool) { f.held = held })
}

// run reads the stream until it ends.
func (f *paneFeed) run() {
	defer f.stop()
	for {
		typ, payload, err := f.conn.ReadFrame()
		if err != nil {
			return
		}
		switch typ {
		case proto.FrameSnapshot:
			err = f.load(payload)
		case proto.FrameOutput:
			f.write(payload)
		case proto.FrameResize:
			var cols, rows uint16
			if cols, rows, err = proto.ParseSize(payload); err == nil {
				f.mu.Lock()
				if !f.closed {
					err = f.mirror.Resize(cols, rows)
				}
				f.mu.Unlock()
			}
		case proto.FrameClosed:
			return // the session ended or restarts: the popup keeps the last screen
		default:
			continue
		}
		if err != nil {
			return
		}
		f.changed()
	}
}

// write feeds output to the mirror.
func (f *paneFeed) write(b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.mirror.Write(b)
	}
}

// changed wakes whoever draws the feed, unless the program is inside a
// synchronized update: its end wakes them instead.
func (f *paneFeed) changed() {
	f.mu.Lock()
	held := f.held
	f.mu.Unlock()
	if held {
		return
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// screen is the session's visible rows, as plain text.
func (f *paneFeed) screen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.mirror == nil {
		return nil
	}
	s, err := f.mirror.Screen()
	if err != nil {
		return nil
	}
	return strings.Split(s, "\n")
}

// stop ends the feed: no more wakes.
func (f *paneFeed) stop() { f.once.Do(func() { close(f.end) }) }

// close detaches and frees the mirror.
func (f *paneFeed) close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	if f.mirror != nil {
		f.mirror.Close()
	}
	f.mu.Unlock()
	f.stop()
	if f.conn != nil {
		f.conn.WriteFrame(proto.FrameDetach, nil)
		f.conn.Close()
	}
}

// place lays the pane's rows out in the area of n rows under the popup,
// as the attach draws them (X, Y, H, Top).
func (o *Over) place(rows []string, n int) []string {
	lines := make([]string, n)
	pad := strings.Repeat(" ", max(o.X, 0))
	for i := range lines {
		if r := i - o.Y + o.Top; i >= o.Y && i < o.Y+o.H && r >= 0 && r < len(rows) {
			lines[i] = pad + rows[r]
		}
	}
	return lines
}
