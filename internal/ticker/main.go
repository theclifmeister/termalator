package ticker

// Keeping up with the default branch (docs/SPEC.md §7.5): the user's
// own checkouts are fast-forwarded when that is safe, and threads whose
// open PR falls behind or conflicts after main moves are told, once per
// head.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/codehost"
	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/thread"
	"github.com/theclifmeister/terminatr/internal/worktree"
)

func (t *Ticker) projectMemo(slug string) *projectMemo {
	pm := t.st.Projects[slug]
	if pm == nil {
		pm = &projectMemo{}
		t.st.Projects[slug] = pm
	}
	return pm
}

// repos are a project's repos and its unresolved threads' repos, cleaned
// and sorted, once each.
func repos(p *project.Project) []string {
	set := map[string]bool{}
	for _, r := range p.Meta.Repos {
		set[filepath.Clean(r)] = true
	}
	if recs, err := thread.List(p); err == nil {
		for _, r := range recs {
			if r.Repo != "" && r.State != thread.Resolved {
				set[filepath.Clean(r.Repo)] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// syncRepos fetches each repo of the project every pr_poll_seconds, and at once
// after one of its PRs merged, and fast-forwards the user's checkout when
// fast_forward_checkout allows and it is safe (worktree.Sync). Each
// fast-forward is journaled as caller ticker. It reports whether it
// synced.
func (t *Ticker) syncRepos(p *project.Project, safety config.Safety, now time.Time, merged bool) bool {
	pm := t.projectMemo(p.Slug)
	if !merged && now.Sub(pm.Synced) < t.prPoll(safety) {
		return false
	}
	pm.Synced = now
	list := repos(p)
	old := pm.Repos
	pm.Repos = map[string]*repoMemo{}
	for _, repo := range list {
		delete(t.hosts, repo) // origin may have changed: pick again
		c, err := t.o.Sync(repo, safety.FastForwardCheckout)
		if err != nil {
			t.o.Log.Printf("ticker: %s: sync %s: %v", p.Slug, repo, err)
		}
		if c.Branch == "" || c.Origin == "" {
			// No origin, or the fetch failed: keep what the last sync saw.
			if prev := old[repo]; prev != nil && err != nil {
				pm.Repos[repo] = prev
			}
			continue
		}
		rm := &repoMemo{Checkout: c}
		if prev := old[repo]; prev != nil && prev.Origin == c.Origin {
			rm.MergedPR = prev.MergedPR
		} else {
			rm.MergedPR = t.host(p, repo).MergedPR(repo, c.Origin)
		}
		pm.Repos[repo] = rm
		if c.From != "" {
			detail := fmt.Sprintf("%s %s..%s", c.Branch, short(c.From), short(c.To))
			if err := p.Journal(caller.Caller{Kind: caller.Ticker}, "repo.fast-forward", repo, detail); err != nil {
				t.o.Log.Printf("ticker: %s: journal: %v", p.Slug, err)
			}
			t.o.Log.Printf("ticker: %s: fast-forwarded %s %s", p.Slug, repo, detail)
		}
	}
	return true
}

func short(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}

// followMain checks each open thread PR against its repo's default
// branch head once per head: one that conflicts gets a fixed prompt when
// pr_followup is on and its session runs, and a pr-conflict item for the
// coordinator. One that is merely behind is left alone (T77). A head
// that main already has is left alone, and the PR is asked about again
// right before it is judged conflicting: main may have moved by merging it.
func (t *Ticker) followMain(p *project.Project, sessions []proto.SessionInfo, safety config.Safety, now time.Time) {
	pm := t.projectMemo(p.Slug)
	if len(pm.Repos) == 0 {
		return
	}
	recs, err := thread.List(p)
	if err != nil {
		return
	}
	for _, r := range recs {
		m := t.st.Threads[p.Slug+"/"+r.ID]
		if r.State == thread.Resolved || r.Repo == "" || m == nil || m.PR.State != "OPEN" {
			continue
		}
		rm := pm.Repos[filepath.Clean(r.Repo)]
		if rm == nil || m.MainSeen == rm.Origin || (m.PR.Base != "" && m.PR.Base != rm.Branch) {
			continue
		}
		state, ok := headState(r.Repo, m.PR, rm.Origin)
		if !ok {
			continue // GitHub hasn't worked it out yet: ask again later
		}
		if state != "conflict" {
			// Behind main is no reason to merge it (PRs need not be
			// up to date to merge, and each merge is a push and a CI run).
			m.MainSeen = rm.Origin
			continue
		}
		_, _, info, live := liveState(r, sessions)
		// What the last poll said may predate main's move.
		if _, ok := t.refreshPR(p, r, m, info, live, safety, now); !ok {
			continue // gh can't tell now: ask again on the next sweep
		}
		if m.PR.State != "OPEN" || (m.PR.Base != "" && m.PR.Base != rm.Branch) {
			continue
		}
		if state, ok = headState(r.Repo, m.PR, rm.Origin); !ok {
			continue
		}
		m.MainSeen = rm.Origin
		if state != "conflict" {
			continue
		}
		moved := rm.Branch + " moved to " + short(rm.Origin)
		if rm.MergedPR > 0 {
			moved += " (#" + strconv.Itoa(rm.MergedPR) + " merged)"
		}
		ref := m.PR.Ref()
		prompted := false
		if safety.PRFollowup && live {
			text := fmt.Sprintf("[tm] %s, and your %s conflicts with it. Merge origin/%s into your branch (no rebase, no force-push), fix the conflicts, rerun the tests, push, and hand in your report again with tm report once CI is green. (Generated by tm.)",
				moved, ref, rm.Branch)
			if err := t.promptPR(p, r, info, m.PR.Number, text); err != nil {
				t.o.Log.Printf("ticker: %s: main follow-up for %s: %v", p.Slug, r.ID, err)
			} else {
				prompted = true
			}
		}
		after := "; the thread was asked to merge it"
		if !prompted {
			after = "; the thread was not prompted (tm thread restart " + r.ID + ", or tell it)"
		}
		t.item(p, KindPRConflict, r.ID, fmt.Sprintf("%s of %s conflicts with %s at %s%s", ref, r.ID, rm.Branch, short(rm.Origin), after), false)
	}
}

// promptPR sends a PR-related prompt to a thread's session. It can wait
// in the queue (a busy thread, a held prompt box), so right before it is
// delivered the PR is asked about again: one that is merged or closed by
// then gets nothing, and the drop is journaled. A PR gh can't answer
// for is taken as still open.
func (t *Ticker) promptPR(p *project.Project, r *thread.Record, info proto.SessionInfo, number int, text string) error {
	repo, id := r.Repo, r.ID
	host := t.host(p, repo)
	refresh := func() (string, bool) {
		pr, err := host.PR(repo, codehost.Ref{Number: number})
		if err != nil || pr.Number != number || pr.State == "OPEN" {
			return text, true
		}
		detail := fmt.Sprintf("%s: #%d is %s", id, number, strings.ToLower(pr.State))
		if err := p.Journal(caller.Caller{Kind: caller.Ticker}, "prompt.dropped", id, detail); err != nil {
			t.o.Log.Printf("ticker: %s: journal: %v", p.Slug, err)
		}
		t.o.Log.Printf("ticker: %s: dropped a queued prompt for %s: %s", p.Slug, id, detail)
		return "", false
	}
	return t.o.Host.PromptFresh(info.ID, text, refresh)
}

// headState is how pr's head stands against base (worktree.HeadState),
// by git when repo has both commits, else by GitHub's word; ok is false
// while GitHub hasn't worked it out.
func headState(repo string, pr PR, base string) (state string, ok bool) {
	if state, ok := worktree.HeadState(repo, pr.Head, base); ok {
		return state, true
	}
	switch {
	case pr.Mergeable == "CONFLICTING" || pr.MergeState == "DIRTY":
		return "conflict", true
	case pr.MergeState == "BEHIND":
		return "behind", true
	case pr.Mergeable == "MERGEABLE":
		return "", true
	}
	return "", false
}

// Seen is what the ticker last saw for project slug, from its state
// file at path, for project.Context: PR state lines and checkout notes.
func Seen(path, slug string) project.Ticked {
	return project.Ticked{PRs: Summaries(path, slug), Checkouts: Checkouts(path, slug)}
}

// Checkouts are the notes on project slug's checkouts that are behind
// origin (worktree.Checkout's Note), by repo path, from the state file
// at path. A missing file gives none.
func Checkouts(path, slug string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var st state
	if json.Unmarshal(b, &st) != nil {
		return out
	}
	pm := st.Projects[slug]
	if pm == nil {
		return out
	}
	for repo, rm := range pm.Repos {
		if rm == nil {
			continue
		}
		// The file is read back: only a well-formed branch and one of the
		// fixed reasons go into the note.
		c := rm.Checkout
		if !worktree.ValidBranch(c.Branch) || !heldWords[c.Held] {
			continue
		}
		if n := c.Note(); n != "" {
			out[repo] = n
		}
	}
	return out
}

var heldWords = map[string]bool{"": true, worktree.HeldOff: true, worktree.HeldOther: true,
	worktree.HeldDirty: true, worktree.HeldDiverged: true, worktree.HeldRefused: true}
