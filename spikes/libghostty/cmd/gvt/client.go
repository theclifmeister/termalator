package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"go.mitchellh.com/libghostty"
	"golang.org/x/term"
)

// client mirrors the server's emulator locally: it decodes the attach
// snapshot into its own libghostty terminal, then feeds it the same byte
// stream the server sees. Rendering and key encoding read the mirror.
type client struct {
	conn net.Conn
	wmu  sync.Mutex // serialises writes to conn

	mu     sync.Mutex
	mirror *libghostty.Terminal
	r      *renderer
	held   bool // inside a mode-2026 render hold
	heldAt time.Time

	enc  *libghostty.KeyEncoder
	ev   *libghostty.KeyEvent
	menc *libghostty.MouseEncoder
	mev  *libghostty.MouseEvent

	outer map[int]bool // private modes currently enabled on the outer terminal

	wake         chan struct{}
	done         chan string
	detach       chan struct{}
	scrolled     bool
	detachMu     sync.Once
	detachReason string

	// stats
	pendingSince time.Time
	lastKey      time.Time
	st           stats
}

type stats struct {
	Frames         int
	FrameBytes     int
	OutputMsgs     int
	OutputBytes    int
	Holds          int
	HoldTimeouts   int
	DigestOK       int
	DigestMismatch int
	SnapshotBytes  int
	Resyncs        int
	SnapshotDecode string
	LatencyUs      []int `json:"-"` // pty-output-arrived -> frame written
	KeyEchoUs      []int `json:"-"` // key sent -> next output arrives
	RenderUs       []int `json:"-"` // time to build one frame
	Summary        map[string]string
}

func (c *client) send(typ byte, payload []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	writeMsg(c.conn, typ, payload)
}

func (c *client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func runAttach(sock string, statsPath string) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	r, err := newRenderer()
	if err != nil {
		return err
	}
	enc, err := libghostty.NewKeyEncoder()
	if err != nil {
		return err
	}
	ev, err := libghostty.NewKeyEvent()
	if err != nil {
		return err
	}
	menc, err := libghostty.NewMouseEncoder()
	if err != nil {
		return err
	}
	mev, err := libghostty.NewMouseEvent()
	if err != nil {
		return err
	}
	c := &client{conn: conn, r: r, enc: enc, ev: ev, menc: menc, mev: mev, outer: map[int]bool{},
		wake: make(chan struct{}, 1), done: make(chan string, 1), detach: make(chan struct{})}

	// First message must be the snapshot.
	typ, payload, err := readMsg(conn)
	if err != nil || typ != msgSnapshot {
		return fmt.Errorf("expected snapshot, got %d: %v", typ, err)
	}
	if err := c.loadSnapshot(payload); err != nil {
		return err
	}

	// Outer terminal setup.
	in, out := os.Stdin, os.Stdout
	fd := int(in.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	// alt screen, bracketed paste, kitty keyboard "disambiguate" (so
	// Shift+Enter / Ctrl+Enter are distinguishable from Enter).
	out.WriteString("\x1b[?1049h\x1b[?2004h\x1b[>1u")
	restore := func() {
		out.WriteString("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1006l\x1b[<u\x1b[?2004l\x1b[0m\x1b[2 q\x1b[?25h\x1b[?1049l")
		term.Restore(fd, old)
	}
	defer restore()

	cols, rows, _ := term.GetSize(fd)
	c.r.outCols, c.r.outRows = uint16(cols), uint16(rows)
	c.send(msgSetSize, sizePayload(uint16(cols), uint16(rows)))

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	// Terminal window closed (SIGHUP) or we were told to stop: detach. The
	// server lives in its own session and never sees these signals.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)
	go func() { <-hup; c.detachOnce("terminal hung up") }()
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1) // ask for a digest check
	go func() {
		for {
			select {
			case <-winch:
				cols, rows, err := term.GetSize(fd)
				if err == nil {
					c.mu.Lock()
					c.r.outCols, c.r.outRows, c.r.full = uint16(cols), uint16(rows), true
					c.mu.Unlock()
					c.send(msgSetSize, sizePayload(uint16(cols), uint16(rows)))
					c.poke()
				}
			case <-usr1:
				c.send(msgDigestReq, nil)
			}
		}
	}()

	go c.readLoop()
	c.poke() // paint the snapshot now, even if the pane is idle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.inputLoop(ctx, in)

	reason := c.renderLoop(out)
	restore()
	c.writeStats(statsPath)
	fmt.Fprintf(os.Stderr, "[gvt] %s\n", reason)
	return nil
}

