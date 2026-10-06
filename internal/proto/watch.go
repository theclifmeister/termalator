package proto

// Watching a session (docs/SPEC.md §3.3, Watch): session.watch answers
// the session's state as one Watch, then turns its control connection
// into a stream of watch.changed events, one for every new state, until
// the client hangs up or the session ends. It is what `tm watch --json`
// prints, for mods that would otherwise poll session.list and the
// project files.
const (
	// MethodSessionWatch watches a session (SessionIDParams) and answers
	// a Watch; WatchEvent lines follow.
	MethodSessionWatch = "session.watch"
	// EventWatchChanged carries the session's new state.
	EventWatchChanged = "watch.changed"
)

// Watch is a session's state as a mod shows it: the session, its
// thread's task and PR, and its project's waiting work.
type Watch struct {
	Session WatchSession `json:"session"`
	// Task is the thread's task; nil for a session that is no thread's,
	// or a thread without a task.
	Task *WatchTask `json:"task"`
	// PR is the thread's pull request in the ticker's words ("#12 open,
	// checks pass, approved"); "" before the ticker has seen one. PRURL
	// is its link, from the ticker or the thread's latest report.
	PR    string `json:"pr"`
	PRURL string `json:"pr_url,omitempty"`
	// NeedsYou counts the project's tasks in the board's Needs you group
	// (review or blocked); Inbox its unhandled inbox items.
	NeedsYou int `json:"needs_you"`
	Inbox    int `json:"inbox"`
	// Queued counts the prompts waiting to be pasted into the session.
	Queued int `json:"queued_prompts"`
}

// WatchSession is the session part of a Watch.
type WatchSession struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Agent   string `json:"agent,omitempty"`
	Project string `json:"project,omitempty"`
	Thread  string `json:"thread,omitempty"`
	// State and Reason are the agent state (§8.4); State is "exited" in
	// the last line, sent when the session has ended.
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	// NeedsYou is the thread's question to the user (tm status
	// --needs-you), when it waits for one.
	NeedsYou string `json:"needs_you,omitempty"`
}

// WatchTask is the task part of a Watch.
type WatchTask struct {
	ID         string `json:"id"` // "T12"
	Title      string `json:"title"`
	Status     string `json:"status"`
	StepsDone  int    `json:"steps_done"`
	StepsTotal int    `json:"steps_total"`
	// Current is the thread's current item: its in-progress todo.
	Current string `json:"current,omitempty"`
}

// WatchEvent is one line of a watch after the answer.
type WatchEvent struct {
	Event string `json:"event"` // EventWatchChanged
	Watch Watch  `json:"watch"`
}
