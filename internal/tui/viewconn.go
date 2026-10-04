package tui

import (
	"errors"
	"sync"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/view"
)

// ViewConn is a console's membership of a server-owned view (docs/SPEC.md
// §3.3, Views): the subscription that brings each new version, and the
// calls that change it. It outlives the dashboard and attach screens,
// which both render what it holds. When the server goes away it keeps
// trying to join the view again, as the same kind of member.
type ViewConn struct {
	paths server.Paths

	mu     sync.Mutex
	params proto.ViewSubscribeParams // to join again
	stream *server.ViewStream
	client string
	v      view.View
	up     bool // joined; false while the server is away
	closed bool
	watch  map[chan struct{}]bool // woken by a new version, or up changing

	callMu sync.Mutex // one call at a time on ctl
	ctl    *server.Client
}

// ErrViewDown is returned by calls while the server is away.
var ErrViewDown = errors.New("not joined to a view (the server is away)")

// JoinView joins a view. A failure to join the first time is returned;
// later the connection rejoins on its own.
func JoinView(p server.Paths, params proto.ViewSubscribeParams) (*ViewConn, error) {
	vc := &ViewConn{paths: p, params: params, watch: map[chan struct{}]bool{}}
	st, v, err := server.SubscribeView(p, params)
	if err != nil {
		return nil, err
	}
	vc.params.Session = "" // shown once; a rejoin keeps what the view shows
	vc.stream, vc.client, vc.v, vc.up = st, st.Client, v, true
	go vc.run(st)
	return vc, nil
}

// run reads the stream until it ends, then joins again until it works
// or the connection is closed.
func (vc *ViewConn) run(st *server.ViewStream) {
	for {
		for {
			v, err := st.Next()
			if err != nil {
				break
			}
			vc.set(v)
		}
		st.Close()
		vc.mu.Lock()
		vc.up = false
		closed := vc.closed
		vc.mu.Unlock()
		vc.notify()
		if closed {
			return
		}
		for {
			time.Sleep(viewRejoin)
			vc.mu.Lock()
			if vc.closed {
				vc.mu.Unlock()
				return
			}
			params := vc.params
			vc.mu.Unlock()
			s, v, err := server.SubscribeView(vc.paths, params)
			if err != nil {
				continue
			}
			vc.mu.Lock()
			if vc.closed {
				vc.mu.Unlock()
				s.Close()
				return
			}
			// A new server counts versions from scratch.
			vc.stream, vc.client, vc.v, vc.up, st = s, s.Client, v, true, s
			vc.mu.Unlock()
			vc.dropCtl()
			vc.notify()
			break
		}
	}
}

// viewRejoin is how often a console tries to join again while the server
// is away.
const viewRejoin = 500 * time.Millisecond

// set takes v when it is newer than what the console has.
func (vc *ViewConn) set(v view.View) {
	vc.mu.Lock()
	newer := v.Name == vc.v.Name && v.Seq > vc.v.Seq
	if newer {
		vc.v = v
	}
	vc.mu.Unlock()
	if newer {
		vc.notify()
	}
}

func (vc *ViewConn) notify() {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	for ch := range vc.watch {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// View is the newest version the console has.
func (vc *ViewConn) View() view.View {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.v.Clone()
}

// Up says whether the console is joined (the server is there).
func (vc *ViewConn) Up() bool {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.up
}

// Client is the console's id in the view.
func (vc *ViewConn) Client() string {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.client
}

// Watch returns a channel that receives when the view has a new version
// or the server came or went, until stop is called. Each screen watches
// with its own, so one left behind never takes another's wake-up.
func (vc *ViewConn) Watch() (ch <-chan struct{}, stop func()) {
	c := make(chan struct{}, 1)
	vc.mu.Lock()
	vc.watch[c] = true
	vc.mu.Unlock()
	return c, func() {
		vc.mu.Lock()
		delete(vc.watch, c)
		vc.mu.Unlock()
	}
}

// Do runs a view.* action and returns the view afterwards, which the
// console takes at once: it never waits for its own change to come back.
func (vc *ViewConn) Do(method string, p proto.ViewParams) (view.View, error) {
	vc.mu.Lock()
	p.Client = vc.client
	if !vc.up {
		vc.mu.Unlock()
		return vc.View(), ErrViewDown
	}
	if method == proto.MethodViewSize {
		// Rejoining reports the window as it is now.
		vc.params.Cols, vc.params.Rows = p.Cols, p.Rows
	}
	vc.mu.Unlock()
	vc.callMu.Lock()
	defer vc.callMu.Unlock()
	if vc.ctl == nil {
		c, err := server.Connect(vc.paths, false)
		if err != nil {
			return vc.View(), err
		}
		vc.ctl = c
	}
	var v view.View
	if err := vc.ctl.Call(method, p, &v); err != nil {
		var perr *proto.Error
		if !errors.As(err, &perr) {
			vc.ctl.Close()
			vc.ctl = nil
		}
		return vc.View(), err
	}
	vc.set(v)
	return vc.View(), nil
}

// dropCtl closes the call connection, after the server changed.
func (vc *ViewConn) dropCtl() {
	vc.callMu.Lock()
	defer vc.callMu.Unlock()
	if vc.ctl != nil {
		vc.ctl.Close()
		vc.ctl = nil
	}
}

// WaitChange waits up to d for a version newer than seq, or for the
// server to go away.
func (vc *ViewConn) WaitChange(seq uint64, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		vc.mu.Lock()
		done := vc.v.Seq != seq || !vc.up
		vc.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Close leaves the view.
func (vc *ViewConn) Close() {
	vc.mu.Lock()
	vc.closed = true
	st := vc.stream
	vc.mu.Unlock()
	if st != nil {
		st.Close()
	}
	vc.dropCtl()
}