// loadSnapshot replaces the mirror with a decoded snapshot (on attach, and
// when the server resyncs a client that fell too far behind).
// Called with c.mu held, or before any other goroutine runs.
func (c *client) loadSnapshot(payload []byte) error {
	t0 := time.Now()
	mirror, err := decodeSnapshot(payload)
	if err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	c.st.SnapshotBytes = len(payload)
	c.st.SnapshotDecode = time.Since(t0).String()
	if c.mirror != nil {
		c.mirror.Close()
		c.st.Resyncs++
	}
	c.mirror = mirror
	c.held = false
	c.r.full = true
	mirror.SetEffectRenderHold(func(t *libghostty.Terminal, held bool) {
		// Called inside VTWrite with c.mu held. On hold start the terminal
		// holds the last complete frame: capture it, then stop updating.
		if held {
			c.r.rs.Update(t)
			c.held, c.heldAt = true, time.Now()
			c.st.Holds++
		} else {
			c.held = false
		}
	})
	return nil
}

func (c *client) readLoop() {
	for {
		typ, payload, err := readMsg(c.conn)
		if err != nil {
			c.done <- "server connection closed: " + errString(err)
			return
		}
		switch typ {
		case msgSnapshot:
			c.mu.Lock()
			err := c.loadSnapshot(payload)
			c.mu.Unlock()
			if err != nil {
				c.done <- err.Error()
				return
			}
			c.poke()
		case msgOutput:
			c.mu.Lock()
			now := time.Now()
			if c.pendingSince.IsZero() {
				c.pendingSince = now
			}
			if !c.lastKey.IsZero() {
				c.st.KeyEchoUs = append(c.st.KeyEchoUs, int(now.Sub(c.lastKey).Microseconds()))
				c.lastKey = time.Time{}
			}
			c.st.OutputMsgs++
			c.st.OutputBytes += len(payload)
			c.mirror.VTWrite(payload)
			c.mu.Unlock()
			c.poke()
		case msgResize:
			cols, rows := parseSize(payload)
			c.mu.Lock()
			err := c.mirror.Resize(cols, rows, 8, 16)
			log.Printf("msgResize %dx%d err=%v", cols, rows, err)
			c.r.full = true
			c.mu.Unlock()
			c.poke()
		case msgDigest:
			c.mu.Lock()
			mine := digest(c.mirror)
			if mine == string(payload) {
				c.st.DigestOK++
				log.Printf("digest OK %s", mine)
			} else {
				c.st.DigestMismatch++
				log.Printf("digest MISMATCH server=%s client=%s", payload, mine)
				os.WriteFile(os.Getenv("GVT_DUMP_PREFIX")+"client-dump.txt", []byte(stateDump(c.mirror)), 0o644)
			}
			c.mu.Unlock()
		case msgExit:
			c.done <- "child exited: " + string(payload)
			return
		}
	}
}

func errString(err error) string {
	if err == nil {
		return "EOF"
	}
	if errors.Is(err, io.EOF) {
		return "EOF"
	}
	return err.Error()
}

