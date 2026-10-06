package proto

// Watching a project (docs/SPEC.md §3.3, Watch): project.watch answers
// what the project's dashboard shows as one ProjectWatch, then turns its
// control connection into a stream of project.changed events, one for
// every change, until the client hangs up. It is what `tm watch
// --project <slug> --json` prints, for the coordinator's /tm pane.
const (
	// MethodProjectWatch watches a project (ProjectWatchParams) and
	// answers a ProjectWatch; ProjectWatchEvent lines follow.
	MethodProjectWatch = "project.watch"
	// EventProjectChanged carries the project's new state.
	EventProjectChanged = "project.changed"
)

// ProjectWatchParams are the params of project.watch.
type ProjectWatchParams struct {
	Project string `json:"project"`
}

// ProjectWatch is a project as the /tm pane shows it: what waits for the
// user first, then the inbox, the running threads and the tasks on deck.
type ProjectWatch struct {
	Project string `json:"project"`
	// NeedsYou is what waits for the user, most pressing first
	// (Why*): held prompt queues, tasks to accept, threads' questions,
	// red CI, blocked tasks.
	NeedsYou []WatchNeed `json:"needs_you"`
	// Inbox is the coordinator's unhandled inbox, oldest first, an item
	// kind of a subject once (project.Rows).
	Inbox []WatchItem `json:"inbox"`
	// Threads are the project's unresolved threads, in id order.
	Threads []WatchThread `json:"threads"`
	// Ready are the tasks on deck (open or ready), which the user may
	// delegate, in board order.
	Ready []WatchTodo `json:"ready"`
}

// Why a WatchNeed waits for the user, in the order they are listed.
const (
	// WhyQueue: a session's queued prompts are held while its agent
	// idles (SessionInfo.QueueNote); a coordinator's nudges wait behind
	// them, so it hears of nothing meanwhile (T82).
	WhyQueue    = "queue"
	WhyReview   = "review"   // a task in review: accept, send back, merge
	WhyQuestion = "question" // a thread asks the user something
	WhyCI       = "ci"       // a thread's PR has failing checks or conflicts
	WhyBlocked  = "blocked"  // a blocked task with no question from a thread
)

// WatchNeed is one thing that waits for the user.
type WatchNeed struct {
	Why    string `json:"why"`
	Task   string `json:"task,omitempty"` // "T12"
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	Thread string `json:"thread,omitempty"`
	// Session is the session whose queue is held (WhyQueue).
	Session string `json:"session,omitempty"`
	// Question is the thread's question (tm status --needs-you, or the
	// question menu open in its agent).
	Question string `json:"question,omitempty"`
	// PR is the thread's pull request in the ticker's words, PRURL its
	// link, PRNumber its number; Mergeable says it is open with no
	// failing checks, conflicts or requested changes.
	PR        string `json:"pr,omitempty"`
	PRURL     string `json:"pr_url,omitempty"`
	PRNumber  int    `json:"pr_number,omitempty"`
	Mergeable bool   `json:"mergeable,omitempty"`
	// Asked is the kind of the inbox item that already asks the
	// coordinator about the task (delegate, accept, send-back).
	Asked string `json:"asked,omitempty"`
}

// WatchItem is one unhandled inbox item.
type WatchItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Summary   string `json:"summary"`
	NeedsUser bool   `json:"needs_user,omitempty"`
	// Count is how many unhandled items of this kind the subject has
	// (the item is the latest); Task, What and Title are the summary
	// taken apart (project.Parts), for a row.
	Count int    `json:"count"`
	Task  string `json:"task,omitempty"`
	What  string `json:"what"`
	Title string `json:"title,omitempty"`
}

// WatchThread is one thread, compact.
type WatchThread struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Session is the session running it, "" when none does; State and
	// Reason are that session's agent state.
	Session  string     `json:"session,omitempty"`
	State    string     `json:"state,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	NeedsYou string     `json:"needs_you,omitempty"`
	Task     *WatchTask `json:"task,omitempty"`
	PR       string     `json:"pr,omitempty"`
	PRURL    string     `json:"pr_url,omitempty"`
	// PRBad says the PR needs acting on: failing checks, conflicts,
	// requested changes or behind its base.
	PRBad   bool `json:"pr_bad,omitempty"`
	Reports int  `json:"reports,omitempty"`
	Done    bool `json:"done,omitempty"`
}

// WatchTodo is a task on deck.
type WatchTodo struct {
	Task   string `json:"task"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Asked  string `json:"asked,omitempty"`
}

// ProjectWatchEvent is one line of a project watch after the answer.
type ProjectWatchEvent struct {
	Event string       `json:"event"` // EventProjectChanged
	Watch ProjectWatch `json:"watch"`
}
