package proto

import "github.com/theclifmeister/termalator/internal/view"

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
	// MethodViewExpand opens (Expand) or closes Project in the sidebar's
	// tree.
	MethodViewExpand = "view.expand"
	// MethodViewSelect selects a dashboard row (Key).
	MethodViewSelect = "view.select"
	// MethodViewSideSel moves the sidebar's keyboard row to Key.
	MethodViewSideSel = "view.sidesel"
	// MethodViewSplit starts a shell in the focused pane's directory and
	// shows it beside (Side) or below the focused pane.
	MethodViewSplit = "view.split"
	// MethodViewClose closes the pane of Session (the focused one when
	// empty); its session keeps running.
	MethodViewClose = "view.close"
	// MethodViewFocus focuses Session, the next pane (Next) or the pane
	// in direction (DX, DY).
	MethodViewFocus = "view.focus"
	// MethodViewZoom zooms the focused pane, and back.
	MethodViewZoom = "view.zoom"
	// MethodViewEven switches between all panes side by side and all
	// stacked.
	MethodViewEven = "view.even"
	// MethodViewResize moves the divider nearest the focused pane by
	// Cells, on the side-by-side axis when Side.
	MethodViewResize = "view.resize"
	// MethodViewDrag moves the divider through cell (X, Y) of the view's
	// layout to column or row To: the mouse dragging it.
	MethodViewDrag = "view.drag"
	// MethodViewSidebar sets the projects sidebar (Sidebar).
	MethodViewSidebar = "view.sidebar"
	// MethodViewSize reports the client's window (Cols, Rows). With
	// Resize, the user really resized it: the client becomes the view's
	// latest and the panes follow.
	MethodViewSize = "view.size"
	// MethodViewInput says the user typed into Session in this console:
	// the console you type in sizes the panes it shows.
	MethodViewInput = "view.input"
)

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
}

// ViewSubscribeResult is the answer to view.subscribe: the client's id in
// the view, for the other view.* calls, and the view.
type ViewSubscribeResult struct {
	Client string    `json:"client"`
	View   view.View `json:"view"`
}

// ViewEvent is one line of a view subscription.
type ViewEvent struct {
	Event string    `json:"event"` // EventViewChanged
	View  view.View `json:"view"`
}

// EventViewChanged carries a view's new version.
const EventViewChanged = "view.changed"

// ViewParams are the params of every view.* action. Client is the id
// view.subscribe gave; the other fields are each action's own.
type ViewParams struct {
	Client  string        `json:"client"`
	Session string        `json:"session,omitempty"`
	Project string        `json:"project,omitempty"`
	Key     string        `json:"key,omitempty"`
	Side    bool          `json:"side,omitempty"`
	Next    bool          `json:"next,omitempty"`
	DX      int           `json:"dx,omitempty"`
	DY      int           `json:"dy,omitempty"`
	Cells   int           `json:"cells,omitempty"`
	Sidebar *view.Sidebar `json:"sidebar,omitempty"`
	Cols    uint16        `json:"cols,omitempty"`
	Rows    uint16        `json:"rows,omitempty"`
	Resize  bool          `json:"resize,omitempty"`
	Expand  bool          `json:"expand,omitempty"`
	X       int           `json:"x,omitempty"`
	Y       int           `json:"y,omitempty"`
	To      int           `json:"to,omitempty"`
}
