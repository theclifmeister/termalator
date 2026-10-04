package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/view"
)

// fakeHost is a viewHost with sessions in memory.
type fakeHost struct {
	mu       sync.Mutex
	panes    map[string]paneInfo
	sizes    map[string][2]uint16
	resizes  []string // "id cols×rows", in order
	started  int
	startErr error
}

func newFakeHost(ids ...string) *fakeHost {
	h := &fakeHost{panes: map[string]paneInfo{}, sizes: map[string][2]uint16{}}
	for _, id := range ids {
		h.panes[id] = paneInfo{cwd: "/", role: proto.RoleShell, follows: true}
	}
	return h
}

func (h *fakeHost) pane(id string) (paneInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.panes[id]
	return p, ok
}

func (h *fakeHost) resizePane(id string, cols, rows uint16) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sizes[id] = [2]uint16{cols, rows}
	h.resizes = append(h.resizes, fmt.Sprintf("%s %d×%d", id, cols, rows))
}

func (h *fakeHost) startShell(cwd string, cols, rows uint16) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.startErr != nil {
		return "", h.startErr
	}
	h.started++
	id := fmt.Sprintf("n-%d", h.started)
	h.panes[id] = paneInfo{cwd: cwd, role: proto.RoleShell, follows: true}
	h.sizes[id] = [2]uint16{cols, rows}
	return id, nil
}

// takeResizes returns the resizes since the last call.
func (h *fakeHost) takeResizes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.resizes
	h.resizes = nil
	return r
}

func mustDo(t *testing.T, vs *views, method string, p proto.ViewParams) view.View {
	t.Helper()
	v, err := vs.do(method, p)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return v
}

func join(t *testing.T, vs *views, p proto.ViewSubscribeParams) (*member, string) {
	t.Helper()
	m, name, _, err := vs.subscribe(p)
	if err != nil {
		t.Fatal(err)
	}
	return m, name
}

// woken says whether m was notified, and clears it.
func woken(m *member) bool {
	select {
	case <-m.notify:
		return true
	default:
		return false
	}
}

func TestViewsSharedMain(t *testing.T) {
	h := newFakeHost("s-1", "s-2")
	vs := newViews(h, "", nil)
	a, name := join(t, vs, proto.ViewSubscribeParams{Cols: 120, Rows: 40})
	b, _ := join(t, vs, proto.ViewSubscribeParams{Cols: 100, Rows: 32})
	if name != view.Main {
		t.Fatalf("joined %q", name)
	}
	woken(a)
	woken(b)
	v, _ := vs.get(view.Main)
	if v.Latest != a.id || v.Cols != 120 || v.Rows != 40 || v.Mode != view.ModeDashboard {
		t.Fatalf("the first to join sizes the view: %+v", v)
	}

	// A attaches: both see it; attaching never resizes.
	v = mustDo(t, vs, proto.MethodViewAttach, proto.ViewParams{Client: a.id, Session: "s-1", Project: "p"})
	if v.Mode != view.ModeLayout || v.Focus != "s-1" || v.Current != "p" {
		t.Fatalf("attach: %+v", v)
	}
	if !woken(a) || !woken(b) {
		t.Fatal("not broadcast")
	}
	if r := h.takeResizes(); len(r) != 0 {
		t.Fatalf("attaching resized: %v", r)
	}

	// B types: B is the latest, the pane takes B's size (less the
	// sidebar and the status bar).
	v = mustDo(t, vs, proto.MethodViewInput, proto.ViewParams{Client: b.id, Session: "s-1"})
	if v.Latest != b.id || v.Cols != 100 || v.Rows != 32 {
		t.Fatalf("after B typed: %+v", v)
	}
	if r := h.takeResizes(); !slices.Equal(r, []string{"s-1 76×31"}) {
		t.Fatalf("resizes %v", r)
	}
	// Typing again changes nothing: no new version.
	woken(a)
	mustDo(t, vs, proto.MethodViewInput, proto.ViewParams{Client: b.id, Session: "s-1"})
	if woken(a) {
		t.Fatal("a claim that changed nothing was broadcast")
	}
	h.takeResizes() // the same size again, which a session ignores

	// A selects a row on the way: the view keeps it.
	mustDo(t, vs, proto.MethodViewSelect, proto.ViewParams{Client: a.id, Key: "p:x"})
	if v, _ = vs.get(view.Main); v.Selected != "p:x" {
		t.Fatalf("selected %q", v.Selected)
	}

	// A window resize from A always resizes, and makes A the latest.
	v = mustDo(t, vs, proto.MethodViewSize, proto.ViewParams{Client: a.id, Cols: 130, Rows: 40, Resize: true})
	if v.Latest != a.id || v.Cols != 130 || !slices.Equal(h.takeResizes(), []string{"s-1 106×39"}) {
		t.Fatalf("after A resized: %+v", v)
	}
	// A size report without a resize (joining) only records it.
	mustDo(t, vs, proto.MethodViewSize, proto.ViewParams{Client: b.id, Cols: 90, Rows: 30})
	if v, _ = vs.get(view.Main); v.Latest != a.id || len(h.takeResizes()) != 0 {
		t.Fatalf("a size report took over: %+v", v)
	}

	// The latest leaving hands over to B, resizing nothing.
	vs.leave(view.Main, a)
	if v, _ = vs.get(view.Main); v.Latest != b.id || v.Cols != 90 || len(h.takeResizes()) != 0 {
		t.Fatalf("after A left: %+v", v)
	}
	// main outlives its clients.
	vs.leave(view.Main, b)
	if _, ok := vs.get(view.Main); !ok {
		t.Fatal("main went with its clients")
	}
	if _, err := vs.do(proto.MethodViewZoom, proto.ViewParams{Client: a.id}); err == nil {
		t.Fatal("a client that left can still act")
	}
}

