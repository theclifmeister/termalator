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
	"strings"
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

// The attach client (docs/SPEC.md §3.3). For each pane it keeps a mirror
// of the session's emulator: restored from the server's snapshot, then
// fed exactly the bytes and resizes the server's emulator gets, in the
// same order. It draws the outer terminal from the mirrors and encodes
// input against the focused mirror's modes, so neither needs a round
// trip.
//
// A window starts with one pane, the session attached to. The prefix
// then % or " splits the focused pane and starts a shell beside or below
// it (panes.go holds the layout); each pane has its own connection,
// mirror and renderer.
//
// A thread's pane is watch-only: the user talks to coordinators, and
// threads to their coordinator (docs/SPEC.md §4). Keys, paste and the
// mouse don't reach it until the user takes it over with the prefix then
// u and confirms; the thread's coordinator is then told.

const (
	frameInterval = time.Second / 120 // render cap
	maxHold       = time.Second       // an app that never ends a 2026 hold is drawn anyway
)

// Outer terminal setup: alternate screen, bracketed paste, kitty keyboard
// "disambiguate" (so Shift+Enter and the prefix are unambiguous), colour
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
	// StatusBar draws the status bar (docs/SPEC.md §4) on the window's
	// last row, kept current by polling the server; the pane is shown in
	// the rows above it. It also means there is a dashboard to go back
	// to. A thread's pane gets the status bar anyway, to show that it is
	// watch-only.
	StatusBar bool
	// Takeover tells a thread's coordinator that the user took over the
	// thread's pane; nil tells no one.
	Takeover func(s proto.SessionInfo) error
	// Sidebar shows the projects sidebar left of the panes (docs/SPEC.md
	// §4); nil shows none (tm attach). It needs StatusBar.
	Sidebar *SidebarOptions
}

// SidebarOptions configure the attach view's projects sidebar.
type SidebarOptions struct {
	// UIFile is ui.json, where its width is kept; empty keeps it in
	// memory only.
	UIFile string
	// Current is the project the dashboard was on, highlighted while the
	// focused pane belongs to none.
	Current string
}

// Result says how an attach ended.
type Result struct {
	// Reason is shown to the user: "detached", "session exited: …".
	Reason string
	// Detached is true when the session is still running.
	Detached bool
	// Then is the dashboard key to run once back on the dashboard: the
	// key typed after the prefix (p, ], [, i, t, , or ?), or "".
	Then string
}

// ErrNotTTY is returned when stdin is not a terminal.
var ErrNotTTY = errors.New("tm attach needs a terminal")

