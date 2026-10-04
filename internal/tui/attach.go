package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/term"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
)

// The attach client (docs/SPEC.md §3.3). It keeps a mirror of the pane's
// emulator: restored from the server's snapshot, then fed exactly the
// bytes and resizes the server's emulator gets, in the same order. It
// draws the outer terminal from the mirror and encodes input against the
// mirror's modes, so neither needs a round trip.

const (
	frameInterval = time.Second / 120 // render cap
	maxHold       = time.Second       // an app that never ends a 2026 hold is drawn anyway
)

// Outer terminal setup: alternate screen, bracketed paste, kitty keyboard
// "disambiguate" (so Shift+Enter and Ctrl+\ are unambiguous), colour
// scheme updates (2031) plus a query for the current scheme.
const (
	outerSetup   = "\x1b[?1049h\x1b[?2004h\x1b[>1u\x1b[?2031h\x1b[?996n"
	outerRestore = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1006l\x1b[?2031l" +
		"\x1b[<u\x1b[?2004l\x1b[0m\x1b[0 q\x1b[?25h\x1b[?1049l"
)

// Options configure Attach.
type Options struct {
	Paths server.Paths
	// Session is the session to attach to.
	Session string
	// In and Out are the outer terminal; In must be a TTY.
	In, Out *os.File
	// Log receives diagnostics (digest checks, key encodings); nil
	// discards them.
	Log *log.Logger
}

// Result says how an attach ended.
type Result struct {
	// Reason is shown to the user: "detached", "session exited: …".
	Reason string
	// Detached is true when the session is still running.
	Detached bool
}

// ErrNotTTY is returned when stdin is not a terminal.
var ErrNotTTY = errors.New("tm attach needs a terminal")

// Attach attaches to a session and runs until the user detaches, the
// session ends or the outer terminal goes away. The outer terminal is
// restored on every path out, panics included.
func Attach(opts Options) (res Result, err error) {
	if opts.Log == nil {
		opts.Log = log.New(io.Discard, "", 0)
	}
	fd := int(opts.In.Fd())
	if !term.IsTerminal(fd) {
		return res, ErrNotTTY
	}
	detach, kerr := detachKey()
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "tm attach: %v; using %s\n", kerr, DefaultDetachKey)
	}
	cols, rows, err := term.GetSize(fd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	conn, info, err := server.Attach(opts.Paths, proto.AttachParams{
		Session: opts.Session, Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return res, err
	}
	defer conn.Close()

	c, err := newClient(conn, opts.Log, uint16(cols), uint16(rows))
	if err != nil {
		return res, err
	}
	defer c.close()
	c.detach = detach
	// The stream starts with the pane's snapshot.
	typ, payload, err := conn.ReadFrame()
	if err != nil {
		return res, fmt.Errorf("attach %s: %w", info.ID, err)
	}
	if typ != proto.FrameSnapshot {
		return res, fmt.Errorf("attach %s: stream starts with frame %d, not a snapshot", info.ID, typ)
	}
	if err := c.loadSnapshot(payload); err != nil {
		return res, err
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return res, err
	}
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			opts.Out.WriteString(outerRestore)
			term.Restore(fd, old)
		})
	}
	defer restore()
	defer func() {
		if p := recover(); p != nil {
			restore()
			panic(p)
		}
	}()
	if _, err := opts.Out.WriteString(outerSetup); err != nil {
		return res, err
	}

	// SIGHUP (the window closed), SIGTERM and SIGINT detach. The server
	// lives in its own session and never sees these.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGWINCH, syscall.SIGUSR1)
	defer signal.Stop(sigs)
	go c.signals(sigs, fd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.readLoop()
	go c.inputLoop(ctx, opts.In)
	c.poke() // paint the snapshot now, even if the pane is idle
	res = c.renderLoop(opts.Out)
	restore()
	return res, nil
}

// client is one attached pane.
type client struct {
	conn *server.Client
	log  *log.Logger
	wmu  sync.Mutex // serialises frames to the server

	mu       sync.Mutex // guards everything below
	mirror   *emu.Terminal
	r        *emu.Renderer
	enc      *emu.Encoder
	held     bool // inside the program's 2026 hold
	heldAt   time.Time
	scrolled bool // the local viewport is scrolled back
	outer    map[int]bool
	detach   chord

	// detaching is set before DETACH is written. The server may hang up
	// as soon as it reads it, so from then on a failed write or a closed
	// stream is the detach completing, not a lost server.
	detaching atomic.Bool

	wake    chan struct{}
	endOnce sync.Once
	end     chan struct{}
	result  Result
}

func newClient(conn *server.Client, l *log.Logger, cols, rows uint16) (*client, error) {
	r, err := emu.NewRenderer(cols, rows)
	if err != nil {
		return nil, err
	}
	enc, err := emu.NewEncoder()
	if err != nil {
		r.Close()
		return nil, err
	}
	return &client{conn: conn, log: l, r: r, enc: enc, outer: map[int]bool{},
		wake: make(chan struct{}, 1), end: make(chan struct{})}, nil
}

