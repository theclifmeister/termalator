package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/plat/fsx"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/session"
	"github.com/theclifmeister/terminatr/internal/view"
)

// Server-owned views (docs/SPEC.md §3.3, Views). The server keeps every
// view, applies the view.* actions its clients send, sizes the sessions
// the view shows to the window of its latest client, and sends every
// subscribed client each new version. Views other than own views are
// saved in state/views.json and come back after a restart.
//
// Lock order: views.mu, then the server's mu (through viewHost); nothing
// holding the server's mu calls into views.

// viewHost is what views need from the server: the sessions.
type viewHost interface {
	pane(id string) (paneInfo, bool)
	resizePane(id string, cols, rows uint16)
}

// paneInfo is what views know about a session.
type paneInfo struct {
	// follows is false for an agent whose manifest says screen.resize =
	// "explicit": typing doesn't resize it.
	follows bool
	// sized is set once a console has sized the session; until then the
	// first view showing it gives it its rectangle (fill).
	sized bool
	// panel: the session is a thread's or a coordinator's, so a view
	// showing it has the info panel (view.View.Panel).
	panel bool
}

type views struct {
	host viewHost
	path string // views.json; "" saves nothing
	logf func(string, ...any)

	mu         sync.Mutex
	byName     map[string]*liveView
	nextClient int
	nextOwn    int
}

// liveView is a view and the clients joined to it.
type liveView struct {
	v       view.View
	members map[string]*member
}

// member is one client joined to a view.
type member struct {
	id         string
	cols, rows uint16
	active     time.Time // its last input, resize or layout change
	notify     chan struct{}
	pid        int           // the console's process
	digest     chan struct{} // view.digest asked for a check
}

func newViews(host viewHost, path string, logf func(string, ...any)) *views {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &views{host: host, path: path, logf: logf, byName: map[string]*liveView{}}
}

// viewsFile is views.json.
type viewsFile struct {
	Version int         `json:"version"`
	Views   []view.View `json:"views"`
}

const viewsVersion = 1

// load reads views.json, dropping the panes whose sessions didn't come
// back. A broken file or view is logged and skipped.
func (vs *views) load() {
	if vs.path == "" {
		return
	}
	b, err := os.ReadFile(vs.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		vs.logf("views.json unreadable, starting fresh: %v", err)
		return
	}
	vs.loadBytes(b)
}

// loadBytes takes the views from views.json's content b.
func (vs *views) loadBytes(b []byte) {
	var f viewsFile
	if err := json.Unmarshal(b, &f); err != nil {
		vs.logf("views.json unreadable, starting fresh: %v", err)
		return
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	for _, v := range f.Views {
		if v.Name == "" || v.Own {
			continue
		}
		if err := v.Valid(); err != nil {
			vs.logf("views.json: %v; dropped", err)
			continue
		}
		v.Latest = ""
		v.Prune(vs.alive)
		v.Normalize()
		vs.byName[v.Name] = &liveView{v: v, members: map[string]*member{}}
	}
	vs.saveLocked()
}

func (vs *views) alive(id string) bool {
	_, ok := vs.host.pane(id)
	return ok
}

// saveLocked writes views.json: every view but the own ones. vs.mu held.
func (vs *views) saveLocked() {
	if vs.path == "" {
		return
	}
	f := viewsFile{Version: viewsVersion, Views: []view.View{}}
	for _, lv := range vs.byName {
		if !lv.v.Own {
			v := lv.v.Clone()
			v.Latest = ""
			f.Views = append(f.Views, v)
		}
	}
	sort.Slice(f.Views, func(i, j int) bool { return f.Views[i].Name < f.Views[j].Name })
	b, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = fsx.WriteAtomic(vs.path, append(b, '\n'), 0o600)
	}
	if err != nil {
		vs.logf("views.json: %v", err)
	}
}