// Attach attaches to a session and runs until the user detaches, the
// last pane's session ends or the outer terminal goes away. The outer
// terminal is restored on every path out, panics included.
func Attach(opts Options) (res Result, err error) {
	if opts.Log == nil {
		opts.Log = log.New(io.Discard, "", 0)
	}
	fd := int(opts.In.Fd())
	if !term.IsTerminal(fd) {
		return res, ErrNotTTY
	}
	prefix, kerr := prefixKey()
	if kerr != nil {
		fmt.Fprintf(os.Stderr, "tm attach: %v; using %s\n", kerr, DefaultPrefixKey)
	}
	cols, rows, err := term.GetSize(fd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	c, err := newClient(opts.Paths, opts.Log)
	if err != nil {
		return res, err
	}
	defer c.close()
	c.prefix, c.statusBar, c.dashboard, c.takeover = prefix, opts.StatusBar, opts.StatusBar, opts.Takeover
	if so := opts.Sidebar; so != nil && opts.StatusBar {
		c.side = &sidebar{layout: LoadLayout(so.UIFile).Sidebar, uiFile: so.UIFile, current: so.Current,
			items: sideItems(loadSideProjects(), nil)}
	}
	c.setWindow(cols, rows)
	p, err := c.open(opts.Session, c.paneCols, c.paneRows)
	if err != nil {
		return res, err
	}
	if p.watch && !c.statusBar {
		c.statusBar = true // says the pane is watch-only
		c.setWindow(cols, rows)
	}
	c.mu.Lock()
	c.root, c.focus = &node{leaf: p}, p
	c.relayout(false) // attaching never resizes the session (§3.3)
	c.mu.Unlock()

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
	go c.readLoop(p)
	go c.inputLoop(ctx, opts.In)
	if c.statusBar {
		go c.pollState(ctx)
	}
	c.poke() // paint the snapshot now, even if the pane is idle
	res = c.renderLoop(opts.Out)
	restore()
	return res, nil
}

// pane is one session in the window.
type pane struct {
	conn *server.Client
	wmu  sync.Mutex // serialises frames to the server

	// Guarded by client.mu.
	mirror   *emu.Terminal
	r        *emu.Renderer
	held     bool // inside the program's 2026 hold
	heldAt   time.Time
	scrolled bool // the local viewport is scrolled back
	watch    bool // a thread's pane, not taken over: no input reaches it
	info     proto.SessionInfo
	rect     rect      // where it is in the window (panes.go)
	asked    [2]uint16 // the size last sent, until the server's RESIZE
	gone     bool      // closed or ended: its goroutines stop
	// restarting: the server relaunches its session under the same id
	// (proto.ClosedRestarting); a failing write isn't a lost server then.
	restarting bool
}

// client is the attached window: its panes and everything they share.
type client struct {
	paths server.Paths
	log   *log.Logger
	enc   *emu.Encoder

	mu       sync.Mutex // guards everything below, and the panes' state
	root     *node      // the layout
	focus    *pane      // keys, paste and the cursor go here
	zoomed   bool       // the focused pane fills the window
	dividers []divider
	single   bool // one pane shown: its renderer has the window to itself
	full     bool // the next frame repaints the whole window
	closed   bool

	prefix      chord
	pending     bool      // the prefix was typed: the next key is a command
	repeatUntil time.Time // until then, a resize key repeats without the prefix
	flash       string    // a note for the status bar until the next key
	confirm     *pane     // asking whether to take over this watch-only pane
	// confirmRemote: asking whether to turn this coordinator's remote
	// control on or off.
	confirmRemote *pane
	takeover      func(proto.SessionInfo) error

	statusBar   bool
	dashboard   bool     // there is a dashboard to go back to
	side        *sidebar // the projects sidebar, nil for none
	sideW       int      // its width in this window, 0 without one
	cols, rows  int      // the window
	paneCols    int      // the columns right of the sidebar
	paneRows    int      // the rows above the status bar
	statusText  string
	statusDrawn string
	lastCursor  string
	title       string
	outer       map[int]bool // focus reports and SGR set on the outer terminal
	outerTrack  int          // the mouse tracking mode set there: 1000, 1002, 1003 or 0
	bell        bool         // ring the outer terminal's bell with the next frame
	buf         []byte

	// detaching is set before DETACH is written. The server may hang up
	// as soon as it reads it, so from then on a failed write or a closed
	// stream is the detach completing, not a lost server. then, set
	// before detaching, is the dashboard key that detach carries.
	detaching atomic.Bool
	then      string

	wake    chan struct{}
	endOnce sync.Once
	end     chan struct{}
	result  Result
}

func newClient(p server.Paths, l *log.Logger) (*client, error) {
	enc, err := emu.NewEncoder()
	if err != nil {
		return nil, err
	}
	return &client{paths: p, log: l, enc: enc, outer: map[int]bool{},
		wake: make(chan struct{}, 1), end: make(chan struct{})}, nil
}

// setWindow records the window's size. c.mu held, or no other goroutine
// running.
func (c *client) setWindow(cols, rows int) {
	c.cols, c.rows, c.paneRows = cols, rows, rows
	if c.statusBar {
		c.paneRows = max(rows-1, 1)
	}
	c.sideW = 0
	if c.side != nil {
		c.sideW = c.side.layout.cols(cols)
	}
	c.paneCols = max(cols-c.sideW, 1)
}

// open attaches to session id as a new pane of cols×rows, its snapshot
// loaded. The pane isn't in the layout yet.
func (c *client) open(id string, cols, rows int) (*pane, error) {
	conn, info, err := server.Attach(c.paths, proto.AttachParams{Session: id, Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	r, err := emu.NewRenderer(uint16(cols), uint16(rows))
	if err != nil {
		conn.Close()
		return nil, err
	}
	p := &pane{conn: conn, r: r, info: *info, watch: info.Role == proto.RoleThread}
	// The stream starts with the pane's snapshot.
	typ, payload, err := conn.ReadFrame()
	if err == nil && typ != proto.FrameSnapshot {
		err = fmt.Errorf("stream starts with frame %d, not a snapshot", typ)
	}
	if err == nil {
		c.mu.Lock()
		err = c.loadSnapshot(p, payload)
		c.mu.Unlock()
	}
	if err != nil {
		conn.Close()
		r.Close()
		return nil, fmt.Errorf("attach %s: %w", info.ID, err)
	}
	return p, nil
}

// close frees every pane. Goroutines still running see c.closed through
// lock and stop.
func (c *client) close() {
	c.mu.Lock()
	c.closed = true
	ps := c.root.leaves(nil)
	for _, p := range ps {
		c.free(p)
	}
	c.enc.Close()
	c.mu.Unlock()
	for _, p := range ps {
		p.conn.Close()
	}
}

// free releases p's mirror and renderer. c.mu held.
func (c *client) free(p *pane) {
	p.gone = true
	if p.mirror != nil {
		p.mirror.Close()
		p.mirror = nil
	}
	if p.r != nil {
		p.r.Close()
		p.r = nil
	}
}

// lock takes c.mu unless the client is closed.
func (c *client) lock() bool {
	c.mu.Lock()
	if c.closed {
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

func (c *client) send(p *pane, typ proto.FrameType, payload []byte) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := p.conn.WriteFrame(typ, payload); err != nil {
		c.lost(p, err)
	}
}

// lost handles p's connection failing with err: the detach completing,
// a pane closed on purpose, or the server gone.
func (c *client) lost(p *pane, err error) {
	if c.detaching.Load() {
		c.finish(c.detachResult())
		return
	}
	c.mu.Lock()
	gone := p.gone || c.closed || p.restarting
	c.mu.Unlock()
	if !gone {
		c.finish(Result{Reason: "lost the server: " + errString(err)})
	}
}

var detached = Result{Reason: "detached", Detached: true}

func (c *client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// loadSnapshot replaces p's mirror (on attach, and when the server
// resyncs a client that fell behind). c.mu held.
func (c *client) loadSnapshot(p *pane, payload []byte) error {
	mirror, err := emu.Decode(payload)
	if err != nil {
		return err
	}
	if p.mirror != nil {
		p.mirror.Close()
		c.log.Printf("resync %s: new snapshot of %d bytes", p.info.ID, len(payload))
	}
	p.mirror = mirror
	p.held, p.scrolled = false, false
	p.asked = [2]uint16{}
	p.r.Invalidate()
	// Runs inside mirror.Write with c.mu held. At hold start the terminal
	// still shows the last complete frame: capture it and keep drawing it.
	mirror.OnRenderHold(func(held bool) {
		if held && p.r != nil {
			p.r.Capture(mirror)
			p.heldAt = time.Now()
		}
		p.held = held
	})
	return nil
}

func (c *client) readLoop(p *pane) {
	for {
		typ, payload, err := p.conn.ReadFrame()
		if err != nil {
			c.lost(p, err)
			return
		}
		if !c.lock() {
			return
		}
		if p.gone {
			c.mu.Unlock()
			return
		}
		switch typ {
		case proto.FrameSnapshot:
			err = c.loadSnapshot(p, payload)
		case proto.FrameOutput:
			p.mirror.Write(payload)
		case proto.FrameResize:
			var cols, rows uint16
			if cols, rows, err = proto.ParseSize(payload); err == nil {
				err = p.mirror.Resize(cols, rows)
				p.r.Invalidate()
				p.asked = [2]uint16{}
			}
		case proto.FrameDigest:
			mine, derr := p.mirror.Digest()
			if derr == nil && mine == string(payload) {
				c.log.Printf("digest ok %s", mine)
			} else {
				c.log.Printf("digest mismatch server=%s client=%s %v", payload, mine, derr)
			}
		case proto.FrameClosed:
			if string(payload) == proto.ClosedRestarting {
				p.restarting = true
				c.flash = paneName(p.info) + " is restarting"
				c.status()
				c.mu.Unlock()
				c.poke()
				go c.reattach(p)
				return
			}
			c.mu.Unlock()
			c.ended(p, string(payload))
			return
		}
		c.mu.Unlock()
		if err != nil {
			c.ended(p, err.Error())
			return
		}
		c.poke()
	}
}

// ended handles p's session ending (or its stream failing) with reason:
// the attach ends with the last pane; otherwise the pane goes and the
// status bar says why.
func (c *client) ended(p *pane, reason string) {
	if !c.lock() {
		return
	}
	if c.root.leaf == p {
		c.mu.Unlock()
		c.finish(Result{Reason: reason})
		return
	}
	c.flash = p.info.ID + ": " + reason
	sizes := c.remove(p)
	c.mu.Unlock()
	p.conn.Close()
	c.sendSizes(sizes)
	c.poke()
}

// remove takes p out of the layout and frees it; the sizes to send
// follow. c.mu held.
func (c *client) remove(p *pane) []resize {
	c.root = removeLeaf(c.root, p)
	c.free(p)
	if c.focus == p {
		c.focus = c.root.leaves(nil)[0]
	}
	c.zoomed = false
	return c.relayout(true)
}

// resize is a pane whose session must take its new size.
type resize struct {
	p          *pane
	cols, rows uint16
	claim      bool // from typing (CLAIM_SIZE), not a window or split change
}

// relayout places the panes in the window after a change and returns the
// sessions to resize, when send is set. The caller sends them once c.mu
// is released (sendSizes). c.mu held.
func (c *client) relayout(send bool) []resize {
	area := rect{c.sideW, 0, c.paneCols, c.paneRows}
	vis := c.visible()
	if c.zoomed {
		c.focus.rect, c.dividers = area, nil
	} else {
		c.dividers = c.root.layout(area, nil)
	}
	// One pane draws alone, unless it shares the window with the sidebar.
	c.single = len(vis) == 1 && c.side == nil
	var out []resize
	for _, p := range vis {
		if c.single {
			p.r.SetSize(uint16(area.w), uint16(area.h))
		} else {
			p.r.SetRect(p.rect.x, p.rect.y, p.rect.w, p.rect.h)
		}
		if cols, rows := p.mirror.Size(); send && (int(cols) != p.rect.w || int(rows) != p.rect.h) {
			p.asked = [2]uint16{uint16(p.rect.w), uint16(p.rect.h)}
			out = append(out, resize{p, p.asked[0], p.asked[1], false})
		}
	}
	c.full = true
	c.status()
	return out
}

// claimSizes: the console typed in sizes the panes it shows (docs/SPEC.md
// §3.3). It returns the visible panes, other than watch-only ones, whose
// session has another size than their rectangle, unless that size was
// already asked for. c.mu held.
func (c *client) claimSizes() []resize {
	var out []resize
	for _, p := range c.visible() {
		if p.watch { // watching never resizes
			continue
		}
		want := [2]uint16{uint16(p.rect.w), uint16(p.rect.h)}
		if cols, rows := p.mirror.Size(); (cols == want[0] && rows == want[1]) || p.asked == want {
			continue
		}
		p.asked = want
		out = append(out, resize{p, want[0], want[1], true})
	}
	return out
}

func (c *client) sendSizes(sizes []resize) {
	for _, s := range sizes {
		typ := proto.FrameSetSize
		if s.claim {
			typ = proto.FrameClaimSize
		}
		c.send(s.p, typ, proto.Size(s.cols, s.rows))
	}
}

// visible are the panes drawn: all, or the zoomed one. c.mu held.
func (c *client) visible() []*pane {
	if c.zoomed {
		return []*pane{c.focus}
	}
	return c.root.leaves(nil)
}

func (c *client) signals(sigs <-chan os.Signal, fd int) {
	for sig := range sigs {
		switch sig {
		case syscall.SIGWINCH:
			// The user really resized the window: only now do the panes
			// follow; attaching never resizes (docs/SPEC.md §3.3).
			cols, rows, err := term.GetSize(fd)
			if err != nil || cols <= 0 || rows <= 0 {
				continue
			}
			if !c.lock() {
				return
			}
			c.setWindow(cols, rows)
			sizes := c.relayout(true)
			c.mu.Unlock()
			c.sendSizes(sizes)
			c.poke()
		case syscall.SIGUSR1:
			// Consistency check: the server puts its digest into the
			// stream, readLoop compares it with the mirror's.
			if c.lock() {
				p := c.focus
				c.mu.Unlock()
				c.send(p, proto.FrameDigestReq, nil)
			}
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

// focused is the focused pane, nil once closed.
func (c *client) focused() *pane {
	if !c.lock() {
		return nil
	}
	defer c.mu.Unlock()
	return c.focus
}

func (c *client) handle(ev uv.Event) {
	switch e := ev.(type) {
	case uv.KeyPressEvent:
		c.key(uv.Key(e))
	case uv.MouseClickEvent, uv.MouseReleaseEvent, uv.MouseMotionEvent, uv.MouseWheelEvent:
		c.mouse(ev)
	case uv.FocusEvent:
		c.focusReport(true)
	case uv.BlurEvent:
		c.focusReport(false)
	case uv.PasteEvent:
		if !c.lock() {
			return
		}
		p := c.focus
		if p.watch {
			c.mu.Unlock()
			return
		}
		b, err := emu.Paste(p.mirror, []byte(e.Content))
		sizes := c.claimSizes()
		c.mu.Unlock()
		c.sendSizes(sizes)
		if err == nil {
			c.send(p, proto.FrameInput, b)
		}
	case uv.DarkColorSchemeEvent, uv.LightColorSchemeEvent:
		scheme := emu.SchemeDark
		if _, ok := e.(uv.LightColorSchemeEvent); ok {
			scheme = emu.SchemeLight
		}
		if !c.lock() {
			return
		}
		ps := c.root.leaves(nil)
		c.mu.Unlock()
		for _, p := range ps {
			c.send(p, proto.FrameColorScheme, []byte{byte(scheme)})
		}
	}
}

// key handles a key from the outer terminal: the prefix and the command
// after it (docs/SPEC.md §4), or a key for the focused program.
func (c *client) key(k uv.Key) {
	if !c.lock() {
		return
	}
	if p := c.confirm; p != nil {
		c.answerTakeover(p, keyName(k) == "y")
		return
	}
	if p := c.confirmRemote; p != nil {
		c.answerRemote(p, keyName(k) == "y")
		return
	}
	pending := c.pending
	do := prefixStep(c.prefix, pending, time.Now().Before(c.repeatUntil), k, c.dashboard)
	c.pending = do.arm
	redraw := pending || do.arm || c.flash != ""
	c.flash = ""
	if redraw {
		c.status()
	}
	c.mu.Unlock()
	if redraw {
		c.poke()
	}
	switch {
	case do.input:
		c.input(k)
	case do.detach:
		c.detachThen(do.then)
	case do.pane != "":
		c.paneCommand(do.pane)
	case do.takeover:
		c.askTakeover()
	case do.remote:
		c.askRemote()
	}
}

// prefixDo is what a key does given whether the prefix came before it.
type prefixDo struct {
	arm    bool   // it is the prefix: the next key is a command
	input  bool   // the program gets it
	detach bool   // detach, then run then on the dashboard
	then   string //
	pane   string // a split-pane command (paneCommands)
	// takeover asks to take over the focused watch-only pane.
	takeover bool
	// remote asks to turn the focused coordinator's remote control on
	// or off.
	remote bool
}

// paneCommands are the keys that, after the prefix, act on the window's
// panes: split beside (%) or below ("), focus (arrows, o), resize
// (ctrl+arrows), zoom (z), close (x) and switch layout (space).
var paneCommands = map[string]bool{
	"%": true, `"`: true, "o": true, "z": true, "x": true, "space": true,
	"{": true, "}": true, "b": true, // the sidebar
	"left": true, "right": true, "up": true, "down": true,
	"ctrl+left": true, "ctrl+right": true, "ctrl+up": true, "ctrl+down": true,
}

// keyName names k as paneCommands does.
func keyName(k uv.Key) string {
	switch {
	case k.Code == uv.KeySpace || k.Text == " ":
		return "space"
	case k.Text != "":
		return k.Text
	}
	return k.String()
}

// prefixStep decides what k does. After the prefix: d detaches, a
// dashboard key detaches and runs there (only when there is a dashboard
// to go back to), a pane command acts on the panes, u takes over a
// watch-only pane, the prefix again goes to the program, and anything
// else cancels. While repeat holds (just
// after a resize) a resize key needs no prefix.
func prefixStep(prefix chord, pending, repeat bool, k uv.Key, dashboard bool) prefixDo {
	name := keyName(k)
	switch {
	case !pending && prefix.match(k):
		return prefixDo{arm: true}
	case !pending && repeat && strings.HasPrefix(name, "ctrl+") && paneCommands[name]:
		return prefixDo{pane: name}
	case !pending, prefix.match(k):
		return prefixDo{input: true}
	case name == "d":
		return prefixDo{detach: true}
	case name == "u":
		return prefixDo{takeover: true}
	case name == "r":
		return prefixDo{remote: true}
	case prefixCommands[name] && dashboard:
		return prefixDo{detach: true, then: name}
	case paneCommands[name]:
		return prefixDo{pane: name}
	}
	return prefixDo{}
}

// detachThen detaches every pane; then is the dashboard key to run
// afterwards.
func (c *client) detachThen(then string) {
	c.then = then
	c.detaching.Store(true) // publishes then to lost
	var ps []*pane
	if c.lock() {
		ps = c.root.leaves(nil)
		c.mu.Unlock()
	}
	for _, p := range ps {
		c.send(p, proto.FrameDetach, nil)
	}
	c.finish(c.detachResult())
}

// detachResult is how a detach ends. c.then is read only once detaching
// is set.
func (c *client) detachResult() Result {
	res := detached
	res.Then = c.then
	return res
}

// paneCommand runs a split-pane command.
func (c *client) paneCommand(cmd string) {
	switch cmd {
	case "%", `"`:
		c.split(cmd == "%")
		return
	case "x":
		c.closePane()
		return
	case "{", "}", "b":
		c.sideKey(cmd)
		return
	}
	if !c.lock() {
		return
	}
	var sizes []resize
	switch cmd {
	case "left", "right", "up", "down":
		d := directions[cmd]
		if c.zoomed {
			c.zoomed = false
			sizes = c.relayout(true)
		}
		if n := neighbour(c.root.leaves(nil), c.focus, d[0], d[1]); n != nil {
			c.setFocus(n)
		}
	case "o":
		ps := c.root.leaves(nil)
		for i, p := range ps {
			if p == c.focus {
				c.setFocus(ps[(i+1)%len(ps)])
				break
			}
		}
		if c.zoomed {
			sizes = c.relayout(true)
		}
	case "z":
		if c.root.leaf == nil {
			c.zoomed = !c.zoomed
			sizes = c.relayout(true)
		}
	case "space":
		if c.root.leaf == nil {
			c.root = even(c.root.leaves(nil), !c.root.side)
			c.zoomed = false
			sizes = c.relayout(true)
		}
	case "ctrl+left", "ctrl+right", "ctrl+up", "ctrl+down":
		side := cmd == "ctrl+left" || cmd == "ctrl+right"
		cells := map[string]int{"ctrl+left": -2, "ctrl+right": 2, "ctrl+up": -1, "ctrl+down": 1}[cmd]
		if !c.zoomed && resizeTowards(c.focus, c.root, side, cells) {
			sizes = c.relayout(true)
		}
		c.repeatUntil = time.Now().Add(resizeRepeat)
	}
	c.mu.Unlock()
	c.sendSizes(sizes)
	c.poke()
}

// directions are the focus keys' (dx, dy).
var directions = map[string][2]int{"left": {-1, 0}, "right": {1, 0}, "up": {0, -1}, "down": {0, 1}}

// resizeRepeat is how long a resize key keeps working without the
// prefix, as tmux's repeat-time.
const resizeRepeat = 500 * time.Millisecond

// setFocus moves the focus to p. c.mu held.
func (c *client) setFocus(p *pane) {
	if p != c.focus {
		c.focus = p
		c.full = true // the dividers' colours, the cursor, the title
		c.status()
	}
}

// split starts a shell in the focused pane's directory and shows it
// beside (side) or below the focused pane, which shrinks to make room.
// Starting and attaching run in the background; the panes change once
// the new one is ready.
func (c *client) split(side bool) {
	if !c.lock() {
		return
	}
	if c.zoomed {
		c.zoomed = false
		sizes := c.relayout(true)
		defer c.sendSizes(sizes)
	}
	at := c.focus
	r := at.rect
	cols, rows := r.w, r.h
	if side {
		_, cols = splitSizes(r.w, 0.5)
	} else {
		_, rows = splitSizes(r.h, 0.5)
	}
	cwd := at.info.Cwd
	if cols < 2 || rows < 1 {
		c.flash = "no room to split"
		c.status()
		c.mu.Unlock()
		c.poke()
		return
	}
	c.mu.Unlock()
	go func() {
		p, err := c.startPane(cwd, cols, rows)
		if err != nil {
			if c.lock() {
				c.flash = "split: " + err.Error()
				c.status()
				c.mu.Unlock()
				c.poke()
			}
			return
		}
		if !c.lock() { // the window closed meanwhile
			p.conn.Close()
			p.mirror.Close()
			p.r.Close()
			return
		}
		if c.root.find(at) == nil {
			at = c.focus // closed meanwhile: split what has the focus now
		}
		c.root = splitLeaf(c.root, at, p, side)
		c.focus = p
		sizes := c.relayout(true)
		c.mu.Unlock()
		c.sendSizes(sizes)
		go c.readLoop(p)
		c.poke()
	}()
}

// startPane starts a shell of cols×rows in cwd and attaches to it.
func (c *client) startPane(cwd string, cols, rows int) (*pane, error) {
	ctl, err := server.Connect(c.paths, false)
	if err != nil {
		return nil, err
	}
	var res proto.SessionStartResult
	err = ctl.Call(proto.MethodSessionStart, proto.SessionStartParams{Cwd: cwd, Cols: uint16(cols), Rows: uint16(rows)}, &res)
	ctl.Close()
	if err != nil {
		return nil, err
	}
	return c.open(res.Session.ID, cols, rows)
}

// closePane detaches the focused pane; its session keeps running. The
// last pane detaches the window.
func (c *client) closePane() {
	if !c.lock() {
		return
	}
	p := c.focus
	if c.root.leaf == p {
		c.mu.Unlock()
		c.detachThen("")
		return
	}
	sizes := c.remove(p)
	c.mu.Unlock()
	c.send(p, proto.FrameDetach, nil)
	p.conn.Close()
	c.sendSizes(sizes)
	c.poke()
}

// askTakeover asks, in the status bar, whether to take over the focused
// pane when it is watch-only.
func (c *client) askTakeover() {
	if !c.lock() {
		return
	}
	if c.focus.watch {
		c.confirm = c.focus
	} else {
		c.flash = "this pane takes your keys already"
	}
	c.status()
	c.mu.Unlock()
	c.poke()
}

// answerTakeover takes over p on yes: it takes keys from now on, for this
// attach, and its coordinator is told. c.mu held; released here.
func (c *client) answerTakeover(p *pane, yes bool) {
	c.confirm = nil
	yes = yes && !p.gone
	if yes {
		p.watch = false
		c.flash = "you took over " + paneName(p.info) + "; its coordinator is told"
	} else {
		c.flash = "still watching"
	}
	c.status()
	info, tell := p.info, c.takeover
	c.mu.Unlock()
	c.poke()
	if yes && tell != nil {
		go func() {
			if err := tell(info); err != nil && c.lock() {
				c.flash = "telling the coordinator failed: " + err.Error()
				c.status()
				c.mu.Unlock()
				c.poke()
			}
		}()
	}
}

// paneName is how the status bar names a session: its thread's id, else
// its own.
func paneName(s proto.SessionInfo) string {
	if s.Thread != "" {
		return s.Thread
	}
	return s.ID
}

// status redraws the status bar. c.mu held.
func (c *client) status() {
	if !c.statusBar || c.focus == nil {
		return
	}
	if p := c.confirmRemote; p != nil {
		line := "\x1b[7m" + fit(" "+remoteQuestion(p.info)+" y yes · any other key no", c.paneCols) + "\x1b[27m"
		if c.single {
			c.focus.r.SetStatus(line)
		}
		c.statusText = line
		return
	}
	if p := c.confirm; p != nil {
		line := "\x1b[7m" + fit(" take over "+paneName(p.info)+" and type into it? Its coordinator is told. y yes · any other key no", c.paneCols) + "\x1b[27m"
		if c.single {
			c.focus.r.SetStatus(line)
		}
		c.statusText = line
		return
	}
	where := ""
	switch {
	case c.focus.watch:
		where = "watch-only, prefix+u takes over"
	case c.focus.info.Role == proto.RoleThread:
		where = "taken over"
	}
	if ps := c.root.leaves(nil); len(ps) > 1 {
		where = strings.TrimPrefix(where+" · ", " · ")
		for i, p := range ps {
			if p == c.focus {
				where += fmt.Sprintf("pane %d/%d", i+1, len(ps))
			}
		}
		if c.zoomed {
			where += " zoomed"
		}
	}
	if c.flash != "" {
		where = strings.TrimPrefix(where+" · "+c.flash, " · ")
	}
	line := statusLine(c.focus.info, c.pending, c.paneCols, where)
	if c.single {
		c.focus.r.SetStatus(line)
	}
	c.statusText = line
}

// input sends a key to the focused program, unless its pane is
// watch-only.
func (c *client) input(k uv.Key) {
	if !c.lock() {
		return
	}
	p := c.focus
	// Shift+PgUp/PgDn scroll this client's own scrollback, for programs on
	// the main screen (shells, inline apps). Full-screen apps get the key.
	if k.Mod == uv.ModShift && (k.Code == uv.KeyPgUp || k.Code == uv.KeyPgDown) && !p.mirror.Modes().AltScreen {
		_, rows := p.mirror.Size()
		d := int(rows) / 2
		if k.Code == uv.KeyPgUp {
			d = -d
		}
		p.mirror.ScrollViewport(d)
		p.scrolled = true
		c.mu.Unlock()
		c.poke()
		return
	}
	if p.watch {
		c.mu.Unlock()
		return
	}
	if p.scrolled {
		p.mirror.ScrollViewportBottom()
		p.scrolled = false
		c.poke()
	}
	ek, ok := toKey(k)
	var b []byte
	var err error
	if ok {
		b, err = c.enc.Key(p.mirror, ek)
	}
	sizes := c.claimSizes()
	c.mu.Unlock()
	c.sendSizes(sizes)
	c.log.Printf("key %s -> %q %v", k, b, err)
	if err == nil && len(b) > 0 {
		c.send(p, proto.FrameInput, b)
	}
}

// mouse forwards a mouse event to the program under it, unless its pane
// is watch-only; a click on another pane focuses that pane first. The outer terminal only reports
// the mouse while the focused program tracks it (see outerModes).
func (c *client) mouse(ev uv.Event) {
	m, ok := toMouse(ev)
	if !ok || !c.lock() {
		return
	}
	if c.side != nil && (m.X < c.sideW || c.side.drag) {
		c.sideMouse(m)
		return
	}
	var p *pane
	for _, q := range c.visible() {
		if r := q.rect; m.X >= r.x && m.X < r.x+r.w && m.Y >= r.y && m.Y < r.y+r.h {
			p = q
		}
	}
	if p == nil { // the status bar or a divider
		c.mu.Unlock()
		return
	}
	_, click := ev.(uv.MouseClickEvent)
	if click && p != c.focus {
		c.setFocus(p)
		c.poke()
	}
	if p.watch {
		c.mu.Unlock()
		return
	}
	var sizes []resize
	if _, wheel := ev.(uv.MouseWheelEvent); click || wheel {
		sizes = c.claimSizes()
	}
	m.X -= p.rect.x
	m.Y -= p.rect.y
	m.Y += p.r.Top()
	cols, rows := p.mirror.Size()
	var b []byte
	var err error
	if m.X < int(cols) && m.Y < int(rows) && p.mirror.Modes().MouseTracking() {
		b, err = c.enc.Mouse(p.mirror, m)
	}
	c.mu.Unlock()
	c.sendSizes(sizes)
	if err == nil && len(b) > 0 {
		c.send(p, proto.FrameInput, b)
	}
}

func (c *client) focusReport(gained bool) {
	if !c.lock() {
		return
	}
	p := c.focus
	if p.watch {
		c.mu.Unlock()
		return
	}
	b := emu.Focus(p.mirror, gained)
	c.mu.Unlock()
	if b != nil {
		c.send(p, proto.FrameInput, b)
	}
}

// outerModes turns mouse tracking and focus reports on the outer terminal
// on or off to match the focused program, so native selection works
// whenever the program doesn't want the mouse. c.mu held.
//
// Terminals keep one mouse tracking mode, not three flags: resetting any
// of 1000, 1002 and 1003 stops all tracking. So the client moves from
// the mode it set to the one it wants, never resetting one after setting
// another.
func (c *client) outerModes() []byte {
	m := c.focus.mirror.Modes()
	track := 0
	switch {
	case m.AnyMouse:
		track = 1003
	case m.ButtonMouse:
		track = 1002
	case m.NormalMouse:
		track = 1000
	}
	if c.side != nil && track < 1002 {
		// The sidebar takes clicks and drags whatever the program wants;
		// selecting text in a pane then needs Shift.
		track = 1002
	}
	var b []byte
	set := func(n int, on bool) {
		b = append(b, "\x1b[?"...)
		b = strconv.AppendInt(b, int64(n), 10)
		if on {
			b = append(b, 'h')
		} else {
			b = append(b, 'l')
		}
	}
	if track != c.outerTrack {
		if c.outerTrack != 0 {
			set(c.outerTrack, false)
		}
		if track != 0 {
			set(track, true)
		}
		c.outerTrack = track
	}
	// Always SGR coordinates from the outer terminal while it reports.
	want := map[int]bool{1004: m.Focus, 1006: track != 0}
	for _, n := range []int{1004, 1006} {
		if c.outer[n] != want[n] {
			c.outer[n] = want[n]
			set(n, want[n])
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
		if !c.lock() {
			continue
		}
		vis := c.visible()
		held := false
		for _, p := range vis {
			if p.held && time.Since(p.heldAt) > maxHold {
				p.held = false // the program never ended its update; draw anyway
			}
			held = held || p.held
		}
		var b []byte
		var err error
		if c.single {
			c.full = false // the renderer repaints after SetSize
			b, err = vis[0].r.Frame(vis[0].mirror, vis[0].held)
		} else {
			b, err = c.frameSplit(vis)
		}
		if err == nil {
			b = append(c.outerModes(), b...)
			if c.bell {
				b = append(b, '\a')
				c.bell = false
			}
		}
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

// frameSplit draws a window of several panes: the dividers, each pane's
// changes, the status bar, then the focused pane's cursor, all in one
// synchronised update; nil when nothing changed. c.mu held.
func (c *client) frameSplit(vis []*pane) ([]byte, error) {
	b := append(c.buf[:0], "\x1b[?2026h\x1b[?25l"...)
	wrote := false
	if c.full {
		b = append(b, "\x1b[0m\x1b[H\x1b[2J"...)
		for _, p := range vis {
			p.r.Invalidate()
		}
		b = c.appendDividers(b)
		c.statusDrawn, c.full, wrote = "", false, true
		if c.side != nil {
			c.side.drawn = nil
		}
	}
	if c.side != nil {
		b, wrote = c.appendSidebar(b, wrote)
	}
	for _, p := range vis {
		part, err := p.r.Frame(p.mirror, p.held)
		if err != nil {
			return nil, err
		}
		if len(part) > 0 {
			b, wrote = append(b, part...), true
		}
	}
	if c.statusBar && c.statusText != c.statusDrawn {
		b = append(b, fmt.Sprintf("\x1b[%d;%dH\x1b[0m", c.rows, c.sideW+1)...)
		b = append(b, c.statusText...)
		b = append(b, "\x1b[0m"...)
		c.statusDrawn, wrote = c.statusText, true
	}
	cur := string(c.focus.r.Cursor())
	title := oneLine(c.focus.mirror.Title())
	if !wrote && cur == c.lastCursor && title == c.title {
		c.buf = b
		return nil, nil
	}
	b = append(b, cur...)
	c.lastCursor = cur
	if title != c.title {
		c.title = title
		b = append(b, "\x1b]2;"...)
		b = append(b, title...)
		b = append(b, '\a')
	}
	b = append(b, "\x1b[?2026l"...)
	c.buf = b
	return b, nil
}

// appendDividers draws the lines between panes; those of the splits
// holding the focused pane in the accent colour. c.mu held.
func (c *client) appendDividers(b []byte) []byte {
	for _, d := range c.dividers {
		st := styleFaint
		if d.n.contains(c.focus) {
			st = styleAccent
		}
		if d.side {
			cell := st.Render("│")
			for y := range d.at.h {
				b = append(b, fmt.Sprintf("\x1b[%d;%dH", d.at.y+y+1, d.at.x+1)...)
				b = append(b, cell...)
			}
			continue
		}
		b = append(b, fmt.Sprintf("\x1b[%d;%dH", d.at.y+1, d.at.x+1)...)
		b = append(b, st.Render(strings.Repeat("─", d.at.w))...)
	}
	return b
}

func errString(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "EOF"
	}
	return err.Error()
}

// statePoll is how often the status bar asks the server for state.
const statePoll = 500 * time.Millisecond

// pollState keeps the status bar current and rings the bell when the
// server sent a notification (a session blocked, a thread reported;
// docs/SPEC.md §4). It polls session.list; a lost server is noticed by
// the attach streams themselves.
func (c *client) pollState(ctx context.Context) {
	var ctl *server.Client
	defer func() {
		if ctl != nil {
			ctl.Close()
		}
	}()
	var alerts uint64
	first := true
	tick := time.NewTicker(statePoll)
	defer tick.Stop()
	for {
		if ctl == nil {
			ctl, _ = server.Connect(c.paths, false)
		}
		var res proto.SessionListResult
		if ctl != nil {
			if err := ctl.Call(proto.MethodSessionList, nil, &res); err != nil {
				ctl.Close()
				ctl = nil
			}
		}
		ring := false
		if ctl != nil {
			ring = !first && res.Alerts > alerts
			alerts, first = res.Alerts, false
		}
		var projects []sideProject
		if c.side != nil {
			projects = loadSideProjects()
		}
		if c.lock() {
			if c.side != nil {
				c.side.items = sideItems(projects, res.Sessions)
			}
			for _, p := range c.root.leaves(nil) {
				for _, s := range res.Sessions {
					if s.ID == p.info.ID {
						p.info = s
					}
				}
			}
			c.status()
			c.bell = c.bell || ring
			c.mu.Unlock()
			c.poke()
		}
		select {
		case <-ctx.Done():
			return
		case <-c.end:
			return
		case <-tick.C:
		}
	}
}
