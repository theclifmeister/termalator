package tui

import (
	"cmp"
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
	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/thread"
	"github.com/theclifmeister/termalator/internal/view"
)

// The attach client (docs/SPEC.md §3.3). It draws a server-owned view's
// pane: the session the view shows, with its own attach connection and a
// mirror of the session's emulator, restored
// from the server's snapshot, then fed exactly the bytes and resizes the
// server's emulator gets, in the same order. It draws the outer terminal
// from the mirror and encodes input against the mirror's modes,
// so neither needs a round trip.
//
// The view is the server's, not the client's: showing a session, the
// sidebar's width and tree are view.* calls, and every console joined to
// the view redraws from the version that comes back. The pane's
// rectangle is laid out at the view's size, the window of its latest
// client; a smaller window shows the same frame cropped, a larger one
// padded. What stays here is this console's own: its window, the outer
// terminal's modes, the local scrollback, the prefix and the takeovers.
//
// A thread's pane is watch-only: the user talks to coordinators, and
// threads to their coordinator (docs/SPEC.md §4). Keys, paste and the
// mouse don't reach it until the user takes it over with the prefix then
// u and confirms; the thread's coordinator is then told. A takeover is
// this console's, for this attach.

const (
	frameInterval = time.Second / 120 // render cap
	maxHold       = time.Second       // an app that never ends a 2026 hold is drawn anyway
	// endWait is how long the last pane's session may be gone before the
	// view says so; then the attach ends anyway.
	endWait = 3 * time.Second
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
	// View is the view drawn, in its layout mode. A bare view (tm attach,
	// tm project open) has no dashboard to go back to: detaching leaves.
	View *ViewConn
	// In and Out are the outer terminal; In must be a TTY.
	In, Out *os.File
	// Log receives diagnostics (digest checks, key encodings); nil
	// discards them.
	Log *log.Logger
	// Takeover tells a thread's coordinator that the user took over the
	// thread's pane; nil tells no one.
	Takeover func(s proto.SessionInfo) error
	// Sidebar configures the projects sidebar, which every view shows
	// (docs/SPEC.md §4); nil is the defaults.
	Sidebar *SidebarOptions
	// TakeOver is a session to ask about taking over once its pane has
	// the focus: "take over…" picked from a dashboard menu.
	TakeOver string
}

// SidebarOptions configure the attach view's projects sidebar.
type SidebarOptions struct {
	// UIFile is ui.json, where its width is kept as the default of new
	// views; empty keeps it nowhere.
	UIFile string
	// Agent runs the coordinator a click starts; empty is DefaultAgent.
	Agent string
}

// DefaultAgent runs coordinators.
const DefaultAgent = "claude"

// Result says how an attach ended.
type Result struct {
	// Reason is shown to the user: "detached", "session exited: …".
	Reason string
	// Detached is true when the session is still running.
	Detached bool
	// Session is the session that had the focus.
	Session string
	// Then is the dashboard key to run once back on the dashboard: the
	// key typed after the prefix (p, ], [, i, t, , or ?), or "".
	Then string
	// GoTo is a sidebar row clicked in a bare view, which has no
	// dashboard: the caller hands the console over to view main, which
	// opens it (OpenTarget).
	GoTo *Target
	// Quit is set when the console itself went away (its terminal closed,
	// a signal): the client exits rather than show the dashboard.
	Quit bool
}

// ErrNotTTY is returned when stdin is not a terminal.
var ErrNotTTY = errors.New("tm attach needs a terminal")