// close frees the mirror. Goroutines still running see a nil mirror
// through lock and stop.
func (c *client) close() {
	c.conn.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mirror != nil {
		c.mirror.Close()
		c.mirror = nil
	}
	c.enc.Close()
	c.r.Close()
}

// lock takes c.mu unless the client is closed.
func (c *client) lock() bool {
	c.mu.Lock()
	if c.mirror == nil {
		c.mu.Unlock()
		return false
	}
	return true
}

// finish ends the attach with res; the first call wins.
func (c *client) finish(res Result) {
	c.endOnce.Do(func() {
		c.result = res
		c.log.Printf("end: %s", res.Reason)
		close(c.end)
	})
}

func (c *client) send(typ proto.FrameType, payload []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.conn.WriteFrame(typ, payload); err != nil {
		c.lost(err)
	}
}

// lost ends the attach after the connection failed with err.
func (c *client) lost(err error) {
	if c.detaching.Load() {
		c.finish(detached)
		return
	}
	c.finish(Result{Reason: "lost the server: " + errString(err)})
}

var detached = Result{Reason: "detached", Detached: true}

func (c *client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// loadSnapshot replaces the mirror (on attach, and when the server resyncs
// a client that fell behind). c.mu held, or no other goroutine running.
func (c *client) loadSnapshot(payload []byte) error {
	mirror, err := emu.Decode(payload)
	if err != nil {
		return err
	}
	if c.mirror != nil {
		c.mirror.Close()
		c.log.Printf("resync: new snapshot of %d bytes", len(payload))
	}
	c.mirror = mirror
	c.held, c.scrolled = false, false
	c.r.Invalidate()
	// Runs inside mirror.Write with c.mu held. At hold start the terminal
	// still shows the last complete frame: capture it and keep drawing it.
	mirror.OnRenderHold(func(held bool) {
		if held {
			c.r.Capture(mirror)
			c.heldAt = time.Now()
		}
		c.held = held
	})
	return nil
}

func (c *client) readLoop() {
	for {
		typ, payload, err := c.conn.ReadFrame()
		if err != nil {
			c.lost(err)
			return
		}
		if !c.lock() {
			return
		}
		switch typ {
		case proto.FrameSnapshot:
			err = c.loadSnapshot(payload)
		case proto.FrameOutput:
			c.mirror.Write(payload)
		case proto.FrameResize:
			var cols, rows uint16
			if cols, rows, err = proto.ParseSize(payload); err == nil {
				err = c.mirror.Resize(cols, rows)
				c.r.Invalidate()
			}
		case proto.FrameDigest:
			mine, derr := c.mirror.Digest()
			if derr == nil && mine == string(payload) {
				c.log.Printf("digest ok %s", mine)
			} else {
				c.log.Printf("digest mismatch server=%s client=%s %v", payload, mine, derr)
			}
		case proto.FrameClosed:
			c.mu.Unlock()
			c.finish(Result{Reason: string(payload)})
			return
		}
		c.mu.Unlock()
		if err != nil {
			c.finish(Result{Reason: err.Error()})
			return
		}
		c.poke()
	}
}

func (c *client) signals(sigs <-chan os.Signal, fd int) {
	for sig := range sigs {
		switch sig {
		case syscall.SIGWINCH:
			// The user really resized the window: only now does the pane
			// follow (no resize on attach, docs/SPEC.md §3.3).
			cols, rows, err := term.GetSize(fd)
			if err != nil || cols <= 0 || rows <= 0 {
				continue
			}
			if !c.lock() {
				return
			}
			c.r.SetSize(uint16(cols), uint16(rows))
			c.mu.Unlock()
			c.send(proto.FrameSetSize, proto.Size(uint16(cols), uint16(rows)))
			c.poke()
		case syscall.SIGUSR1:
			// Consistency check: the server puts its digest into the
			// stream, readLoop compares it with the mirror's.
			c.send(proto.FrameDigestReq, nil)
		default:
			c.finish(Result{Reason: "detached (" + sig.String() + ")", Detached: true})
		}
	}
}

func (c *client) inputLoop(ctx context.Context, in *os.File) {
	events := make(chan uv.Event, 64)
	go func() {
		err := uv.NewTerminalReader(in, os.Getenv("TERM")).StreamEvents(ctx, events)
		// EOF or EIO: the outer terminal is gone. That is a detach.
		c.finish(Result{Reason: "detached (terminal closed: " + errString(err) + ")", Detached: true})
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			c.handle(ev)
		}
	}
}

