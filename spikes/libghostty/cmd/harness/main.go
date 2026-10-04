// Command harness drives gvt end to end. It plays the role of the user's
// terminal window: it runs `gvt new|attach` inside a PTY and feeds the
// output into its own libghostty-vt terminal (the "outer screen"), encodes
// keys with libghostty's key encoder using the modes gvt set on that outer
// terminal (so Shift+Enter arrives exactly as Ghostty would send it), and
// can close the window (close the PTY master -> SIGHUP to the client).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"go.mitchellh.com/libghostty"
)

var (
	gvt     = flag.String("gvt", "./bin/gvt", "gvt binary")
	outDir  = flag.String("out", "results", "results directory")
	workDir = flag.String("cwd", "", "working directory for the agent")
	model   = flag.String("model", "haiku", "claude model")
	same    = flag.Bool("samesize", false, "reattach with the same window size (no resize)")
)

type window struct {
	name string
	ptmx *os.File
	cmd  *exec.Cmd
	mu   sync.Mutex
	term *libghostty.Terminal
	enc  *libghostty.KeyEncoder
	ev   *libghostty.KeyEvent
	last time.Time // last output
	n    int
	exit chan struct{}
	raw  []byte
}

func openWindow(name string, cols, rows uint16, args ...string) *window {
	w := &window{name: name, exit: make(chan struct{})}
	t, err := libghostty.NewTerminal(libghostty.WithSize(cols, rows), libghostty.WithMaxScrollbackLines(1000),
		libghostty.WithWritePty(func(_ *libghostty.Terminal, b []byte) { w.ptmx.Write(b) }))
	must(err)
	w.term = t
	w.enc, err = libghostty.NewKeyEncoder()
	must(err)
	w.ev, err = libghostty.NewKeyEvent()
	must(err)
	cmd := exec.Command(*gvt, args...)
	cmd.Dir = *workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-ghostty")
	ef, _ := os.Create(filepath.Join(*outDir, "client-"+name+".stderr"))
	cmd.Stderr = ef
	orig, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	must(err)
	// creack/pty calls Fd() on the master, which puts it in blocking mode;
	// Go then defers Close until a pending Read returns, so "closing the
	// window" would never hang up the client. Dup it into a fresh
	// non-blocking, poller-owned file and drop the original.
	nfd, err := syscall.Dup(int(orig.Fd()))
	must(err)
	must(syscall.SetNonblock(nfd, true))
	orig.Close()
	w.ptmx = os.NewFile(uintptr(nfd), "ptmx")
	w.cmd = cmd
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := w.ptmx.Read(buf)
			if n > 0 {
				w.mu.Lock()
				w.term.VTWrite(buf[:n])
				w.raw = append(w.raw, buf[:n]...)
				w.last = time.Now()
				w.n += n
				w.mu.Unlock()
			}
			if err != nil {
				break
			}
		}
	}()
	go func() { cmd.Wait(); close(w.exit) }()
	return w
}

func (w *window) screen() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return trimLines(plain(w.term))
}