func TestViewsClaimSkipsWatchOnlyAndExplicit(t *testing.T) {
	h := newFakeHost("co", "th", "inl")
	h.panes["th"] = paneInfo{cwd: "/", role: proto.RoleThread, follows: true}
	h.panes["inl"] = paneInfo{cwd: "/", role: proto.RoleShell, follows: false}
	vs := newViews(h, "", nil)
	a, _ := join(t, vs, proto.ViewSubscribeParams{View: "x", Cols: 120, Rows: 40})
	b, _ := join(t, vs, proto.ViewSubscribeParams{View: "x", Cols: 100, Rows: 30})
	mustDo(t, vs, proto.MethodViewAttach, proto.ViewParams{Client: a.id, Session: "co"})
	vs.mu.Lock()
	lv := vs.byName["x"]
	lv.v.Split("co", "th", true)
	lv.v.Split("th", "inl", false)
	vs.mu.Unlock()

	// B types into the coordinator: only it is resized; the thread is
	// watch-only and the inline agent doesn't follow typing.
	mustDo(t, vs, proto.MethodViewInput, proto.ViewParams{Client: b.id, Session: "co"})
	if r := h.takeResizes(); len(r) != 1 || r[0][:3] != "co " {
		t.Fatalf("claim resized %v", r)
	}
	// Typing into the thread (taken over) resizes it too.
	mustDo(t, vs, proto.MethodViewInput, proto.ViewParams{Client: b.id, Session: "th"})
	if r := h.takeResizes(); len(r) != 2 {
		t.Fatalf("claim from the thread resized %v", r)
	}
	// A layout change resizes every visible pane, whatever it is.
	mustDo(t, vs, proto.MethodViewEven, proto.ViewParams{Client: a.id})
	if r := h.takeResizes(); len(r) != 3 {
		t.Fatalf("even resized %v", r)
	}
}