// Attach draws the view until it leaves its layout mode (detached, the
// last pane closed or ended), the user detaches from a bare view or the
// outer terminal goes away. The outer terminal is restored on every path
// out, panics included.
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
	vc := opts.View
	v := vc.View()
	c.vc, c.me = vc, vc.Client()
	c.prefix, c.takeover, c.askFor = prefix, opts.Takeover, opts.TakeOver
	c.bare, c.dashboard = v.Bare, !v.Bare
	so := opts.Sidebar
	if so == nil {
		so = &SidebarOptions{}
	}
	c.side = &sidebar{uiFile: so.UIFile, agent: cmp.Or(so.Agent, DefaultAgent), projects: loadSideProjects()}
	c.setWindow(cols, rows)
	watch, stopWatch := vc.Watch()
	defer stopWatch()
	if err := c.sync(v); err != nil {
		return res, err
	}
	if c.empty() {
		return Result{Reason: "no session to show", Detached: true}, nil
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
	go c.viewLoop(watch)
	go c.inputLoop(ctx, opts.In)
	if c.statusBar || c.side != nil {
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
	status   *thread.Status // a thread's STATUS.md, for the status bar
	rect     view.Rect      // where it is in this window; empty when cropped away
	gone     bool           // closed or ended: its goroutines stop
	// restarting: the server relaunches its session under the same id
	// (proto.ClosedRestarting); a failing write isn't a lost server then.
	restarting bool
}

// client is the attached window: its panes and everything they share.
type client struct {
	paths server.Paths
	log   *log.Logger
	enc   *emu.Encoder
	vc    *ViewConn
	me    string // this console's id in the view
	// syncMu serialises sync: the view loop, and actions drawing their
	// answer at once.
	syncMu sync.Mutex

	mu     sync.Mutex // guards everything below, and the panes' state
	v      view.View  // the version drawn
	geo    view.Geometry
	panes  map[string]*pane
	focus  *pane // the view's pane: keys, paste and the cursor go here; nil without one
	single bool  // the pane draws alone: its renderer has the window to itself
	full   bool  // the next frame repaints the whole window
	closed bool
	// claimSeq is the view's version when this console last claimed the
	// size by typing: it claims again only once the view changed.
	claimSeq uint64
	claimed  bool
	// lastReason is why the last pane's session ended, for the result.
	lastReason string

	prefix  chord
	pending bool   // the prefix was typed: the next key is a command
	flash   string // a note for the status bar until the next key
	confirm *pane  // asking whether to take over this watch-only pane
	// confirmRemote: asking whether to turn this coordinator's remote
	// control on or off.
	confirmRemote *pane
	takeover      func(proto.SessionInfo) error
	// askFor is a session to ask about taking over once its pane has the
	// focus (a menu's "take over…").
	askFor string

	// The mouse (attachmouse.go): the open menu and the status bar's
	// buttons, by column from its start.
	menu       *amenu
	statusHits []hint

	bare      bool     // the view has no dashboard: detaching leaves
	statusBar bool     // the view's chrome has the status bar
	dashboard bool     // there is a dashboard to go back to
	side      *sidebar // the projects sidebar, nil for none
	sideW     int      // its width, 0 without one
	// sideFocus: the sidebar has this console's keyboard (prefix+tab);
	// the panes get no keys, paste or prefix meanwhile.
	sideFocus   bool
	cols, rows  int // the window
	paneCols    int // the columns right of the sidebar
	paneRows    int // the rows above the status bar and the empty row over it
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
	goTo      *Target // a sidebar click in a bare view, carried like then

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
	return &client{paths: p, log: l, enc: enc, outer: map[int]bool{}, panes: map[string]*pane{},
		wake: make(chan struct{}, 1), end: make(chan struct{})}, nil
}

// setWindow records the window's size. c.mu held, or no other goroutine
// running.
func (c *client) setWindow(cols, rows int) {
	c.cols, c.rows = cols, rows
	c.paneCols = max(cols-c.sideW, 1)
	c.paneRows = rows
	if c.statusBar {
		c.paneRows = max(rows-2, 1)
	}
}

// empty says whether no pane is open.
func (c *client) empty() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.panes) == 0
}