func (c *client) inputLoop(ctx context.Context, in *os.File) {
	tr := uv.NewTerminalReader(in, os.Getenv("TERM"))
	events := make(chan uv.Event, 64)
	go func() {
		err := tr.StreamEvents(ctx, events)
		c.detachOnce("input closed: " + errString(err))
	}()
	for ev := range events {
		switch e := ev.(type) {
		case uv.KeyPressEvent:
			k := uv.Key(e)
			if k.Code == '\\' && k.Mod == uv.ModCtrl {
				c.detachOnce("detached")
				return
			}
			c.mu.Lock()
			// Shift+PgUp/PgDn scroll this client's viewport locally, but
			// only on the primary screen; full-screen apps get the key.
			scr, _ := c.mirror.ActiveScreen()
			if scr == libghostty.ScreenPrimary && k.Mod == uv.ModShift && (k.Code == uv.KeyPgUp || k.Code == uv.KeyPgDown) {
				rows, _ := c.mirror.Rows()
				d := int(rows) / 2
				if k.Code == uv.KeyPgUp {
					d = -d
				}
				c.mirror.ScrollViewportDelta(d)
				c.scrolled = true
				c.mu.Unlock()
				c.poke()
				continue
			}
			if c.scrolled {
				c.mirror.ScrollViewportBottom()
				c.scrolled = false
				c.poke()
			}
			fillKeyEvent(c.ev, k, libghostty.KeyActionPress)
			c.enc.SetOptFromTerminal(c.mirror)
			b, err := c.enc.Encode(c.ev)
			c.lastKey = time.Now()
			c.mu.Unlock()
			if err == nil && len(b) > 0 {
				log.Printf("key %q -> %q", k.String(), b)
				c.send(msgInput, b)
			}
		case uv.MouseClickEvent, uv.MouseReleaseEvent, uv.MouseMotionEvent, uv.MouseWheelEvent:
			c.handleMouse(ev)
		case uv.FocusEvent:
			c.handleFocus(true)
		case uv.BlurEvent:
			c.handleFocus(false)
		case uv.PasteEvent:
			c.mu.Lock()
			bracketed, _ := c.mirror.Mode(libghostty.ModeBracketedPaste)
			c.mu.Unlock()
			b, err := libghostty.PasteEncode([]byte(e.Content), bracketed)
			if err == nil {
				log.Printf("paste %d bytes bracketed=%v -> %q", len(e.Content), bracketed, b)
				c.send(msgInput, b)
			}
		}
	}
}

const maxHold = time.Second

func (c *client) renderLoop(out *os.File) string {
	tick := time.NewTicker(time.Second / 120) // frame cap ~120 Hz
	defer tick.Stop()
	for {
		select {
		case reason := <-c.done:
			return reason
		case <-c.detach:
			return c.detachReason
		case <-c.wake:
		}
		<-tick.C
		c.mu.Lock()
		if c.held && time.Since(c.heldAt) > maxHold {
			c.held = false // app never ended its sync update; draw anyway
			c.st.HoldTimeouts++
		}
		t0 := time.Now()
		b, err := c.r.frame(c.mirror, c.held)
		renderDur := time.Since(t0)
		pending := c.pendingSince
		if !c.held {
			c.pendingSince = time.Time{}
		}
		if err != nil {
			c.mu.Unlock()
			return "render error: " + err.Error()
		}
		if modes := c.outerModes(); len(modes) > 0 {
			out.Write(modes)
		}
		if len(b) > 0 {
			if _, err := out.Write(b); err != nil {
				c.mu.Unlock()
				return "terminal write failed: " + err.Error()
			}
			c.st.Frames++
			c.st.FrameBytes += len(b)
			c.st.RenderUs = append(c.st.RenderUs, int(renderDur.Microseconds()))
			if !pending.IsZero() && !c.held {
				c.st.LatencyUs = append(c.st.LatencyUs, int(time.Since(pending).Microseconds()))
			}
		}
		c.mu.Unlock()
		if c.held {
			// re-check when the hold might time out
			go func() { time.Sleep(maxHold); c.poke() }()
		}
	}
}

func pct(xs []int) string {
	if len(xs) == 0 {
		return "n/a"
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	p := func(q float64) int { return s[int(q*float64(len(s)-1))] }
	return fmt.Sprintf("n=%d p50=%dus p95=%dus p99=%dus max=%dus", len(s), p(.5), p(.95), p(.99), s[len(s)-1])
}

func (c *client) writeStats(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	c.st.Summary = map[string]string{
		"output_to_frame": pct(c.st.LatencyUs),
		"key_to_echo":     pct(c.st.KeyEchoUs),
		"render_time":     pct(c.st.RenderUs),
		"client_cpu":      fmt.Sprintf("user=%v sys=%v", time.Duration(ru.Utime.Nano()), time.Duration(ru.Stime.Nano())),
		"client_maxrss":   fmt.Sprintf("%d MB", maxRSSBytes(ru.Maxrss)>>20),
	}
	if path == "" {
		return
	}
	b, _ := json.MarshalIndent(c.st, "", "  ")
	os.WriteFile(path, b, 0o644)
}

func (c *client) detachOnce(reason string) {
	c.detachMu.Do(func() {
		c.detachReason = reason
		log.Printf("detach: %s", reason)
		close(c.detach)
	})
}