func TestViewsLayoutActions(t *testing.T) {
	h := newFakeHost("s-1")
	vs := newViews(h, "", nil)
	a, _ := join(t, vs, proto.ViewSubscribeParams{Cols: 120, Rows: 30, Sidebar: &view.Sidebar{Width: 20}})
	v := mustDo(t, vs, proto.MethodViewAttach, proto.ViewParams{Client: a.id, Session: "s-1"})
	if v.Sidebar.Width != 20 {
		t.Fatalf("the new view's sidebar: %+v", v.Sidebar)
	}
	// A split starts the shell at its pane's size and focuses it.
	v = mustDo(t, vs, proto.MethodViewSplit, proto.ViewParams{Client: a.id, Side: true})
	if v.Focus != "n-1" || len(v.Root.Leaves()) != 2 {
		t.Fatalf("split: %+v", v)
	}
	g := v.Lay(120, 30)
	if r := g.Panes["n-1"]; h.sizes["n-1"] != [2]uint16{uint16(r.W), uint16(r.H)} {
		t.Fatalf("the new shell is %v, its pane %+v", h.sizes["n-1"], r)
	}
	if r := g.Panes["s-1"]; h.sizes["s-1"] != [2]uint16{uint16(r.W), uint16(r.H)} {
		t.Fatalf("the split pane is %v, its rect %+v", h.sizes["s-1"], r)
	}
	v = mustDo(t, vs, proto.MethodViewFocus, proto.ViewParams{Client: a.id, DX: -1})
	if v.Focus != "s-1" {
		t.Fatalf("focus left: %s", v.Focus)
	}
	v = mustDo(t, vs, proto.MethodViewZoom, proto.ViewParams{Client: a.id})
	if !v.Zoom || h.sizes["s-1"] != [2]uint16{100, 29} {
		t.Fatalf("zoom: %+v %v", v, h.sizes["s-1"])
	}
	v = mustDo(t, vs, proto.MethodViewSidebar, proto.ViewParams{Client: a.id, Sidebar: &view.Sidebar{Slim: true}})
	if !v.Sidebar.Slim || h.sizes["s-1"] != [2]uint16{120 - view.SideSlim, 29} {
		t.Fatalf("sidebar: %+v %v", v.Sidebar, h.sizes["s-1"])
	}
	// Closing the last pane goes back to the dashboard.
	mustDo(t, vs, proto.MethodViewClose, proto.ViewParams{Client: a.id})
	v = mustDo(t, vs, proto.MethodViewClose, proto.ViewParams{Client: a.id})
	if v.Mode != view.ModeDashboard || v.Root != nil {
		t.Fatalf("closed both: %+v", v)
	}
	if _, err := vs.do(proto.MethodViewAttach, proto.ViewParams{Client: a.id, Session: "nope"}); err == nil {
		t.Fatal("attached a session that doesn't exist")
	}

	// A session ending leaves the views.
	mustDo(t, vs, proto.MethodViewAttach, proto.ViewParams{Client: a.id, Session: "s-1"})
	vs.sessionGone("s-1")
	if v, _ = vs.get(view.Main); v.Mode != view.ModeDashboard {
		t.Fatalf("after the session ended: %+v", v)
	}
}

func TestViewsOwn(t *testing.T) {
	h := newFakeHost("s-1", "s-2")
	vs := newViews(h, "", nil)
	main, _ := join(t, vs, proto.ViewSubscribeParams{Cols: 80, Rows: 24})
	own, name := join(t, vs, proto.ViewSubscribeParams{Own: true, Bare: true, Session: "s-2", Cols: 80, Rows: 24})
	if name == view.Main {
		t.Fatal("an own view is main")
	}
	v, _ := vs.get(name)
	if !v.Own || !v.Bare || v.Mode != view.ModeLayout || v.Focus != "s-2" {
		t.Fatalf("own view: %+v", v)
	}
	woken(main)
	mustDo(t, vs, proto.MethodViewDashboard, proto.ViewParams{Client: own.id})
	if woken(main) {
		t.Fatal("main heard of an own view's change")
	}
	if v, _ := vs.get(view.Main); v.Mode != view.ModeDashboard || v.Root != nil {
		t.Fatalf("main changed: %+v", v)
	}
	vs.leave(name, own)
	if _, ok := vs.get(name); ok {
		t.Fatal("the own view outlived its client")
	}
	if _, _, _, err := vs.subscribe(proto.ViewSubscribeParams{Own: true, Session: "gone"}); err == nil {
		t.Fatal("subscribed showing a session that doesn't exist")
	}
}

func TestViewsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "views.json")
	h := newFakeHost("s-1", "s-2")
	vs := newViews(h, path, nil)
	a, _ := join(t, vs, proto.ViewSubscribeParams{Cols: 120, Rows: 30})
	mustDo(t, vs, proto.MethodViewAttach, proto.ViewParams{Client: a.id, Session: "s-1", Project: "p"})
	vs.mu.Lock()
	vs.byName[view.Main].v.Split("s-1", "s-2", false)
	vs.changedLocked(vs.byName[view.Main])
	vs.mu.Unlock()
	mustDo(t, vs, proto.MethodViewSidebar, proto.ViewParams{Client: a.id, Sidebar: &view.Sidebar{Width: 30}})
	join(t, vs, proto.ViewSubscribeParams{Own: true, Cols: 80, Rows: 24})

	// A new server: s-2 (a shell) didn't come back.
	h2 := newFakeHost("s-1")
	vs2 := newViews(h2, path, nil)
	vs2.load()
	v, ok := vs2.get(view.Main)
	if !ok || v.Mode != view.ModeLayout || v.Current != "p" || v.Sidebar.Width != 30 || v.Latest != "" {
		t.Fatalf("loaded: %+v", v)
	}
	if got := v.Root.Leaves(); !slices.Equal(got, []string{"s-1"}) || v.Focus != "s-1" {
		t.Fatalf("loaded panes %v focus %s", got, v.Focus)
	}
	if len(vs2.byName) != 1 {
		t.Fatalf("own views were saved: %v", vs2.byName)
	}
	// Nothing survives: the dashboard.
	vs3 := newViews(newFakeHost(), path, nil)
	vs3.load()
	if v, _ := vs3.get(view.Main); v.Mode != view.ModeDashboard || v.Root != nil || v.Current != "p" {
		t.Fatalf("loaded without sessions: %+v", v)
	}
}

