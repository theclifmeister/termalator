package ticker

// Completing tasks by the project's setting (docs/SPEC.md §6.4, §7.5):
// with complete_tasks "merged", a task in review is marked done once its
// thread's PR merged. The setting is the user's standing acceptance; the
// coordinator gets a task-done item, and a send-back reopens the task.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// KindTaskDone: the ticker marked a task done by complete_tasks.
const KindTaskDone = "task-done"

// mergeMemo is a thread's merged PR: kept past the thread's resolve, so
// a task still in review later still finds the merge commit.
type mergeMemo struct {
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
	Merge string `json:"merge"`
}

// rememberMerge keeps thread r's PR merge commit for completeTasks.
func (t *Ticker) rememberMerge(p *project.Project, r *thread.Record, pr PR) {
	if pr.State != "MERGED" || !oidRE.MatchString(pr.Merge) || r.Repo == "" || pr.Number <= 0 {
		return
	}
	pm := t.projectMemo(p.Slug)
	if pm.Merges == nil {
		pm.Merges = map[string]*mergeMemo{}
	}
	pm.Merges[r.ID] = &mergeMemo{Repo: r.Repo, PR: pr.Number, Merge: pr.Merge}
}

// mergeOf is thread id's PR merge: as the ticker saw it, else from the
// PR in its report and its repo's history (a PR that merged before tm
// kept merges). nil when there is none.
func (t *Ticker) mergeOf(p *project.Project, pm *projectMemo, id string) *mergeMemo {
	if mm := pm.Merges[id]; mm != nil {
		return mm
	}
	if !thread.ValidID(id) {
		return nil
	}
	r, err := thread.Load(p, id)
	if err != nil || r.Repo == "" {
		return nil
	}
	rep, _ := thread.ReadReport(p, id)
	if rep == nil {
		return nil
	}
	n := PRNumber(rep.PR)
	if merge := t.host(p, r.Repo).MergeCommit(r.Repo, n); n > 0 && oidRE.MatchString(merge) {
		return &mergeMemo{Repo: r.Repo, PR: n, Merge: merge}
	}
	return nil
}

// PRNumber is the number at the end of a pull request's URL, 0 for none.
func PRNumber(url string) int {
	_, n, ok := strings.Cut(url, "/pull/")
	if !ok {
		return 0
	}
	v, err := strconv.Atoi(n)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// byUser: a task the user owns is theirs to accept.
func byUser(owner string) bool {
	return strings.EqualFold(strings.TrimSpace(owner), "me")
}

// completeTasks marks done each task in review whose thread's PR merged
// (complete_tasks "merged"), once per merge commit: a task the user sent back
// after that stays open until a new PR ships. Tasks without a PR, and
// tasks the user owns, are left to the user.
func (t *Ticker) completeTasks(p *project.Project, safety config.Safety) {
	pm := t.projectMemo(p.Slug)
	board, err := p.Tasks().Load()
	if err != nil {
		return
	}
	// Forget what no task still needs.
	live, refs := map[string]bool{}, map[string]bool{}
	for _, tk := range board.Tasks {
		refs[tk.Ref()] = true
		if tk.Status != tasks.Done && tk.Thread != "" {
			live[tk.Thread] = true
		}
	}
	for id := range pm.Merges {
		if !live[id] {
			delete(pm.Merges, id)
		}
	}
	for ref := range pm.Completed {
		if !refs[ref] {
			delete(pm.Completed, ref)
		}
	}
	if safety.CompleteTasks != config.CompleteMerged {
		return
	}
	for _, tk := range board.Tasks {
		if tk.Status != tasks.Review || tk.Thread == "" || byUser(tk.Owner) {
			continue
		}
		mm := t.mergeOf(p, pm, tk.Thread)
		if mm == nil || pm.Completed[tk.Ref()] == mm.Merge {
			continue
		}
		why := fmt.Sprintf("merged (PR #%d)", mm.PR)
		res, err := p.Tasks().CompleteBySetting(caller.Caller{Kind: caller.Ticker}, tk.ID, why)
		if err != nil {
			t.o.Log.Printf("ticker: %s: complete %s: %v", p.Slug, tk.Ref(), err)
			continue
		}
		if !res.Changed {
			continue
		}
		if pm.Completed == nil {
			pm.Completed = map[string]string{}
		}
		pm.Completed[tk.Ref()] = mm.Merge
		t.item(p, KindTaskDone, tk.Ref(), fmt.Sprintf("%s is done: %s, as the user's setting says (complete tasks when merged); the user can still send it back",
			thread.TaskLabel(p, tk.Ref()), why), false)
	}
}
