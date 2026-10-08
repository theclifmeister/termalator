package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/ipc"
	"github.com/theclifmeister/terminatr/internal/plat/proc"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/service"
	"github.com/theclifmeister/terminatr/internal/version"
	"github.com/theclifmeister/terminatr/internal/view"
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
	// Started is set by Connect when it started the server.
	Started bool
}

// Dial connects to the socket and performs the handshake for kind.
func Dial(p Paths, kind proto.Kind) (*Client, error) { return dial(p, kind, proto.Protocol) }

// dial is Dial claiming protocol, which Stop lowers to an older server's.
func dial(p Paths, kind proto.Kind, protocol int) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), UnresponsiveTimeout)
	conn, err := ipc.Dial(ctx, ipc.Addr(p.Socket))
	cancel()
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, br: bufio.NewReader(conn)}
	conn.SetDeadline(time.Now().Add(UnresponsiveTimeout))
	hello := proto.Hello{Protocol: protocol, Version: version.Version, Build: version.BuildID(), Kind: proto.Kind(kind)}
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
	if !ipc.IsAbsent(err) {
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
		lk.Unlock()
		if !autostart {
			return nil, ErrNotRunning
		}
		if err := StartDetached(p); err != nil {
			return nil, err
		}
		c, err := Dial(p, proto.KindControl)
		if c != nil {
			c.Started = true
		}
		return c, err
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

// StartDetached starts the server and waits until it answers. On macOS it
// goes through launchd's GUI domain (StartLaunchd), so the server runs in
// the desktop's session, with the keychain, whatever session the caller
// is in; elsewhere, or with TERMINATR_LAUNCHD=off, it starts it as a
// child (StartChild).
func StartDetached(p Paths) error {
	if service.Wanted(runtime.GOOS, os.Getenv) {
		return StartLaunchd(p)
	}
	return StartChild(p)
}

// LaunchdConfig is the launchd job for the server of p, handing it this
// process's environment (docs/SPEC.md §3.1).
func LaunchdConfig(p Paths) (service.Config, error) {
	return service.Current(os.Getenv, os.Environ(), p.Home, p.RunDir, filepath.Dir(p.Log))
}

// StartLaunchd pins this tm and starts the server through launchd
// (service.Config.Start) from the pin, and waits until it answers. It fails with service.ErrNoConsole when
// nobody is logged in at the Mac.
func StartLaunchd(p Paths) error {
	c, err := LaunchdConfig(p)
	if err != nil {
		return err
	}
	// launchd runs the pin (service.Config.Program), so it must exist
	// and hold this build: no server runs, so none uses it.
	if _, err := pinBinary(p.RunDir, c.Bin); err != nil {
		return fmt.Errorf("start the server through launchd: %w", err)
	}
	if err := c.Start(); err != nil {
		if errors.Is(err, service.ErrNoConsole) {
			return err
		}
		return fmt.Errorf("start the server through launchd: %w; tm server start --no-launchd starts it without (and without the keychain)", err)
	}
	return waitUp(p, nil, p.Log+" and "+c.ServiceLog())
}

// StartChild starts `tm server run --detached` detached
// (proc.StartDetached: a new session with no controlling terminal and
// stdio on the null device), and waits until it answers. On macOS the
// server runs in the caller's security session.
func StartChild(p Paths) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	child, err := proc.StartDetached(proc.Spec{Argv: []string{bin, "server", "run", "--detached"}, Dir: "/"})
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		st, err := child.Wait()
		if err == nil {
			err = errors.New(st.String())
		}
		exited <- err
	}()
	return waitUp(p, exited, p.Log)
}

