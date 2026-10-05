package ticker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/tasks"
)

var human = caller.Caller{Kind: caller.Human}

func (r *rig) setConfig(body string) {
	r.t.Helper()
	dir, err := home.Dir()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// addTask adds a task with status st on thread th, owned by owner.
func (r *rig) addTask(title, st, th, owner string) {
	r.t.Helper()
	res, err := r.p.Tasks().Add(human, []tasks.NewTask{{Title: title, Status: st, Owner: owner}})
	if err != nil || res[0].Result != "created" {
		r.t.Fatalf("%v %v", res, err)
	}
	if th != "" {
		id, _ := tasks.ParseRef(res[0].ID)
		if _, err := r.p.Tasks().SetThread(human, id, th); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) status(id int) tasks.Status {
	r.t.Helper()
	tk, err := r.p.Tasks().Get(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return tk.Status
}

func mergedPR(n int, merge string) string {
	return fmt.Sprintf(`{"number":%d,"url":"https://github.com/o/r/pull/%d","state":"MERGED","mergedAt":"2026-10-04T12:00:00Z","mergeCommit":{"oid":%q},"statusCheckRollup":[]}`, n, n, merge)
}

// TestCompleteTasksMergedItem: with complete_tasks "merged", a task in
// review is done once its thread's PR merged; journaled as the ticker,
// and the coordinator gets an item. Sent back and in review again, it
// isn't completed again for that merge.
func TestCompleteTasksMergedItem(t *testing.T) {
	repo, other := gitFixture(t)
	merge := push(t, other, "g", "two\n", "Merge pull request #7 from a/b")
	r := newRigIn(t, repo, []string{repo})
	r.setConfig("[projects.demo]\ncomplete_tasks = \"merged\"\n")
	r.addTask("Ship it", "review", "t-0001", "")
	r.gh = []string{mergedPR(7, merge)}
	r.sweep(0)
	if st := r.status(1); st != tasks.Done {
		t.Fatalf("merged, still %s", st)
	}
	if j := r.journal(); !strings.Contains(j, "ticker task.done T1 merged (PR #7)") {
		t.Fatalf("journal:\n%s", j)
	}
	if s := r.summaries(); !strings.Contains(s, "T1 Ship it is done: merged (PR #7), as the user's setting says (complete tasks when merged)") {
		t.Fatalf("items:\n%s", s)
	}
	if n := NudgeText(r.items()[len(r.items())-1:], nil); !strings.Contains(n, "T1 done by the user's setting") {
		t.Fatalf("nudge %q", n)
	}

	// Sent back (the coordinator reopens it), then in review again with
	// the same PR: left to the user.
	if _, err := r.p.Tasks().SetStatus(human, 1, tasks.Review, "sent back: still broken"); err != nil {
		t.Fatal(err)
	}
	r.sweep(2 * time.Minute)
	if st := r.status(1); st != tasks.Review {
		t.Fatalf("completed again for the same merge: %s", st)
	}
}

// TestCompleteTasksReleasedRemoved: the removed complete_tasks
// "released" reads as "user": a merged, tagged PR leaves its task in
// review.
func TestCompleteTasksReleasedRemoved(t *testing.T) {
	repo, other := gitFixture(t)
	merge := push(t, other, "g", "two\n", "Merge pull request #7 from a/b")
	gitT(t, other, "tag", "v0.5.0")
	gitT(t, other, "push", "-q", "origin", "v0.5.0")
	r := newRigIn(t, repo, []string{repo})
	r.setConfig("[projects.demo]\ncomplete_tasks = \"released\"\n")
	r.addTask("Ship it", "review", "t-0001", "")
	r.gh = []string{mergedPR(7, merge)}
	r.sweep(0)
	r.sweep(2 * time.Minute)
	if st := r.status(1); st != tasks.Review || strings.Contains(r.kinds(), KindTaskDone) {
		t.Fatalf("completed by the removed setting: %s, %s", st, r.kinds())
	}
}

// TestCompleteTasksMerged: "merged" completes a task in review once its
// thread's PR merged; tasks owned by the user, without a thread, or not
// in review stay; the default leaves everything to the user.
func TestCompleteTasksMerged(t *testing.T) {
	r := newRig(t)
	r.addTask("Ship it", "review", "t-0001", "")
	r.addTask("Mine", "review", "t-0001", "me")
	r.addTask("No thread", "review", "", "")
	r.addTask("Still going", "started", "t-0001", "")
	r.gh = []string{mergedPR(7, strings.Repeat("ab", 20))}
	r.sweep(0)
	if st := r.status(1); st != tasks.Review {
		t.Fatalf("completed by default: %s", st)
	}
	r.setConfig("[projects.demo]\ncomplete_tasks = \"merged\"\n")
	r.sweep(2 * time.Minute)
	for id, want := range map[int]tasks.Status{1: tasks.Done, 2: tasks.Review, 3: tasks.Review, 4: tasks.Started} {
		if st := r.status(id); st != want {
			t.Errorf("T%d %s, want %s", id, st, want)
		}
	}
	if j := r.journal(); !strings.Contains(j, "ticker task.done T1 merged (PR #7)") || strings.Count(j, "task.done") != 1 {
		t.Fatalf("journal:\n%s", j)
	}
}
