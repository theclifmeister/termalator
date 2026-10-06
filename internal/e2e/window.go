package e2e

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/pty"
	"github.com/theclifmeister/terminatr/internal/view"
)

// Window is a virtual terminal window: a PTY running a command (`tm …`, or
// a shell the test types into) whose output its own libghostty-vt emulator
// parses, the "outer screen" a user would look at. The emulator answers
// terminal queries the way a real terminal would.
type Window struct {
	env  *Env
	cmd  *exec.Cmd
	ptmx *os.File
	done chan struct{}

	mu     sync.Mutex
	cols   int
	term   *emu.Terminal
	enc    *emu.Encoder // input encoder, made on first use
	raw    []byte
	last   time.Time // when output last arrived
	screen string    // last screen, kept after CloseWindow
	// Inside the program's mode-2026 synchronized update a terminal keeps
	// showing the last complete frame (heldScreen, from heldAt), as tm's
	// own attach does (internal/tui/attach.go): Screen never shows a torn
	// one.
	held       bool
	heldAt     time.Time
	heldScreen string
}

// maxHold is how long Screen shows the held frame of a program that never
// ends its synchronized update, as tm's attach does.
const maxHold = time.Second

// Window opens a window of the given size running `tm args…`.
func (e *Env) Window(cols, rows uint16, args ...string) *Window {
	e.T.Helper()
	return e.WindowCmd(cols, rows, append([]string{e.Bin}, args...)...)
}

// Shell opens a window running /bin/sh, with $TM naming the tm under test.
func (e *Env) Shell(cols, rows uint16) *Window {
	e.T.Helper()
	return e.WindowCmd(cols, rows, "/bin/sh")
}

// WindowCmd opens a window running argv. Like under a real terminal
// emulator, the command leads a new session with the PTY as its
// controlling terminal.
func (e *Env) WindowCmd(cols, rows uint16, argv ...string) *Window {
	e.T.Helper()
	w := &Window{env: e, done: make(chan struct{}), last: time.Now(), cols: int(cols)}
	term, err := emu.NewWith(emu.Options{
		Cols: cols, Rows: rows, Scrollback: 1000,
		WritePty:  func(b []byte) { w.ptmx.Write(b) },
		Xtversion: "terminatr-e2e",
		// Like a terminal on a dark desktop, it answers CSI ? 996 n.
		ColorScheme: func() (emu.Scheme, bool) { return emu.SchemeDark, true },
	})
	if err != nil {
		e.T.Fatal(err)
	}
	w.term = term
	// Runs inside term.Write, w.mu held: at hold start the terminal still
	// shows the last complete frame.
	term.OnRenderHold(func(held bool) {
		if held && !w.held {
			w.heldScreen, w.heldAt = w.liveScreen(), time.Now()
		}
		w.held = held
	})
	env := append(append([]string(nil), e.Vars...), "TERM=xterm-256color")
	// internal/pty gives a non-blocking master, so closing it really hangs
	// up the window.
	cmd, ptmx, err := pty.Start(argv, "/", env, cols, rows)
	if err != nil {
		e.T.Fatal(err)
	}
	w.ptmx, w.cmd = ptmx, cmd
	e.track(cmd.Process.Pid, "window "+strings.Join(argv, " "))
	e.mu.Lock()
	e.windows = append(e.windows, w)
	e.mu.Unlock()
	go w.readLoop()
	go func() { cmd.Wait(); close(w.done) }()
	return w
}