// waitUp waits until the server answers, or exited (nil when the server
// isn't a child) says it ended; logs names where to look.
func waitUp(p Paths, exited <-chan error, logs string) error {
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
			return fmt.Errorf("server exited during start (%v); see %s", err, logs)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("server did not answer within %v; see %s", AutoStartTimeout, logs)
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

// CallWithin is Call that gives up after d: Dial clears the
// connection's deadline after the handshake, so a plain Call waits as
// long as the server takes. The deadline is cleared again after.
func (c *Client) CallWithin(d time.Duration, method string, params, result any) error {
	c.conn.SetDeadline(time.Now().Add(d))
	defer c.conn.SetDeadline(time.Time{})
	return c.Call(method, params, result)
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
		lk.Unlock()
		os.Remove(p.Socket)
		os.Remove(p.PID)
		return 0, ErrNotRunning
	}
	if !errors.Is(err, ErrLocked) {
		return 0, err
	}
	pid := readPID(p.PID)
	if !proc.Alive(pid) {
		return pid, fmt.Errorf("lock is held but pid file names no live process (%d)", pid)
	}
	if err := proc.Kill(pid); err != nil {
		return pid, err
	}
	return pid, nil
}

// WaitStopped waits until no server holds the lock.
func WaitStopped(p Paths, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if lk, err := tryLock(p.Lock); err == nil {
			lk.Unlock()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitExited waits until pid no longer exists.
func WaitExited(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for proc.Alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// ViewStream is a view subscription (docs/SPEC.md §3.3, Views): the
// view's versions as the server sends them. Closing it leaves the view.
type ViewStream struct {
	c *Client
	// Client is this console's id in the view, for the view.* calls.
	Client string
	// Digest, when set before the first Next, is called for each
	// view.digest event.
	Digest func()
}

// SubscribeView joins a view and returns the stream and the view as it
// was on joining.
func SubscribeView(p Paths, params proto.ViewSubscribeParams) (*ViewStream, view.View, error) {
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		return nil, view.View{}, err
	}
	var res proto.ViewSubscribeResult
	if err := c.Call(proto.MethodViewSubscribe, params, &res); err != nil {
		c.Close()
		return nil, view.View{}, err
	}
	return &ViewStream{c: c, Client: res.Client}, res.View, nil
}

// Next blocks until the view's next version.
func (s *ViewStream) Next() (view.View, error) {
	var ev proto.ViewEvent
	for {
		if err := readJSONLine(s.c.br, &ev); err != nil {
			return view.View{}, err
		}
		switch ev.Event {
		case proto.EventViewChanged:
			return ev.View, nil
		case proto.EventViewDigest:
			if s.Digest != nil {
				s.Digest()
			}
		}
	}
}

// Close leaves the view.
func (s *ViewStream) Close() error { return s.c.Close() }

// WatchStream is a session watch (session.watch): the session's states
// as the server sends them. Closing it ends the watch.
type WatchStream struct{ c *Client }

// WatchSession watches session id and returns the stream and the
// session's state on joining.
func WatchSession(p Paths, id string) (*WatchStream, proto.Watch, error) {
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		return nil, proto.Watch{}, err
	}
	var w proto.Watch
	if err := c.Call(proto.MethodSessionWatch, proto.SessionIDParams{ID: id}, &w); err != nil {
		c.Close()
		return nil, proto.Watch{}, err
	}
	return &WatchStream{c: c}, w, nil
}

// Next blocks until the session's next state. After the one whose state
// is "exited" the server hangs up and Next returns io.EOF.
func (s *WatchStream) Next() (proto.Watch, error) {
	for {
		var ev proto.WatchEvent
		if err := readJSONLine(s.c.br, &ev); err != nil {
			return proto.Watch{}, err
		}
		if ev.Event == proto.EventWatchChanged {
			return ev.Watch, nil
		}
	}
}

// Close ends the watch.
func (s *WatchStream) Close() error { return s.c.Close() }

// AskSession opens question q on session id (session.ask) and waits
// until it ends: the answers by question text once every question has
// one, or nil when it closed without (replaced, or the session ended).
// The caller ending takes the question down with its connection.
func AskSession(p Paths, id string, q proto.Question) (map[string]string, error) {
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := c.Call(proto.MethodSessionAsk, proto.SessionAskParams{ID: id, Question: q}, nil); err != nil {
		return nil, err
	}
	for {
		var ev proto.AskEvent
		if err := readJSONLine(c.br, &ev); err != nil {
			return nil, err
		}
		switch ev.Event {
		case proto.EventAskAnswered:
			return ev.Answers, nil
		case proto.EventAskClosed:
			return nil, nil
		}
	}
}