// open attaches to session id as a new pane, its snapshot loaded. The
// pane isn't in the window yet.
func (c *client) open(id string) (*pane, error) {
	conn, info, err := server.Attach(c.paths, proto.AttachParams{Session: id, Cols: uint16(c.cols), Rows: uint16(c.rows)})
	if err != nil {
		return nil, err
	}
	r, err := emu.NewRenderer(uint16(c.cols), uint16(c.rows))
	if err != nil {
		conn.Close()
		return nil, err
	}
	p := &pane{conn: conn, r: r, info: *info, watch: info.Role == proto.RoleThread,
		status: threadStatuses([]proto.SessionInfo{*info})[info.ID]}
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

// sync makes the window show v: it opens a pane for every session new to
// the layout, closes those that left it, and lays the window out again.
// A build mismatch is returned (the client re-execs); a session that
// can't be attached is skipped with a note in the status bar.
func (c *client) sync(v view.View) error {
	c.mu.Lock()
	have := map[string]bool{}
	for id := range c.panes {
		have[id] = true
	}
	c.mu.Unlock()
	var opened []*pane
	var note string
	for _, id := range v.Visible() {
		if have[id] {
			continue
		}
		p, err := c.open(id)
		var verr *proto.MismatchError
		if errors.As(err, &verr) {
			for _, p := range opened {
				p.conn.Close()
			}
			return err
		}
		if err != nil {
			c.log.Printf("view %s: %s: %v", v.Name, id, err)
			note = id + ": " + err.Error()
			continue
		}
		opened = append(opened, p)
	}
	if !c.lock() || v.Name == c.v.Name && v.Seq < c.v.Seq {
		// Closed, or a newer version was drawn meanwhile.
		if !c.closed {
			c.mu.Unlock()
		}
		for _, p := range opened {
			p.conn.Close()
			p.r.Close()
			p.mirror.Close()
		}
		return nil
	}
	var gone []*pane
	for id, p := range c.panes {
		if !v.Has(id) {
			gone = append(gone, p)
			delete(c.panes, id)
			c.free(p)
		}
	}
	for _, p := range opened {
		c.panes[p.info.ID] = p
	}
	if note != "" {
		c.flash = note
	}
	c.v = v
	c.relayout()
	c.mu.Unlock()
	for _, p := range gone {
		c.send(p, proto.FrameDetach, nil)
		p.conn.Close()
	}
	for _, p := range opened {
		go c.readLoop(p)
	}
	c.poke()
	return nil
}

// viewLoop follows the view: each new version is drawn, and the attach
// ends when the view leaves its layout mode or the server goes away.
func (c *client) viewLoop(watch <-chan struct{}) {
	for {
		select {
		case <-c.end:
			return
		case <-watch:
		}
		if !c.vc.Up() {
			c.finish(Result{Reason: "lost the server"})
			return
		}
		v := c.vc.View()
		if !c.lock() {
			return
		}
		same := v.Name == c.v.Name && v.Seq == c.v.Seq
		c.mu.Unlock()
		if same {
			continue
		}
		if v.Mode != view.ModeLayout || v.Focus == "" {
			c.finish(c.endResult())
			return
		}
		c.syncMu.Lock()
		err := c.sync(v)
		c.syncMu.Unlock()
		if err != nil {
			c.finish(Result{Reason: err.Error()})
			return
		}
	}
}

// apply draws v now when it is newer than what the window shows and
// still a layout; the view loop handles the rest.
func (c *client) apply(v view.View) {
	if !c.lock() {
		return
	}
	newer := v.Name == c.v.Name && v.Seq > c.v.Seq
	c.mu.Unlock()
	if newer && v.Mode == view.ModeLayout && v.Focus != "" {
		c.syncMu.Lock()
		defer c.syncMu.Unlock()
		c.sync(v)
	}
}

// endResult is how the attach ends when the view leaves its layout: a
// detach (here or from another console), or the last session ending.
func (c *client) endResult() Result {
	if c.detaching.Load() {
		return c.detachResult()
	}
	c.mu.Lock()
	reason := c.lastReason
	c.mu.Unlock()
	if reason != "" {
		return Result{Reason: reason}
	}
	return detached
}

// close frees every pane. Goroutines still running see c.closed through
// lock and stop.
func (c *client) close() {
	c.mu.Lock()
	c.closed = true
	var ps []*pane
	for _, p := range c.panes {
		ps = append(ps, p)
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
	if c.focus == p {
		c.focus = nil
	}
	if c.confirm == p {
		c.confirm = nil
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
		c.mu.Lock()
		if c.focus != nil {
			res.Session = c.focus.info.ID
		} else if res.Session == "" {
			res.Session = c.v.Focus
		}
		c.mu.Unlock()
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

// ended handles p's session ending (or its stream failing) with reason.
// The server takes the session out of the view, which every console then
// draws; here the pane goes at once and the status bar says why. With
// the last pane gone the attach ends once the view says so, or after
// endWait.
func (c *client) ended(p *pane, reason string) {
	if !c.lock() {
		return
	}
	if p.gone {
		c.mu.Unlock()
		return
	}
	delete(c.panes, p.info.ID)
	c.free(p)
	last := len(c.panes) == 0
	if last {
		c.lastReason = reason
	} else {
		c.flash = paneName(p.info) + ": " + reason
		c.relayout()
	}
	c.mu.Unlock()
	p.conn.Close()
	if last {
		time.AfterFunc(endWait, func() { c.finish(Result{Reason: reason}) })
	}
	c.poke()
}

// relayout places the panes in the window after a change of the view or
// the window. The view is laid out at its size, the window of its latest
// client (docs/SPEC.md §3.3), and drawn into this window from the top
// left: cropped when this one is smaller, padded when it is larger. c.mu
// held.
func (c *client) relayout() {
	vcols, vrows := int(c.v.Cols), int(c.v.Rows)
	if vcols == 0 || vrows == 0 {
		vcols, vrows = c.cols, c.rows
	}
	c.geo = c.v.Lay(vcols, vrows)
	c.sideW = 0
	if c.side != nil {
		c.sideW = min(c.geo.SideW, max(c.cols-1, 1))
	}
	c.statusBar = c.geo.Status > 0
	c.paneCols = max(c.cols-c.sideW, 1)
	c.paneRows = max(c.rows-c.geo.Status, 1)
	own := view.Rect{X: c.sideW, Y: 0, W: c.paneCols, H: c.paneRows}
	c.focus = c.panes[c.v.Focus]
	if c.askFor != "" && c.focus != nil && c.focus.info.ID == c.askFor {
		// A menu's "take over…": ask now its pane has the focus.
		if c.focus.watch {
			c.confirm = c.focus
		}
		c.askFor = ""
	}
	vis := c.visible()
	// The pane draws alone, unless it shares the window with the sidebar
	// or the status bar (which has an empty row above it).
	c.single = len(vis) == 1 && c.side == nil && !c.statusBar
	for _, p := range vis {
		if c.single {
			p.rect = own
			p.r.SetSize(uint16(own.W), uint16(own.H))
			continue
		}
		p.rect = clip(c.geo.Panes[p.info.ID], own)
		if p.rect.W > 0 && p.rect.H > 0 {
			p.r.SetRect(p.rect.X, p.rect.Y, p.rect.W, p.rect.H)
		}
	}
	c.full = true
	c.status()
}

// clip is r within area; empty when they don't meet.
func clip(r, area view.Rect) view.Rect {
	x0, y0 := max(r.X, area.X), max(r.Y, area.Y)
	x1, y1 := min(r.X+r.W, area.X+area.W), min(r.Y+r.H, area.Y+area.H)
	if x1 <= x0 || y1 <= y0 {
		return view.Rect{}
	}
	return view.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// visible are the panes drawn: all the view shows that are open here.
// c.mu held.
func (c *client) visible() []*pane {
	var out []*pane
	for _, id := range c.v.Visible() {
		if p := c.panes[id]; p != nil {
			out = append(out, p)
		}
	}
	return out
}

// shown are the visible panes that aren't cropped away. c.mu held.
func (c *client) shown() []*pane {
	var out []*pane
	for _, p := range c.visible() {
		if p.rect.W > 0 && p.rect.H > 0 {
			out = append(out, p)
		}
	}
	return out
}

// leaves are the open panes: the view's one, when open. c.mu held.
func (c *client) leaves() []*pane { return c.visible() }

// needClaim says whether typing into this console must first claim the
// view's size (docs/SPEC.md §3.3): when another console sizes the view,
// or a pane it shows has another size than its rectangle. It claims once
// per version of the view. c.mu held.
func (c *client) needClaim() bool {
	if c.claimed && c.claimSeq == c.v.Seq {
		return false
	}
	need := c.v.Latest != c.me
	for _, p := range c.visible() {
		r := c.geo.Panes[p.info.ID]
		if cols, rows := p.mirror.Size(); !p.watch && (int(cols) != r.W || int(rows) != r.H) {
			need = true
		}
	}
	if need {
		c.claimSeq, c.claimed = c.v.Seq, true
	}
	return need
}

// claim tells the server that the user typed into session id here.
func (c *client) claim(id string) {
	if _, err := c.vc.Do(proto.MethodViewInput, proto.ViewParams{Session: id}); err != nil {
		c.log.Printf("claim %s: %v", id, err)
	}
}

// act runs a view action; a failure shows in the status bar. The view
// it answers is drawn before act returns, so the next event already sees
// it here.
func (c *client) act(method string, p proto.ViewParams) {
	v, err := c.vc.Do(method, p)
	if err == nil {
		c.apply(v)
		return
	}
	if c.lock() {
		var perr *proto.Error
		msg := err.Error()
		if errors.As(err, &perr) {
			msg = perr.Message
		}
		c.flash = msg
		c.status()
		c.mu.Unlock()
		c.poke()
	}
}

func (c *client) signals(sigs <-chan os.Signal, fd int) {
	for sig := range sigs {
		switch sig {
		case syscall.SIGWINCH:
			// The user really resized the window: the view takes its size
			// and its panes follow, whoever typed last (docs/SPEC.md §3.3).
			cols, rows, err := term.GetSize(fd)
			if err != nil || cols <= 0 || rows <= 0 {
				continue
			}
			if !c.lock() {
				return
			}
			c.setWindow(cols, rows)
			c.relayout()
			c.mu.Unlock()
			c.act(proto.MethodViewSize, proto.ViewParams{Cols: uint16(cols), Rows: uint16(rows), Resize: true})
			c.poke()
		case syscall.SIGUSR1:
			// Consistency check: the server puts its digest into the
			// stream, readLoop compares it with the mirror's.
			if c.lock() {
				p := c.focus
				c.mu.Unlock()
				if p != nil {
					c.send(p, proto.FrameDigestReq, nil)
				}
			}
		default:
			c.finish(Result{Reason: "detached (" + sig.String() + ")", Detached: true, Quit: true})
		}
	}
}

func (c *client) inputLoop(ctx context.Context, in *os.File) {
	events := make(chan uv.Event, 64)
	go func() {
		err := uv.NewTerminalReader(in, os.Getenv("TERM")).StreamEvents(ctx, events)
		// EOF or EIO: the outer terminal is gone. That is a detach.
		c.finish(Result{Reason: "detached (terminal closed: " + errString(err) + ")", Detached: true, Quit: true})
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
		c.focusReport(true)
	case uv.BlurEvent:
		c.focusReport(false)
	case uv.PasteEvent:
		if !c.lock() {
			return
		}
		p := c.focus
		if p == nil || p.watch || c.sideFocus {
			c.mu.Unlock()
			return
		}
		b, err := emu.Paste(p.mirror, []byte(e.Content))
		claim := c.needClaim()
		c.mu.Unlock()
		if claim {
			c.claim(p.info.ID)
		}
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
		ps := c.leaves()
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
	if c.menu != nil {
		c.menuKey(k)
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
	if c.sideFocus && !pending && !c.prefix.match(k) {
		c.sideKeyboard(k)
		return
	}
	do := prefixStep(c.prefix, pending, k, c.dashboard)
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
	if do.input {
		if !c.sideFocus { // the sidebar has the keyboard: nothing leaks
			c.input(k)
		}
		return
	}
	c.run(do)
}

// run does what prefixStep decided, but for input.
func (c *client) run(do prefixDo) {
	switch {
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
	pane   string // a sidebar command (paneCommands)
	// takeover asks to take over the focused watch-only pane.
	takeover bool
	// remote asks to turn the focused coordinator's remote control on
	// or off.
	remote bool
}

// paneCommands are the keys that, after the prefix, act on the window
// itself: the sidebar's width ({ }), its slim strip (b) and its keyboard
// (tab).
var paneCommands = map[string]bool{"{": true, "}": true, "b": true, "tab": true}

// keyName names k as paneCommands does.
func keyName(k uv.Key) string {
	switch {
	case k.Code == uv.KeySpace || k.Text == " ":
		return "space"
	case k.Code == uv.KeyTab && k.Mod == 0:
		return "tab"
	case k.Text != "":
		return k.Text
	}
	return k.String()
}

// prefixStep decides what k does. After the prefix: d detaches, a
// dashboard key detaches and runs there (only when there is a dashboard
// to go back to), a pane command acts on the panes, u takes over a
// watch-only pane, the prefix again goes to the program, and anything
// else cancels.
func prefixStep(prefix chord, pending bool, k uv.Key, dashboard bool) prefixDo {
	name := keyName(k)
	switch {
	case !pending && prefix.match(k):
		return prefixDo{arm: true}
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

// detachThen leaves the layout; then is the dashboard key to run
// afterwards. A shared view goes back to its dashboard, on every console;
// a bare one has none, so this console leaves it.
func (c *client) detachThen(then string) { c.detachTo("", then) }

// detachTo is detachThen to project's dashboard ("" for the dashboard as
// it was): a sidebar menu's project popup, tasks or inbox.
func (c *client) detachTo(project, then string) {
	c.then = then
	c.detaching.Store(true) // publishes then to lost
	if !c.bare {
		method := proto.MethodViewDashboard
		if project != "" {
			method = proto.MethodViewProject
		}
		if _, err := c.vc.Do(method, proto.ViewParams{Project: project}); err != nil {
			c.log.Printf("%s: %v", method, err)
		}
	}
	var ps []*pane
	if c.lock() {
		ps = c.leaves()
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
	res.Then, res.GoTo = c.then, c.goTo
	return res
}

// paneCommand runs a command on the window: the sidebar's width, its
// slim strip, its keyboard.
func (c *client) paneCommand(cmd string) {
	switch cmd {
	case "{", "}", "b":
		c.sideKey(cmd)
	case "tab":
		c.sideFocusOn(true)
	}
	c.poke()
}

// askTakeover asks, in the status bar, whether to take over the focused
// pane when it is watch-only.
func (c *client) askTakeover() {
	if !c.lock() {
		return
	}
	switch {
	case c.focus == nil:
	case c.focus.watch:
		c.confirm = c.focus
	default:
		c.flash = "this pane takes your keys already"
	}
	c.status()
	c.mu.Unlock()
	c.poke()
}

// answerTakeover takes over p on yes: it takes keys from now on, in this
// console, for this attach, and its coordinator is told. c.mu held;
// released here.
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
		c.statusText, c.statusHits = line, questionHits(line)
		return
	}
	if p := c.confirm; p != nil {
		line := "\x1b[7m" + fit(" take over "+paneName(p.info)+" and type into it? Its coordinator is told. y yes · any other key no", c.paneCols) + "\x1b[27m"
		if c.single {
			c.focus.r.SetStatus(line)
		}
		c.statusText, c.statusHits = line, questionHits(line)
		return
	}
	where := ""
	switch {
	case c.sideFocus:
		where = sideHint
	case c.focus.watch:
		where = "watch-only, " + takeOverHint
	case c.focus.info.Role == proto.RoleThread:
		where = "taken over"
	}
	if c.flash != "" {
		where = strings.TrimPrefix(where+" · "+c.flash, " · ")
	}
	line, hits := statusBar(c.focus.info, c.focus.status, c.pending, c.paneCols, where)
	if c.single {
		c.focus.r.SetStatus(line)
	}
	c.statusText, c.statusHits = line, hits
}

// input sends a key to the focused program, unless its pane is
// watch-only.
func (c *client) input(k uv.Key) {
	if !c.lock() {
		return
	}
	p := c.focus
	if p == nil {
		c.mu.Unlock()
		return
	}
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
	claim := c.needClaim()
	c.mu.Unlock()
	if claim {
		c.claim(p.info.ID)
	}
	c.log.Printf("key %s -> %q %v", k, b, err)
	if err == nil && len(b) > 0 {
		c.send(p, proto.FrameInput, b)
	}
}

// mouse handles a mouse event (attachmouse.go): the open menu, a
// question in the status bar, the sidebar and the status bar are tm's.
// Over the pane, a click takes the keyboard back from the sidebar; the
// event goes to the program when it tracks the mouse and the pane isn't
// watch-only, and otherwise a right-click opens the ≡ menu. The outer terminal reports the mouse
// while the focused program tracks it or the sidebar shows (see
// outerModes).
func (c *client) mouse(ev uv.Event) {
	m, ok := toMouse(ev)
	if !ok || !c.lock() {
		return
	}
	press := m.Action == emu.MousePress && (m.Button == emu.MouseLeft || m.Button == emu.MouseRight || m.Button == emu.MouseMiddle)
	status := c.statusBar && m.Y >= c.paneRows && m.X >= c.sideW
	switch {
	case c.menu != nil:
		c.menuMouse(m)
		return
	case press && (c.confirm != nil || c.confirmRemote != nil):
		// A click on y yes says yes; any other click, no.
		yes := status && m.Button == emu.MouseLeft && hintAt(c.statusHits, m.X-c.sideW) == "y"
		if p := c.confirm; p != nil {
			c.answerTakeover(p, yes)
		} else {
			c.answerRemote(c.confirmRemote, yes)
		}
		return
	case c.side != nil && (m.X < c.sideW || c.side.drag):
		if press && m.Button == emu.MouseRight && !c.side.drag {
			c.sideMenu(m)
			return
		}
		c.sideMouse(m)
		return
	case status:
		c.statusMouse(m)
		return
	}
	var p *pane
	for _, q := range c.shown() {
		if r := q.rect; m.X >= r.X && m.X < r.X+r.W && m.Y >= r.Y && m.Y < r.Y+r.H {
			p = q
		}
	}
	if p == nil { // padding, or the empty row above the status bar
		c.mu.Unlock()
		return
	}
	_, click := ev.(uv.MouseClickEvent)
	if click && c.sideFocus {
		// A click on a pane takes the keyboard back from the sidebar.
		c.sideFocus = false
		c.status()
		c.poke()
	}
	if p.watch || !p.mirror.Modes().MouseTracking() {
		// tm's: the program doesn't take the mouse.
		if press && m.Button == emu.MouseRight {
			c.openMenu("", c.sessionItems(), m.X, m.Y, false)
		}
		_, wheel := ev.(uv.MouseWheelEvent)
		claim := !p.watch && (click || wheel) && c.needClaim()
		c.mu.Unlock()
		if claim {
			c.claim(p.info.ID)
		}
		c.poke()
		return
	}
	claim := false
	if _, wheel := ev.(uv.MouseWheelEvent); click || wheel {
		claim = c.needClaim()
	}
	m.X -= p.rect.X
	m.Y -= p.rect.Y
	m.Y += p.r.Top()
	cols, rows := p.mirror.Size()
	var b []byte
	var err error
	if m.X < int(cols) && m.Y < int(rows) {
		b, err = c.enc.Mouse(p.mirror, m)
	}
	c.mu.Unlock()
	if claim {
		c.claim(p.info.ID)
	}
	if err == nil && len(b) > 0 {
		c.send(p, proto.FrameInput, b)
	}
}

func (c *client) focusReport(gained bool) {
	if !c.lock() {
		return
	}
	p := c.focus
	if p == nil || p.watch {
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
	var m emu.Modes
	if c.focus != nil {
		m = c.focus.mirror.Modes()
	}
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
		vis := c.shown()
		held := false
		for _, p := range vis {
			if p.held && time.Since(p.heldAt) > maxHold {
				p.held = false // the program never ended its update; draw anyway
			}
			held = held || p.held
		}
		var b []byte
		var err error
		switch {
		case c.focus == nil:
		case c.single && len(vis) == 1:
			c.full = false // the renderer repaints after SetSize
			b, err = vis[0].r.Frame(vis[0].mirror, vis[0].held)
		default:
			b, err = c.frameSplit(vis)
		}
		if err == nil && c.menu != nil && (len(b) > 0 || !c.menu.drawn) {
			b = c.appendMenu(b)
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
				c.finish(Result{Reason: "detached (terminal closed: " + err.Error() + ")", Detached: true, Quit: true})
				continue
			}
		}
		if held {
			time.AfterFunc(maxHold, c.poke) // draw once the hold times out
		}
	}
}

// frameSplit draws the pane beside the sidebar or above the status bar:
// the sidebar, the pane's changes, the status bar, then the
// focused pane's cursor, all in one synchronised update; nil when nothing
// changed. c.mu held.
func (c *client) frameSplit(vis []*pane) ([]byte, error) {
	b := append(c.buf[:0], "\x1b[?2026h\x1b[?25l"...)
	wrote := false
	if c.full {
		b = append(b, "\x1b[0m\x1b[H\x1b[2J"...)
		for _, p := range vis {
			p.r.Invalidate()
		}
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
	var cur string // the focused pane cropped away: no cursor
	if r := c.focus.rect; r.W > 0 && r.H > 0 {
		cur = string(c.focus.r.Cursor())
	}
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

func errString(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "EOF"
	}
	return err.Error()
}

// threadStatuses reads the STATUS.md of every thread session, by
// session id, so the status bar shows the progress the dashboard shows
// (docs/SPEC.md §7.3).
func threadStatuses(sessions []proto.SessionInfo) map[string]*thread.Status {
	out := map[string]*thread.Status{}
	for _, s := range sessions {
		if s.Role != proto.RoleThread || s.Project == "" || s.Thread == "" {
			continue
		}
		if p, err := project.Open(s.Project); err == nil {
			if st, err := thread.ReadStatus(p, s.Thread); err == nil {
				out[s.ID] = st
			}
		}
	}
	return out
}

// statePoll is how often the status bar asks the server for state.
const statePoll = 500 * time.Millisecond

// pollState keeps the status bar and the sidebar current and rings the
// bell when the server sent a notification (a session blocked, a thread
// reported; docs/SPEC.md §4). It polls session.list; a lost server is
// noticed by the attach streams themselves.
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
		var projects []ProjectData
		if c.side != nil {
			projects = loadSideProjects()
		}
		statuses := threadStatuses(res.Sessions)
		if c.lock() {
			if c.side != nil {
				c.side.projects, c.side.sessions = projects, res.Sessions
			}
			for _, p := range c.panes {
				for _, s := range res.Sessions {
					if s.ID == p.info.ID {
						p.info, p.status = s, statuses[s.ID]
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
