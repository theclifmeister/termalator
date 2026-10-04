package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"go.mitchellh.com/libghostty"
)

// server owns the PTY, the child process and the authoritative emulator.
// It runs in its own session (setsid) with no controlling terminal, so
// closing any client's terminal cannot signal it or the child.
type server struct {
	mu      sync.Mutex
	term    *libghostty.Terminal
	ptmx    *os.File
	cmd     *exec.Cmd
	clients map[*serverConn]struct{}
	cols    uint16
	rows    uint16
	sock    string
	bytesIn uint64
	raw     *os.File // optional raw PTY output log (GVT_RAWLOG)
}

type serverConn struct {
	conn net.Conn

	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte // framed messages not yet written
	lastOut int    // offset of a trailing msgOutput frame in pending, or -1
	closed  bool
	resyncs int
}

// maxPending bounds what a slow client may lag behind. Past it, the
// backlog is thrown away and replaced by a fresh snapshot: the client
// resyncs instead of being disconnected, and the PTY never blocks.
const maxPending = 4 << 20

func newServerConn(conn net.Conn) *serverConn {
	sc := &serverConn{conn: conn, lastOut: -1}
	sc.cond = sync.NewCond(&sc.mu)
	return sc
}

// enqueue must be called with server.mu held (so snapshots line up with
// the stream). Adjacent output frames are merged into one.
func (sc *serverConn) enqueue(s *server, typ byte, payload []byte) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.closed {
		return
	}
	if len(sc.pending)+len(payload) > maxPending {
		snap, err := s.term.Snapshot()
		if err == nil {
			sc.resyncs++
			log.Printf("client lagging by %d bytes: resync with %d-byte snapshot", len(sc.pending), len(snap))
			sc.pending = append(sc.pending[:0], frame(msgSnapshot, snap)...)
			sc.lastOut = -1
			sc.cond.Signal()
			return
		}
	}
	if typ == msgOutput && sc.lastOut >= 0 {
		sc.pending = append(sc.pending, payload...)
		n := len(sc.pending) - sc.lastOut - 5
		h := sc.pending[sc.lastOut+1:]
		h[0], h[1], h[2], h[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	} else {
		if typ == msgOutput {
			sc.lastOut = len(sc.pending)
		} else {
			sc.lastOut = -1
		}
		sc.pending = append(sc.pending, frame(typ, payload)...)
	}
	sc.cond.Signal()
}

func (sc *serverConn) writeLoop() {
	var buf []byte
	for {
		sc.mu.Lock()
		for len(sc.pending) == 0 && !sc.closed {
			sc.cond.Wait()
		}
		if len(sc.pending) == 0 && sc.closed {
			sc.mu.Unlock()
			break
		}
		buf, sc.pending = sc.pending, buf[:0]
		sc.lastOut = -1
		sc.mu.Unlock()
		if _, err := sc.conn.Write(buf); err != nil {
			break
		}
	}
	sc.conn.Close()
}

func (sc *serverConn) close() {
	sc.mu.Lock()
	sc.closed = true
	sc.cond.Signal()
	sc.mu.Unlock()
}

// frame builds a framed message so the per-client writer just copies bytes.
func frame(typ byte, payload []byte) []byte {
	b := make([]byte, 0, 5+len(payload))
	b = append(b, typ, byte(len(payload)>>24), byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)))
	return append(b, payload...)
}