// changedLocked publishes lv's new version: Seq goes up, the file is
// saved and every member is woken. vs.mu held.
func (vs *views) changedLocked(lv *liveView) {
	lv.v.Normalize()
	lv.v.Seq++
	if !lv.v.Own {
		vs.saveLocked()
	}
	for _, m := range lv.members {
		select {
		case m.notify <- struct{}{}:
		default:
		}
	}
}

// subscribe joins a client to a view, creating it when needed. Joining
// never resizes a session; a view without a latest client takes the
// joiner's window as its size.
func (vs *views) subscribe(p proto.ViewSubscribeParams, pid int) (*member, string, view.View, *proto.Error) {
	cols, rows := p.Cols, p.Rows
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	if p.Session != "" && !vs.alive(p.Session) {
		return nil, "", view.View{}, proto.Errorf(proto.ErrUnknownSession, "no session %q", p.Session)
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	name := p.View
	switch {
	case p.Own:
		vs.nextOwn++
		name = fmt.Sprintf("own-%d", vs.nextOwn)
	case name == "":
		name = view.Main
	}
	lv := vs.byName[name]
	if lv == nil {
		lv = &liveView{v: view.View{Name: name, Own: p.Own, Bare: p.Own && p.Bare, StatusBar: p.StatusBar,
			Mode: view.ModeDashboard}, members: map[string]*member{}}
		if p.Sidebar != nil {
			lv.v.Sidebar = *p.Sidebar
		}
		if p.Info != nil {
			lv.v.Info = p.Info.Clamp()
		}
		vs.byName[name] = lv
	}
	vs.nextClient++
	m := &member{id: fmt.Sprintf("c-%d", vs.nextClient), cols: cols, rows: rows, notify: make(chan struct{}, 1),
		pid: pid, digest: make(chan struct{}, 1)}
	lv.members[m.id] = m
	if lv.members[lv.v.Latest] == nil {
		vs.makeLatest(lv, m)
	}
	if p.Session != "" {
		lv.v.Attach(p.Session, "")
	}
	vs.tag(&lv.v)
	vs.fill(lv)
	vs.changedLocked(lv)
	vs.logf("view %s: client %s joined at %d×%d", name, m.id, cols, rows)
	return m, name, lv.v.Clone(), nil
}

// leave takes a client out of its view. An own view goes with its last
// client; the latest client's place goes to the member active last,
// without resizing anything.
func (vs *views) leave(name string, m *member) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	lv := vs.byName[name]
	if lv == nil || lv.members[m.id] == nil {
		return
	}
	delete(lv.members, m.id)
	vs.logf("view %s: client %s left", name, m.id)
	if lv.v.Own && len(lv.members) == 0 {
		delete(vs.byName, name)
		return
	}
	if lv.v.Latest != m.id {
		return
	}
	lv.v.Latest = ""
	var next *member
	for _, o := range lv.members {
		if next == nil || o.active.After(next.active) {
			next = o
		}
	}
	if next != nil {
		vs.makeLatest(lv, next)
	}
	vs.changedLocked(lv)
}

// askDigest asks every member of process pid for a digest check
// (view.digest) and says how many there were.
func (vs *views) askDigest(pid int) int {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	n := 0
	for _, lv := range vs.byName {
		for _, m := range lv.members {
			if m.pid != pid {
				continue
			}
			n++
			select {
			case m.digest <- struct{}{}:
			default:
			}
		}
	}
	return n
}

// get is the view's current version.
func (vs *views) get(name string) (view.View, bool) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	lv := vs.byName[name]
	if lv == nil {
		return view.View{}, false
	}
	return lv.v.Clone(), true
}

// makeLatest makes m the client whose window sizes lv. vs.mu held.
func (vs *views) makeLatest(lv *liveView, m *member) {
	lv.v.Latest, lv.v.Cols, lv.v.Rows = m.id, m.cols, m.rows
}

// sessionGone removes an ended session from every view.
func (vs *views) sessionGone(id string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	for _, lv := range vs.byName {
		if lv.v.Remove(id) {
			vs.fill(lv)
			vs.changedLocked(lv)
		}
	}
}