// plain returns exactly the viewport rows via the render state.
func plain(t *libghostty.Terminal) string {
	rs, err := libghostty.NewRenderState()
	must(err)
	defer rs.Close()
	must(rs.Update(t))
	ri, _ := libghostty.NewRenderStateRowIterator()
	defer ri.Close()
	rc, _ := libghostty.NewRenderStateRowCells()
	defer rc.Close()
	must(rs.RowIterator(ri))
	var lines []string
	for ri.Next() {
		b, _ := ri.AppendText(nil, rc)
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func (w *window) waitFor(sub string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if strings.Contains(w.screen(), sub) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// quiet waits until the window got no output for d (or timeout).
func (w *window) quiet(d, timeout time.Duration) bool {
	start := time.Now()
	end := start.Add(timeout)
	for time.Now().Before(end) {
		w.mu.Lock()
		l := w.last
		w.mu.Unlock()
		if l.Before(start) {
			l = start
		}
		if time.Since(l) > d {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (w *window) typeText(s string) {
	for _, r := range s {
		w.ptmx.Write([]byte(string(r)))
		time.Sleep(15 * time.Millisecond)
	}
}

// key sends a key the way a real libghostty-based terminal would, honouring
// the keyboard modes the client enabled on this window.
func (w *window) key(k libghostty.Key, mods libghostty.Mods, text string, unshifted rune) []byte {
	w.mu.Lock()
	w.ev.SetAction(libghostty.KeyActionPress)
	w.ev.SetKey(k)
	w.ev.SetMods(mods)
	w.ev.SetConsumedMods(0)
	w.ev.SetUTF8(text)
	w.ev.SetUnshiftedCodepoint(unshifted)
	w.enc.SetOptFromTerminal(w.term)
	b, err := w.enc.Encode(w.ev)
	w.mu.Unlock()
	must(err)
	w.ptmx.Write(b)
	return b
}

// wheel sends mouse wheel notches at cell (x, y), encoded with the
// tracking modes the client enabled on this outer terminal.
func (w *window) wheel(up bool, x, y, n int) []byte {
	w.mu.Lock()
	enc, _ := libghostty.NewMouseEncoder()
	ev, _ := libghostty.NewMouseEvent()
	defer enc.Close()
	defer ev.Close()
	enc.SetOptFromTerminal(w.term)
	cols, _ := w.term.Cols()
	rows, _ := w.term.Rows()
	enc.SetOptSize(libghostty.MouseEncoderSize{ScreenWidth: uint32(cols) * 8, ScreenHeight: uint32(rows) * 16, CellWidth: 8, CellHeight: 16})
	ev.SetAction(libghostty.MouseActionPress)
	if up {
		ev.SetButton(libghostty.MouseButtonFour)
	} else {
		ev.SetButton(libghostty.MouseButtonFive)
	}
	ev.SetPosition(libghostty.MousePosition{X: float32(x*8 + 4), Y: float32(y*16 + 8)})
	b, err := enc.Encode(ev)
	w.mu.Unlock()
	must(err)
	for i := 0; i < n; i++ {
		w.ptmx.Write(b)
		time.Sleep(20 * time.Millisecond)
	}
	return b
}

func (w *window) paste(s string) []byte {
	w.mu.Lock()
	br, _ := w.term.Mode(libghostty.ModeBracketedPaste)
	w.mu.Unlock()
	b, err := libghostty.PasteEncode([]byte(s), br)
	must(err)
	w.ptmx.Write(b)
	return b
}

func (w *window) resize(cols, rows uint16) {
	w.mu.Lock()
	w.term.Resize(cols, rows, 8, 16)
	w.mu.Unlock()
	pty.Setsize(w.ptmx, &pty.Winsize{Cols: cols, Rows: rows}) // kernel sends SIGWINCH
}

// closeWindow simulates closing the terminal window: the PTY master goes
// away, the client gets SIGHUP/EIO and dies.
func (w *window) closeWindow() error {
	t0 := time.Now()
	before := ptmxFDs()
	w.ptmx.Close()
	time.Sleep(50 * time.Millisecond)
	if runtime.GOOS == "linux" {
		note("ptmx fds in harness before/after close: %d/%d", before, ptmxFDs())
	}
	defer func() { note("client exited %v after window close", time.Since(t0).Round(time.Millisecond)) }()
	select {
	case <-w.exit:
		note("client exit after window close: %v", w.cmd.ProcessState)
		return nil
	case <-time.After(5 * time.Second):
		w.cmd.Process.Signal(syscall.SIGQUIT) // goroutine dump into client-<name>.stderr
		<-w.exit
		return fmt.Errorf("client did not exit after window close (goroutine dump in client-%s.stderr)", w.name)
	}
}

func (w *window) save(name string) string {
	s := w.screen()
	os.WriteFile(filepath.Join(*outDir, name+".txt"), []byte(s+"\n"), 0o644)
	w.mu.Lock()
	os.WriteFile(filepath.Join(*outDir, name+".raw"), w.raw, 0o644)
	w.mu.Unlock()
	return s
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func dump(sock string, extra ...string) string {
	out, err := exec.Command(*gvt, append([]string{"dump", "-s", sock}, extra...)...).Output()
	if err != nil {
		return "DUMP ERROR " + err.Error()
	}
	return string(out)
}

func pids(sock string) (server, child int) {
	b, err := os.ReadFile(sock + ".pid")
	if err != nil {
		return 0, 0
	}
	fmt.Sscan(string(b), &server, &child)
	return
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// cpuTime returns the accumulated CPU time of a pid via ps.
func cpuTime(pid int) time.Duration {
	if runtime.GOOS == "linux" {
		// No ps in minimal containers: utime+stime from /proc (USER_HZ=100).
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return 0
		}
		f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
		ut, _ := strconv.Atoi(f[11])
		st, _ := strconv.Atoi(f[12])
		return time.Duration(ut+st) * 10 * time.Millisecond
	}
	out, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out)) // [[dd-]hh:]mm:ss.cc
	parts := strings.Split(s, ":")
	var d float64
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		d = d*60 + v
	}
	return time.Duration(d * float64(time.Second))
}

func descendants(pid int) []int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var res []int
	for _, f := range strings.Fields(string(out)) {
		p, _ := strconv.Atoi(f)
		res = append(res, p)
		res = append(res, descendants(p)...)
	}
	return res
}

func treeCPU(pid int) time.Duration {
	d := cpuTime(pid)
	for _, p := range descendants(pid) {
		d += cpuTime(p)
	}
	return d
}

// compare checks that the outer window shows what the server's emulator has.
func compare(label string, w *window, sock string) bool {
	outer := w.save(label + "-outer")
	server := trimLines(dump(sock))
	os.WriteFile(filepath.Join(*outDir, label+"-server.txt"), []byte(server+"\n"), 0o644)
	ok := outer == server
	res(label+": outer screen == server screen", ok, "")
	if !ok {
		ol, sl := strings.Split(outer, "\n"), strings.Split(server, "\n")
		for i := 0; i < len(ol) || i < len(sl); i++ {
			var a, b string
			if i < len(ol) {
				a = ol[i]
			}
			if i < len(sl) {
				b = sl[i]
			}
			if a != b {
				fmt.Printf("    first diff row %d:\n      outer:  %q\n      server: %q\n", i, a, b)
				break
			}
		}
	}
	return ok
}

var results bytes.Buffer

func res(name string, ok bool, detail string) {
	mark := "PASS"
	if !ok {
		mark = "FAIL"
	}
	line := fmt.Sprintf("[%s] %s", mark, name)
	if detail != "" {
		line += " — " + detail
	}
	fmt.Println(line)
	results.WriteString(line + "\n")
}

func note(format string, a ...any) {
	line := "  " + fmt.Sprintf(format, a...)
	fmt.Println(line)
	results.WriteString(line + "\n")
}

func main() {
	flag.Parse()
	scenario := flag.Arg(0)
	must(os.MkdirAll(*outDir, 0o755))
	*outDir, _ = filepath.Abs(*outDir)
	abs, _ := filepath.Abs(*gvt)
	*gvt = abs
	if *workDir == "" {
		*workDir, _ = os.Getwd()
	}
	switch scenario {
	case "shell":
		shellScenario()
	case "bench":
		benchScenario()
	case "claude":
		claudeScenario(false)
	case "claude-inline":
		claudeScenario(true)
	default:
		log.Fatalf("usage: harness shell|claude")
	}
	os.WriteFile(filepath.Join(*outDir, scenario+"-results.txt"), results.Bytes(), 0o644)
}

func ptmxFDs() int {
	ents, _ := os.ReadDir("/proc/self/fd")
	n := 0
	for _, e := range ents {
		if l, _ := os.Readlink("/proc/self/fd/" + e.Name()); strings.Contains(l, "ptmx") {
			n++
		}
	}
	return n
}