func runServer(sock string, cols, rows uint16, argv []string) error {
	s := &server{clients: map[*serverConn]struct{}{}, cols: cols, rows: rows, sock: sock}
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollbackLines(10000),
		// Snapshots then also carry half-received escape sequences.
		libghostty.WithContinuationMaxBytes(4096),
		// Only the server's emulator answers queries (DA, DSR, kitty ?u,
		// XTVERSION...). Client mirrors have no write-pty effect, so a
		// query is answered exactly once however many clients attach.
		libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) {
			if s.ptmx != nil {
				s.ptmx.Write(data)
			}
		}),
		libghostty.WithXtversion(func(*libghostty.Terminal) string { return "termalator-spike" }),
	)
	if err != nil {
		return err
	}
	s.term = term

	// Bind first: if the socket cannot be created we must not leave an
	// unreachable agent running. (macOS caps sun_path at 104 bytes.)
	if len(sock) >= 104 {
		return fmt.Errorf("socket path is %d bytes; macOS allows 103: %s", len(sock), sock)
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer func() { ln.Close(); os.Remove(sock); os.Remove(sock + ".pid") }()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(childEnv(), "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=termalator")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	s.ptmx, s.cmd = ptmx, cmd
	os.WriteFile(sock+".pid", fmt.Appendf(nil, "%d %d\n", os.Getpid(), cmd.Process.Pid), 0o644)
	log.Printf("server pid=%d child pid=%d argv=%q size=%dx%d", os.Getpid(), cmd.Process.Pid, argv, cols, rows)

	go s.acceptLoop(ln)

	s.readLoop()
	status := "exited"
	if err := cmd.Wait(); err != nil {
		status = err.Error()
	}
	log.Printf("child %s; bytes in=%d", status, s.bytesIn)
	s.mu.Lock()
	for c := range s.clients {
		c.enqueue(s, msgExit, []byte(status))
		c.close()
	}
	s.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (s *server) readLoop() {
	buf := make([]byte, 64<<10)
	if p := os.Getenv("GVT_RAWLOG"); p != "" {
		s.raw, _ = os.Create(p)
		defer s.raw.Close()
	}
	raw := s.raw
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.bytesIn += uint64(n)
			s.term.VTWrite(data)
			if raw != nil {
				raw.Write(data)
			}
			s.broadcast(msgOutput, data)
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// broadcast must be called with s.mu held.
func (s *server) broadcast(typ byte, payload []byte) {
	for c := range s.clients {
		c.enqueue(s, typ, payload)
	}
}

func (s *server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *server) serve(conn net.Conn) {
	sc := newServerConn(conn)
	// Snapshot and registration happen under one lock, so the client sees
	// the snapshot followed by exactly the bytes that came after it.
	s.mu.Lock()
	t0 := time.Now()
	snap, err := s.term.Snapshot()
	if err != nil {
		s.mu.Unlock()
		log.Printf("snapshot: %v", err)
		conn.Close()
		return
	}
	log.Printf("attach: snapshot %d bytes in %v", len(snap), time.Since(t0))
	sc.enqueue(s, msgSnapshot, snap)
	s.clients[sc] = struct{}{}
	s.mu.Unlock()
	go sc.writeLoop()

	for {
		typ, payload, err := readMsg(conn)
		if err != nil {
			break
		}
		switch typ {
		case msgInput:
			s.ptmx.Write(payload)
		case msgSetSize:
			cols, rows := parseSize(payload)
			if cols == 0 || rows == 0 {
				continue
			}
			s.mu.Lock()
			if cols != s.cols || rows != s.rows {
				s.cols, s.rows = cols, rows
				s.term.Resize(cols, rows, 8, 16)
				if s.raw != nil {
					fmt.Fprintf(s.raw, "\n<<<GVT RESIZE %dx%d>>>\n", cols, rows)
				}
				pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
				s.broadcast(msgResize, sizePayload(cols, rows))
			}
			s.mu.Unlock()
		case msgDigestReq:
			s.mu.Lock()
			s.broadcast(msgDigest, []byte(digest(s.term)))
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	delete(s.clients, sc)
	s.mu.Unlock()
	sc.close()
	log.Printf("client detached (resyncs: %d)", sc.resyncs)
}

// stateDump is the full VT re-encoding of the terminal (all screens,
// scrollback, styles, modes, cursor, keyboard flags...). Two emulators with
// equal dumps are, for every practical purpose, in the same state.
func stateDump(t *libghostty.Terminal) string {
	f, err := libghostty.NewFormatter(t,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterExtraModes(true),
		libghostty.WithFormatterExtraCursor(true),
		libghostty.WithFormatterExtraStyle(true),
		libghostty.WithFormatterExtraScrollingRegion(true),
		libghostty.WithFormatterExtraKeyboard(true),
		libghostty.WithFormatterExtraKittyKeyboard(true),
		libghostty.WithFormatterExtraTabstops(true),
		libghostty.WithFormatterExtraCharsets(true),
	)
	if err != nil {
		return "ERR " + err.Error()
	}
	defer f.Close()
	s, _ := f.FormatString()
	return s
}

func digest(t *libghostty.Terminal) string {
	h := sha256.Sum256([]byte(stateDump(t)))
	return hex.EncodeToString(h[:8])
}

// spawnServer re-executes this binary as a detached server: new session,
// no controlling tty, stdio on /dev/null, log to <sock>.log.
func spawnServer(sock string, cols, rows uint16, argv []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(sock+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	args := append([]string{"server", "-s", sock, "-cols", fmt.Sprint(cols), "-rows", fmt.Sprint(rows), "--"}, argv...)
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("server did not come up; see %s.log", sock)
}

// childEnv drops per-session variables a parent Claude Code exports
// (herdr does the same). Inheriting them makes the child think it is a
// sub-session of whatever launched the server (e.g. transcripts disabled).
func childEnv() []string {
	drop := map[string]bool{"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true,
		"CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_MESSAGING_SOCKET": true, "CLAUDE_CODE_MESSAGING_TOKEN": true,
		"CLAUDE_CODE_EXECPATH": true, "CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_CHILD_SESSION": true,
		"CLAUDE_CODE_SESSION_ATTENDED": true}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	return env
}