// renameProject follows a renamed project in every view's current
// project and selected rows (tm project rename).
func (vs *views) renameProject(from, to string) {
	key := func(k string) string {
		for _, pre := range []string{"p:", "c:", "t:"} {
			if rest, ok := strings.CutPrefix(k, pre+from); ok && (rest == "" || rest[0] == '/') {
				return pre + to + rest
			}
		}
		return k
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	for _, lv := range vs.byName {
		v := &lv.v
		cur, sel, side := v.Current, key(v.Selected), key(v.SideSel)
		if cur == from {
			cur = to
		}
		if cur != v.Current || sel != v.Selected || side != v.SideSel {
			v.Current, v.Selected, v.SideSel = cur, sel, side
			vs.changedLocked(lv)
		}
	}
}

// tag marks whether v shows a thread's or a coordinator's session,
// which has the info panel beside it. vs.mu held.
func (vs *views) tag(v *view.View) {
	info, ok := vs.host.pane(v.Focus)
	v.Panel = v.Focus != "" && ok && info.panel
}

// resize sizes the session lv shows to its pane at its size. With claim
// set (a claim from typing) only a session that follows typing is
// resized. vs.mu held.
func (vs *views) resize(lv *liveView, claim bool) {
	v := &lv.v
	if v.Mode != view.ModeLayout || v.Cols == 0 || v.Rows == 0 {
		return
	}
	g := v.Lay(int(v.Cols), int(v.Rows))
	r := g.Area
	info, alive := vs.host.pane(g.Pane)
	if g.Pane == "" || !alive || r.W < 1 || r.H < 1 || claim && !info.follows {
		return
	}
	vs.host.resizePane(g.Pane, uint16(r.W), uint16(r.H))
}

// fill gives the session lv shows, if no console has sized it yet, its
// pane's size: a new pane fills the console that shows it
// first. Panes already sized, and agents whose manifest says
// screen.resize = "explicit", are left alone: showing a pane never
// resizes it after that (docs/SPEC.md §3.3). vs.mu held.
func (vs *views) fill(lv *liveView) {
	v := &lv.v
	if v.Mode != view.ModeLayout || v.Cols == 0 || v.Rows == 0 {
		return
	}
	g := v.Lay(int(v.Cols), int(v.Rows))
	r := g.Area
	info, alive := vs.host.pane(g.Pane)
	if g.Pane == "" || !alive || info.sized || !info.follows || r.W < 1 || r.H < 1 {
		return
	}
	vs.host.resizePane(g.Pane, uint16(r.W), uint16(r.H))
}

// do runs one view.* action for a client.
func (vs *views) do(method string, p proto.ViewParams) (view.View, *proto.Error) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	var lv *liveView
	var m *member
	for _, l := range vs.byName {
		if l.members[p.Client] != nil {
			lv, m = l, l.members[p.Client]
		}
	}
	if lv == nil {
		return view.View{}, proto.Errorf(proto.ErrBadParams, "no view client %q", p.Client)
	}
	v := &lv.v
	before := v.Clone()
	// layout marks a change of the layout's geometry: the client becomes
	// the latest and the panes are resized to the new rectangles.
	layout := func() {
		m.active = time.Now()
		vs.makeLatest(lv, m)
	}
	resize, claim := false, false
	switch method {
	case proto.MethodViewAttach:
		if !vs.alive(p.Session) {
			return before, proto.Errorf(proto.ErrUnknownSession, "no session %q", p.Session)
		}
		v.Attach(p.Session, p.Project)
	case proto.MethodViewDashboard:
		v.Dashboard()
	case proto.MethodViewProject:
		if p.Project == "" {
			return before, proto.Errorf(proto.ErrBadParams, "no project")
		}
		v.ShowProject(p.Project)
	case proto.MethodViewSelect:
		v.Selected = p.Key
	case proto.MethodViewSideSel:
		if len(p.Key) > view.MaxKey {
			return before, proto.Errorf(proto.ErrBadParams, "key too long")
		}
		v.SideSel = p.Key
	case proto.MethodViewSidebar:
		if p.Sidebar == nil {
			return before, proto.Errorf(proto.ErrBadParams, "no sidebar")
		}
		v.Sidebar = p.Sidebar.Clamp()
		layout()
		resize = true
	case proto.MethodViewInfo:
		if p.Info == nil {
			return before, proto.Errorf(proto.ErrBadParams, "no info panel")
		}
		v.Info = p.Info.Clamp()
		layout()
		resize = true
	case proto.MethodViewSize:
		if p.Cols == 0 || p.Rows == 0 {
			return before, proto.Errorf(proto.ErrBadParams, "invalid size %d×%d", p.Cols, p.Rows)
		}
		m.cols, m.rows = p.Cols, p.Rows
		switch {
		case p.Resize:
			// The user really resized the window: that always resizes
			// (docs/SPEC.md §3.3).
			layout()
			resize = true
		case v.Latest == m.id:
			v.Cols, v.Rows = m.cols, m.rows
		}
	case proto.MethodViewInput:
		if v.Mode != view.ModeLayout {
			break
		}
		layout()
		resize, claim = true, true
	default:
		return before, proto.Errorf(proto.ErrUnknownMethod, "unknown method %q", method)
	}
	v.Normalize()
	vs.tag(v)
	if resize {
		vs.resize(lv, claim)
	}
	vs.fill(lv)
	if !view.Equal(before, *v) {
		vs.changedLocked(lv)
	}
	return v.Clone(), nil
}