func (w *Window) readLoop() {
	buf := make([]byte, 32<<10)
	for {
		n, err := w.ptmx.Read(buf)
		if n > 0 {
			w.mu.Lock()
			if w.term != nil {
				w.term.Write(buf[:n])
			}
			w.raw = append(w.raw, buf[:n]...)
			w.last = time.Now()
			w.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// PID is the pid of the command running in the window.
func (w *Window) PID() int { return w.cmd.Process.Pid }

// Type sends raw bytes as if typed ("\r" is Enter).
func (w *Window) Type(s string) {
	w.env.T.Helper()
	if _, err := w.ptmx.Write([]byte(s)); err != nil {
		w.env.T.Fatalf("type into window: %v", err)
	}
}

// Resize resizes the window; the kernel sends SIGWINCH to its command.
func (w *Window) Resize(cols, rows uint16) {
	w.env.T.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.term == nil {
		return
	}
	w.term.Resize(cols, rows)
	w.cols = int(cols)
	if err := pty.Resize(w.ptmx, cols, rows); err != nil {
		w.env.T.Fatal(err)
	}
}

// Screen returns what the window shows (after CloseWindow: what it showed
// last).
func (w *Window) Screen() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.term == nil {
		return w.screen
	}
	if w.held && time.Since(w.heldAt) < maxHold {
		return w.heldScreen
	}
	return w.liveScreen()
}

// liveScreen is what the emulator holds now, w.mu held.
func (w *Window) liveScreen() string {
	s, err := w.term.Screen()
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return TrimScreen(s)
}

// SideCols is the projects sidebar's width in a tm window cols wide, at
// the default layout: every tm window shows it (docs/SPEC.md §4), the
// slim strip below 84 columns.
func SideCols(cols int) int { return view.Sidebar{}.Cols(cols) }

// statusRows are the status bar and the empty row above it, under every
// attached pane.
const statusRows = 2

// PaneScreen is what the window shows right of the sidebar (at the
// default layout) and above the empty row and the status bar: a lone
// pane's screen.
func (w *Window) PaneScreen() string {
	w.mu.Lock()
	n := SideCols(w.cols)
	w.mu.Unlock()
	lines := strings.Split(w.Screen(), "\n")
	lines = lines[:max(len(lines)-statusRows, 0)]
	for i, l := range lines {
		r := []rune(l)
		lines[i] = string(r[min(n, len(r)):])
	}
	return TrimScreen(strings.Join(lines, "\n"))
}

// Raw returns every byte the window has received.
func (w *Window) Raw() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.raw...)
}

// WaitFor waits until the window's screen contains text and returns it.
func (w *Window) WaitFor(text string, timeout time.Duration) string {
	w.env.T.Helper()
	return w.WaitUntil("screen containing "+text, timeout, func(s string) bool { return strings.Contains(s, text) })
}

// WaitUntil waits until cond holds for the window's screen.
func (w *Window) WaitUntil(desc string, timeout time.Duration, cond func(screen string) bool) string {
	w.env.T.Helper()
	var last string
	if !Poll(timeout, func() bool { last = w.Screen(); return cond(last) }) {
		w.env.T.Fatalf("window: no %s after %v; screen:\n%s", desc, timeout, last)
	}
	return last
}

// Quiet waits until the window has received no output for d (at most
// DefaultTimeout), for screens that must have settled before a compare.
func (w *Window) Quiet(d time.Duration) {
	w.env.T.Helper()
	ok := Poll(DefaultTimeout, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return time.Since(w.last) >= d
	})
	if !ok {
		w.env.T.Fatalf("window never went quiet for %v", d)
	}
}

// KillClient SIGKILLs the window's whole process group: the client dies
// without any chance to clean up.
func (w *Window) KillClient() {
	syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
	}
}

// CloseWindow closes the window: the PTY master goes away and the kernel
// hangs up everything still attached to it.
func (w *Window) CloseWindow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.term == nil {
		return
	}
	if s, err := w.term.Screen(); err == nil {
		w.screen = TrimScreen(s)
	}
	w.ptmx.Close()
	w.term.Close()
	w.term = nil
	if w.enc != nil {
		w.enc.Close()
		w.enc = nil
	}
}

// Exited is closed when the window's command has exited.
func (w *Window) Exited() <-chan struct{} { return w.done }