// TestViewSubscribeStream: over the socket, one console's action reaches
// another's subscription.
func TestViewSubscribeStream(t *testing.T) {
	p := testPaths(t)
	startServer(t, p)
	c, err := Dial(p, proto.KindControl)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var started proto.SessionStartResult
	call(t, c, proto.MethodSessionStart, proto.SessionStartParams{Argv: []string{"/bin/sh"}, Cwd: "/"}, &started)
	id := started.Session.ID

	a, v, err := SubscribeView(p, proto.ViewSubscribeParams{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, _, err := SubscribeView(p, proto.ViewSubscribeParams{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if v.Name != view.Main || v.Latest != a.Client {
		t.Fatalf("joined %+v", v)
	}
	got := make(chan view.View, 16)
	go func() {
		for {
			v, err := b.Next()
			if err != nil {
				close(got)
				return
			}
			got <- v
		}
	}()
	var after view.View
	call(t, c, proto.MethodViewAttach, proto.ViewParams{Client: a.Client, Session: id}, &after)
	if after.Mode != view.ModeLayout {
		t.Fatalf("attach answered %+v", after)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case v := <-got:
			if v.Mode == view.ModeLayout && v.Focus == id {
				// The session ending takes it out of the view.
				call(t, c, proto.MethodSessionStop, proto.SessionIDParams{ID: id}, nil)
				eventually(t, "the view to drop the ended session", func() bool {
					v, err := viewNow(p)
					return err == nil && v.Mode == view.ModeDashboard
				})
				return
			}
		case <-deadline:
			t.Fatal("b never saw the attach")
		}
	}
}

// viewNow is main as a fresh subscriber sees it.
func viewNow(p Paths) (view.View, error) {
	s, v, err := SubscribeView(p, proto.ViewSubscribeParams{Cols: 80, Rows: 24})
	if err != nil {
		return v, err
	}
	s.Close()
	return v, nil
}

// FuzzViewActions runs arbitrary view.* calls, and arbitrary views.json
// content, through the views: nothing may panic, and every view stays
// valid whatever clients send.
func FuzzViewActions(f *testing.F) {
	f.Add([]byte(`{"method":"view.attach","params":{"session":"s-1"}}
{"method":"view.split","params":{"side":true}}
{"method":"view.resize","params":{"side":true,"cells":-40}}
{"method":"view.focus","params":{"dx":1}}
{"method":"view.zoom"}
{"method":"view.input","params":{"session":"s-2"}}
{"method":"view.close"}`), []byte(`{"version":1,"views":[{"name":"main","mode":"layout","root":{"side":true,"ratio":0.3,"a":{"session":"s-1"},"b":{"session":"s-2"}},"focus":"s-2"}]}`))
	f.Add([]byte(`{"method":"view.size","params":{"cols":1,"rows":1,"resize":true}}
{"method":"view.sidebar","params":{"sidebar":{"width":-5,"slim":true}}}
{"method":"view.even"}`), []byte(`{"views":[{"name":"main","root":{"a":{"session":"x"}}}]}`))
	f.Fuzz(func(t *testing.T, calls, file []byte) {
		h := newFakeHost("s-1", "s-2", "s-3")
		vs := newViews(h, "", nil)
		vs.loadBytes(file)
		m, _, _, err := vs.subscribe(proto.ViewSubscribeParams{Cols: 50, Rows: 12})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(calls, []byte("\n")) {
			var req struct {
				Method string           `json:"method"`
				Params proto.ViewParams `json:"params"`
			}
			if json.Unmarshal(line, &req) != nil {
				continue
			}
			req.Params.Client = m.id
			v, _ := vs.do(req.Method, req.Params)
			if err := v.Valid(); err != nil {
				t.Fatalf("%s left %v", line, err)
			}
			v.Lay(int(v.Cols), int(v.Rows))
		}
	})
}