// The server is the views' host.

func (s *Server) pane(id string) (paneInfo, bool) {
	sess, perr := s.session(id)
	if perr != nil {
		return paneInfo{}, false
	}
	return paneInfo{follows: agent.FollowsTyping(sess.Agent()), sized: sess.Sized(), panel: hasPanel(sess.Config().Role)}, true
}

func (s *Server) resizePane(id string, cols, rows uint16) {
	sess, perr := s.session(id)
	if perr != nil {
		return
	}
	if err := sess.RequestResize(cols, rows); err != nil && !errors.Is(err, session.ErrExited) {
		s.log.Printf("session %s: resize: %v", id, err)
	}
}

// serveViewStream answers view.subscribe and then streams the view: a
// view.changed line for every new version, until the client hangs up,
// which leaves the view.
func (s *Server) serveViewStream(c net.Conn, br *bufio.Reader, req proto.Request, pid int) {
	var p proto.ViewSubscribeParams
	resp := proto.Response{ID: req.ID}
	var m *member
	var name string
	if perr := decodeParams(req.Params, &p); perr != nil {
		resp.Error = perr
	} else {
		var v view.View
		m, name, v, perr = s.views.subscribe(p, pid)
		if perr != nil {
			resp.Error = perr
		} else {
			resp.Result, _ = json.Marshal(proto.ViewSubscribeResult{Client: m.id, View: v})
		}
	}
	if err := writeJSONLine(c, resp); err != nil || m == nil {
		if m != nil {
			s.views.leave(name, m)
		}
		return
	}
	defer s.views.leave(name, m)
	// The client sends nothing more; a read returning is it leaving.
	gone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, br)
		close(gone)
	}()
	var sent uint64
	for {
		select {
		case <-gone:
			return
		case <-m.digest:
			if err := writeJSONLine(c, proto.ViewEvent{Event: proto.EventViewDigest}); err != nil {
				return
			}
			continue
		case <-m.notify:
		}
		v, ok := s.views.get(name)
		if !ok {
			return
		}
		if v.Seq == sent {
			continue
		}
		sent = v.Seq
		if err := writeJSONLine(c, proto.ViewEvent{Event: proto.EventViewChanged, View: v}); err != nil {
			return
		}
	}
}

// hasPanel says a session of role has the info panel beside its pane: a
// thread's (its task and PR) or a coordinator's (its project, T90).
func hasPanel(role string) bool { return role == proto.RoleThread || role == proto.RoleCoordinator }
