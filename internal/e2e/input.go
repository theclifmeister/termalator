package e2e

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/theclifmeister/termalator/internal/emu"
)

// Keys a scenario presses with Window.Key. They are encoded the way the
// window's terminal would send them, honouring the keyboard modes the
// program in the window (tm attach) negotiated.
var (
	CtrlBackslash = emu.Key{Rune: '\\', Mods: emu.ModCtrl}
	ShiftEnter    = emu.Key{Special: emu.KeyEnter, Mods: emu.ModShift}
	Enter         = emu.Key{Special: emu.KeyEnter}
	ShiftPageUp   = emu.Key{Special: emu.KeyPageUp, Mods: emu.ModShift}
)

// Attach opens a window of cols×rows running `tm attach id` (the newest
// session when id is empty).
func (e *Env) Attach(cols, rows uint16, id string) *Window {
	e.T.Helper()
	if id == "" {
		return e.Window(cols, rows, "attach")
	}
	return e.Window(cols, rows, "attach", id)
}

// encoder returns the window's input encoder, or an error after
// CloseWindow; w.mu held.
func (w *Window) encoder() (*emu.Encoder, error) {
	if w.term == nil {
		return nil, fmt.Errorf("the window is closed")
	}
	if w.enc == nil {
		enc, err := emu.NewEncoder()
		if err != nil {
			return nil, err
		}
		w.enc = enc
	}
	return w.enc, nil
}

// send writes input bytes the window's terminal produced.
func (w *Window) send(what string, b []byte, err error) {
	w.env.T.Helper()
	if err != nil {
		w.env.T.Fatalf("window: encode %s: %v", what, err)
	}
	if len(b) == 0 {
		w.env.T.Fatalf("window: %s encodes to nothing (does the program in the window track it?)", what)
	}
	if _, err := w.ptmx.Write(b); err != nil {
		w.env.T.Fatalf("window: send %s: %v", what, err)
	}
}

// Key presses a key.
func (w *Window) Key(k emu.Key) {
	w.env.T.Helper()
	w.mu.Lock()
	enc, err := w.encoder()
	var b []byte
	if err == nil {
		b, err = enc.Key(w.term, k)
	}
	w.mu.Unlock()
	w.send(fmt.Sprintf("key %+v", k), b, err)
}

// Paste pastes text, bracketed if the program in the window asked for it.
func (w *Window) Paste(text string) {
	w.env.T.Helper()
	w.mu.Lock()
	_, err := w.encoder()
	var b []byte
	if err == nil {
		b, err = emu.Paste(w.term, []byte(text))
	}
	w.mu.Unlock()
	w.send("paste", b, err)
}

// Wheel turns the mouse wheel by one notch at cell (x, y). The window only
// reports it if the program in the window turned on mouse tracking.
func (w *Window) Wheel(up bool, x, y int) {
	w.env.T.Helper()
	btn := emu.MouseWheelDown
	if up {
		btn = emu.MouseWheelUp
	}
	w.mu.Lock()
	enc, err := w.encoder()
	var b []byte
	if err == nil {
		b, err = enc.Mouse(w.term, emu.Mouse{Action: emu.MousePress, Button: btn, X: x, Y: y})
	}
	w.mu.Unlock()
	w.send("wheel", b, err)
}

// Modes returns the window terminal's modes: what the program in the
// window turned on.
func (w *Window) Modes() emu.Modes {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.term == nil {
		return emu.Modes{}
	}
	return w.term.Modes()
}

// WaitExit waits until the window's command exits.
func (w *Window) WaitExit(timeout time.Duration) {
	w.env.T.Helper()
	select {
	case <-w.done:
	case <-time.After(timeout):
		w.env.T.Fatalf("window command still running after %v; screen:\n%s", timeout, w.Screen())
	}
}

// AttachLog returns the attach log lines of the client running in w.
func (w *Window) AttachLog() []string {
	b, _ := os.ReadFile(w.env.AttachLog)
	tag := fmt.Sprintf(" pid %d: ", w.PID())
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, tag) {
			out = append(out, l)
		}
	}
	return out
}

// AssertMirrorsServer checks that the attach client in w mirrors the
// server exactly: it asks the client (SIGUSR1) to request an in-stream
// digest, and the client compares it with its mirror's at the same point
// of the stream (modes, both screens, cursor, keyboard state).
func (e *Env) AssertMirrorsServer(w *Window) {
	e.T.Helper()
	count := func() (n int, last string) {
		for _, l := range w.AttachLog() {
			if strings.Contains(l, " digest ") {
				n, last = n+1, l
			}
		}
		return n, last
	}
	before, _ := count()
	if err := syscall.Kill(w.PID(), syscall.SIGUSR1); err != nil {
		e.T.Fatalf("signal attach client: %v", err)
	}
	var last string
	if !Poll(DefaultTimeout, func() bool { var n int; n, last = count(); return n > before }) {
		e.T.Fatalf("attach client logged no digest check; log:\n%s", strings.Join(w.AttachLog(), "\n"))
	}
	if !strings.Contains(last, " digest ok ") {
		e.T.Fatalf("client mirror differs from the server: %s", last)
	}
}
