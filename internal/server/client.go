package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/version"
)

// Client timings (docs/SPEC.md §3.1, §3.2).
const (
	// AutoStartTimeout is how long a client waits for a server it started.
	AutoStartTimeout = 5 * time.Second
	// UnresponsiveTimeout is how long a held lock may go without a socket
	// answering before the server counts as hung.
	UnresponsiveTimeout = 2 * time.Second
)

// ErrNotRunning means no server is running and the client did not start one.
var ErrNotRunning = errors.New("tm server is not running")

// UnresponsiveError means a server holds the lock but does not answer.
type UnresponsiveError struct {
	PID int
	Log string
}

func (e *UnresponsiveError) Error() string {
	return fmt.Sprintf("server unresponsive (pid %d); see %s or run tm server stop --force", e.PID, e.Log)
}

// Client is one connection to the server.
type Client struct {
	conn   net.Conn
	br     *bufio.Reader
	nextID int64
	// frameBuf is reused by ReadFrame.
	frameBuf []byte
	// Server is the server's hello.
	Server proto.Hello
}

// Dial connects to the socket and performs the handshake for kind.
func Dial(p Paths, kind proto.Kind) (*Client, error) {
	conn, err := net.DialTimeout("unix", p.Socket, UnresponsiveTimeout)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, br: bufio.NewReader(conn)}
	conn.SetDeadline(time.Now().Add(UnresponsiveTimeout))
	hello := proto.Hello{Protocol: proto.Protocol, Version: version.Version, Build: version.BuildID(), Kind: proto.Kind(kind)}
	if err := writeJSONLine(conn, hello); err != nil {
		conn.Close()
		return nil, err
	}
	var server proto.Hello
	if err := readJSONLine(c.br, &server); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	// A mismatch comes back as *proto.MismatchError; for attach its ReExec
	// says to re-exec Server.Bin (the attach client does that, M2).
	if err := proto.Check(hello, server); err != nil {
		conn.Close()
		return nil, err
	}
	c.Server = server
	return c, nil
}

// Connect opens a control connection, handling stale sockets and, with
// autostart, starting the server when none is running.
func Connect(p Paths, autostart bool) (*Client, error) {
	c, err := Dial(p, proto.KindControl)
	if err == nil {
		return c, nil
	}
	var verr *proto.MismatchError
	if errors.As(err, &verr) {
		return nil, err
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, &UnresponsiveError{PID: readPID(p.PID), Log: p.Log}
		}
		return nil, err
	}
	// Nothing answers. If we can take the lock, no server is running and
	// whatever is at the socket path is left over from a crash.
	if err := os.MkdirAll(p.RunDir, 0o700); err != nil {
		return nil, err
	}
	lk, lerr := tryLock(p.Lock)
	switch {
	case lerr == nil:
		os.Remove(p.Socket)
		os.Remove(p.PID)
		lk.unlock()
		if !autostart {
			return nil, ErrNotRunning
		}
		if err := StartDetached(p); err != nil {
			return nil, err
		}
		return Dial(p, proto.KindControl)
	case errors.Is(lerr, ErrLocked):
		// A server holds the lock: it may still be starting up.
		deadline := time.Now().Add(UnresponsiveTimeout)
		for time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			if c, err := Dial(p, proto.KindControl); err == nil {
				return c, nil
			} else if errors.As(err, &verr) {
				return nil, err
			}
		}
		return nil, &UnresponsiveError{PID: readPID(p.PID), Log: p.Log}
	default:
		return nil, lerr
	}
}

// StartDetached starts `tm server run --detached` as a new session with no
// controlling terminal and stdio on /dev/null, and waits until it answers.
func StartDetached(p Paths) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(bin, "server", "run", "--detached")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(AutoStartTimeout)
	for time.Now().Before(deadline) {
		if c, err := Dial(p, proto.KindControl); err == nil {
			c.Close()
			return nil
		} else {
			var verr *proto.MismatchError
			if errors.As(err, &verr) {
				return err
			}
		}
		select {
		case err := <-exited:
			// It may have lost a race with another client's server,
			// which is fine if that one answers.
			if c, derr := Dial(p, proto.KindControl); derr == nil {
				c.Close()
				return nil
			}
			return fmt.Errorf("server exited during start (%v); see %s", err, p.Log)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("server did not answer within %v; see %s", AutoStartTimeout, p.Log)
}

// Call sends one request and decodes its result into result (which may be
// nil). A server-side failure is returned as *proto.Error.
func (c *Client) Call(method string, params, result any) error {
	c.nextID++
	req := proto.Request{ID: c.nextID, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	if err := writeJSONLine(c.conn, req); err != nil {
		return err
	}
	var resp proto.Response
	if err := readJSONLine(c.br, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Attach turns a fresh attach connection into a frame stream: it sends the
// attach request and returns the server's reply. Frames follow on Conn and
// Reader.
func Attach(p Paths, params proto.AttachParams) (*Client, *proto.SessionInfo, error) {
	c, err := Dial(p, proto.KindAttach)
	if err != nil {
		return nil, nil, err
	}
	if err := writeJSONLine(c.conn, proto.AttachRequest{Attach: params}); err != nil {
		c.Close()
		return nil, nil, err
	}
	var reply proto.AttachReply
	if err := readJSONLine(c.br, &reply); err != nil {
		c.Close()
		return nil, nil, err
	}
	if reply.Error != nil {
		c.Close()
		return nil, nil, reply.Error
	}
	return c, reply.Attached, nil
}

// ReadFrame reads the next attach frame. The payload is only valid until
// the next call.
func (c *Client) ReadFrame() (proto.FrameType, []byte, error) {
	typ, p, err := proto.ReadFrame(c.br, c.frameBuf)
	c.frameBuf = p
	return typ, p, err
}

// WriteFrame sends one attach frame.
func (c *Client) WriteFrame(typ proto.FrameType, payload []byte) error {
	return proto.WriteFrame(c.conn, typ, payload)
}

// ForceKill sends SIGKILL to the recorded server pid, but only while the
// lock is held (so the pid is still the server's) and the pid is alive.
func ForceKill(p Paths) (int, error) {
	lk, err := tryLock(p.Lock)
	if err == nil {
		lk.unlock()
		os.Remove(p.Socket)
		os.Remove(p.PID)
		return 0, ErrNotRunning
	}
	if !errors.Is(err, ErrLocked) {
		return 0, err
	}
	pid := readPID(p.PID)
	if !alive(pid) {
		return pid, fmt.Errorf("lock is held but pid file names no live process (%d)", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return pid, err
	}
	return pid, nil
}

// WaitStopped waits until no server holds the lock.
func WaitStopped(p Paths, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if lk, err := tryLock(p.Lock); err == nil {
			lk.unlock()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
