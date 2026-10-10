package proto

import "github.com/theclifmeister/terminatr/internal/view"

// Views (docs/SPEC.md §3.3, Views): the server keeps what consoles show.
// A console joins a view with view.subscribe, which turns its control
// connection into a stream of view.changed events, and changes it with
// the other view.* methods on a second connection. Each of those returns
// the view as it is afterwards.
const (
	// MethodViewSubscribe joins a view (ViewSubscribeParams) and answers
	// ViewSubscribeResult; ViewEvent lines follow on the connection until
	// it closes, which leaves the view.
	MethodViewSubscribe = "view.subscribe"
	// MethodViewAttach shows a session (Session; Project becomes the
	// current project).
	MethodViewAttach = "view.attach"
	// MethodViewDashboard shows the dashboard.
	MethodViewDashboard = "view.dashboard"
	// MethodViewProject shows Project's dashboard: the dashboard, with
	// Project current (a click on its row in the sidebar's tree).
	MethodViewProject = "view.project"
	// MethodViewSelect selects a dashboard row (Key).
	MethodViewSelect = "view.select"
	// MethodViewSideSel moves the sidebar's keyboard row to Key.
	MethodViewSideSel = "view.sidesel"
	// MethodViewExpand expands (On) or collapses an inactive Project in
	// the sidebar's tree, without activating it (since protocol 10).
	MethodViewExpand = "view.expand"
	// MethodViewSidebar sets the projects sidebar (Sidebar).
	MethodViewSidebar = "view.sidebar"
	// MethodViewInfo sets the info panel beside a thread's pane (Info).
	MethodViewInfo = "view.info"
	// MethodViewSize reports the client's window (Cols, Rows). With
	// Resize, the user really resized it: the client becomes the view's
	// latest and the panes follow.
	MethodViewSize = "view.size"
	// MethodViewInput says the user typed into Session in this console:
	// the console you type in sizes the panes it shows.
	MethodViewInput = "view.input"
	// MethodViewDigest (ViewDigestParams) asks the consoles of process
	// PID to check their mirror against the server's screen: each gets a
	// view.digest event and asks its pane for an in-stream digest. The
	// e2e tests use it; it answers ViewDigestResult.
	MethodViewDigest = "view.digest"
)

// ViewDigestParams are the params of view.digest.
type ViewDigestParams struct {
	PID int `json:"pid"`
}

// ViewDigestResult is the answer to view.digest: how many consoles were
// asked.
type ViewDigestResult struct {
	Consoles int `json:"consoles"`
}

// ViewSubscribeParams are the params of view.subscribe.
type ViewSubscribeParams struct {
	// View names the view; empty is view.Main. Own makes a new view of
	// this console's own, gone when it leaves; Bare (with Own) makes it a
	// view without a dashboard (tm attach), StatusBar gives it the status
	// bar.
	View      string `json:"view,omitempty"`
	Own       bool   `json:"own,omitempty"`
	Bare      bool   `json:"bare,omitempty"`
	StatusBar bool   `json:"status_bar,omitempty"`
	// Session, when set, is shown once joined, as view.attach would.
	Session string `json:"session,omitempty"`
	// Cols and Rows are the client's window. Joining never resizes a
	// session.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	// Sidebar is the sidebar of a view created by this call (from the
	// client's ui.json); an existing view keeps its own.
	Sidebar *view.Sidebar `json:"sidebar,omitempty"`
	// Info is the info panel of a view created by this call (from the
	// client's ui.json), as Sidebar.
	Info *view.Info `json:"info,omitempty"`
}

// ViewSubscribeResult is the answer to view.subscribe: the client's id in
// the view, for the other view.* calls, and the view.
type ViewSubscribeResult struct {
	Client string    `json:"client"`
	View   view.View `json:"view"`
}

// ViewEvent is one line of a view subscription.
type ViewEvent struct {
	Event string    `json:"event"` // EventViewChanged, EventViewDigest
	View  view.View `json:"view"`
}

const (
	// EventViewChanged carries a view's new version.
	EventViewChanged = "view.changed"
	// EventViewDigest (no view) asks the console for a digest check
	// (view.digest).
	EventViewDigest = "view.digest"
)

// ViewParams are the params of every view.* action. Client is the id
// view.subscribe gave; the other fields are each action's own.
type ViewParams struct {
	Client  string        `json:"client"`
	Session string        `json:"session,omitempty"`
	Project string        `json:"project,omitempty"`
	Key     string        `json:"key,omitempty"`
	Sidebar *view.Sidebar `json:"sidebar,omitempty"`
	Info    *view.Info    `json:"info,omitempty"`
	Cols    uint16        `json:"cols,omitempty"`
	Rows    uint16        `json:"rows,omitempty"`
	Resize  bool          `json:"resize,omitempty"`
	// On expands the project (view.expand); false collapses it.
	On bool `json:"on,omitempty"`
}