func (c *client) handle(ev uv.Event) {
	switch e := ev.(type) {
	case uv.KeyPressEvent:
		c.key(uv.Key(e))
	case uv.MouseClickEvent, uv.MouseReleaseEvent, uv.MouseMotionEvent, uv.MouseWheelEvent:
		c.mouse(ev)
	case uv.FocusEvent:
		c.focus(true)
	case uv.BlurEvent:
		c.focus(false)
	case uv.PasteEvent:
		if !c.lock() {
			return
		}
		b, err := emu.Paste(c.mirror, []byte(e.Content))
		c.mu.Unlock()
		if err == nil {
			c.send(proto.FrameInput, b)
		}
	case uv.DarkColorSchemeEvent:
		c.send(proto.FrameColorScheme, []byte{byte(emu.SchemeDark)})
	case uv.LightColorSchemeEvent:
		c.send(proto.FrameColorScheme, []byte{byte(emu.SchemeLight)})
	}
}

func (c *client) key(k uv.Key) {
	if c.detach.match(k) {
		c.detaching.Store(true)
		c.send(proto.FrameDetach, nil)
		c.finish(detached)
		return
	}
	if !c.lock() {
		return
	}
	// Shift+PgUp/PgDn scroll this client's own scrollback, for programs on
	// the main screen (shells, inline apps). Full-screen apps get the key.
	if k.Mod == uv.ModShift && (k.Code == uv.KeyPgUp || k.Code == uv.KeyPgDown) && !c.mirror.Modes().AltScreen {
		_, rows := c.mirror.Size()
		d := int(rows) / 2
		if k.Code == uv.KeyPgUp {
			d = -d
		}
		c.mirror.ScrollViewport(d)
		c.scrolled = true
		c.mu.Unlock()
		c.poke()
		return
	}
	if c.scrolled {
		c.mirror.ScrollViewportBottom()
		c.scrolled = false
		c.poke()
	}
	ek, ok := toKey(k)
	var b []byte
	var err error
	if ok {
		b, err = c.enc.Key(c.mirror, ek)
	}
	c.mu.Unlock()
	c.log.Printf("key %s -> %q %v", k, b, err)
	if err == nil && len(b) > 0 {
		c.send(proto.FrameInput, b)
	}
}

// mouse forwards a mouse event to the program. The outer terminal only
// reports the mouse while the program tracks it (see outerModes).
func (c *client) mouse(ev uv.Event) {
	m, ok := toMouse(ev)
	if !ok || !c.lock() {
		return
	}
	m.Y += c.r.Top()
	cols, rows := c.mirror.Size()
	var b []byte
	var err error
	if m.X < int(cols) && m.Y < int(rows) && c.mirror.Modes().MouseTracking() {
		b, err = c.enc.Mouse(c.mirror, m)
	}
	c.mu.Unlock()
	if err == nil && len(b) > 0 {
		c.send(proto.FrameInput, b)
	}
}

func (c *client) focus(gained bool) {
	if !c.lock() {
		return
	}
	b := emu.Focus(c.mirror, gained)
	c.mu.Unlock()
	if b != nil {
		c.send(proto.FrameInput, b)
	}
}

// outerModes turns mouse tracking and focus reports on the outer terminal
// on or off to match the program, so native selection works whenever the
// program doesn't want the mouse. c.mu held.
func (c *client) outerModes() []byte {
	m := c.mirror.Modes()
	want := map[int]bool{1000: m.NormalMouse, 1002: m.ButtonMouse, 1003: m.AnyMouse, 1004: m.Focus,
		1006: m.MouseTracking()} // always SGR coordinates from the outer terminal
	var b []byte
	for _, n := range []int{1000, 1002, 1003, 1004, 1006} {
		if c.outer[n] == want[n] {
			continue
		}
		c.outer[n] = want[n]
		b = append(b, "\x1b[?"...)
		b = strconv.AppendInt(b, int64(n), 10)
		if want[n] {
			b = append(b, 'h')
		} else {
			b = append(b, 'l')
		}
	}
	return b
}

func (c *client) renderLoop(out io.Writer) Result {
	tick := time.NewTicker(frameInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.end:
			return c.result
		case <-c.wake:
		}
		select {
		case <-c.end:
			return c.result
		case <-tick.C:
		}
		c.mu.Lock()
		if c.held && time.Since(c.heldAt) > maxHold {
			c.held = false // the program never ended its update; draw anyway
		}
		b, err := c.r.Frame(c.mirror, c.held)
		if err == nil {
			b = append(c.outerModes(), b...)
		}
		held := c.held
		c.mu.Unlock()
		if err != nil {
			c.finish(Result{Reason: "render: " + err.Error()})
			continue
		}
		if len(b) > 0 {
			if _, err := out.Write(b); err != nil {
				c.finish(Result{Reason: "detached (terminal closed: " + err.Error() + ")", Detached: true})
				continue
			}
		}
		if held {
			time.AfterFunc(maxHold, c.poke) // draw once the hold times out
		}
	}
}

func errString(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "EOF"
	}
	return err.Error()
}
