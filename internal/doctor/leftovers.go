package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/thread"
	"github.com/theclifmeister/termalator/internal/worktree"
)

// threadIndex is every thread record of every project.
type threadIndex struct {
	byKey  map[string]*thread.Record // "<slug>/<id>"
	active map[string]bool           // worktree paths of unresolved threads
	repos  []string
}

func loadThreads() (*threadIndex, []Check) {
	ix := &threadIndex{byKey: map[string]*thread.Record{}, active: map[string]bool{}}
	var warns []Check
	projects, err := project.List()
	if err != nil {
		return ix, []Check{{Group: "leftovers", Name: "projects", Status: Warn, Detail: err.Error()}}
	}
	repos := map[string]bool{}
	for _, s := range projects {
		if s.Error != "" {
			warns = append(warns, Check{Group: "leftovers", Name: s.Slug, Status: Warn, Detail: "project unreadable: " + s.Error})
			continue
		}
		for _, r := range s.Repos {
			repos[r] = true
		}
		p, err := project.Open(s.Slug)
		if err != nil {
			continue
		}
		recs, err := thread.List(p)
		if err != nil {
			warns = append(warns, Check{Group: "leftovers", Name: s.Slug, Status: Warn, Detail: "threads unreadable: " + err.Error()})
			continue
		}
		for _, r := range recs {
			ix.byKey[s.Slug+"/"+r.ID] = r
			if r.Repo != "" {
				repos[r.Repo] = true
			}
			if r.State != thread.Resolved && r.Worktree != "" {
				ix.active[filepath.Clean(r.Worktree)] = true
			}
		}
	}
	for r := range repos {
		ix.repos = append(ix.repos, r)
	}
	sort.Strings(ix.repos)
	return ix, warns
}

// threadID takes "t-0003" from "t-0003-fix-the-login".
func threadID(name string) string {
	if len(name) >= 6 && strings.HasPrefix(name, "t-") {
		id := name
		if i := strings.IndexByte(name[2:], '-'); i >= 0 {
			id = name[:2+i]
		}
		if thread.ValidID(id) {
			return id
		}
	}
	return ""
}

// why says why a thread no longer needs its worktree or branch.
func (ix *threadIndex) why(slug, name string) (unused bool, reason string) {
	id := threadID(name)
	if id == "" {
		return true, "not a thread's"
	}
	r, ok := ix.byKey[slug+"/"+id]
	switch {
	case !ok:
		return true, "thread " + id + " has no record in project " + slug
	case r.State == thread.Resolved:
		return true, "thread " + id + " is resolved"
	}
	return false, ""
}

// Leftovers finds worktrees under <home>/worktrees with no open thread,
// and tm/<slug>/… branches merged into the repo's default branch whose
// thread is resolved or gone.
func Leftovers(d Deps, _ Live) []Check {
	const g = "leftovers"
	ix, out := loadThreads()
	n := len(out)
	wts := leftoverWorktrees(d, ix)
	removable := map[string]bool{}
	for _, c := range wts {
		if c.Fix != nil {
			removable[realPath(c.path)] = true
		}
		out = append(out, c.Check)
	}
	out = append(out, leftoverBranches(d, ix, removable)...)
	if len(out) == n {
		out = append(out, Check{Group: g, Name: "worktrees and branches", Status: OK, Detail: "none left over"})
	}
	return out
}

// worktreeCheck is a worktree check and its folder.
type worktreeCheck struct {
	Check
	path string
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// checkedOut maps each branch checked out in a worktree of repo to that
// worktree's real path.
func checkedOut(d Deps, repo string) map[string]string {
	out := map[string]string{}
	list, err := d.Run(repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return out
	}
	dir := ""
	for _, l := range strings.Split(list, "\n") {
		if v, ok := strings.CutPrefix(l, "worktree "); ok {
			dir = v
		} else if v, ok := strings.CutPrefix(l, "branch refs/heads/"); ok {
			out[v] = realPath(dir)
		}
	}
	return out
}

func leftoverWorktrees(d Deps, ix *threadIndex) []worktreeCheck {
	const g = "leftovers"
	root := filepath.Join(d.Paths.Home, "worktrees")
	slugs, _ := os.ReadDir(root)
	var out []worktreeCheck
	for _, s := range slugs {
		if !s.IsDir() {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(root, s.Name()))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, s.Name(), e.Name())
			if ix.active[dir] {
				continue
			}
			_, reason := ix.why(s.Name(), e.Name())
			c := Check{Group: g, Name: "worktree", Status: Warn, Detail: dir + ": " + reason}
			common, err := d.Run(dir, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
			if err != nil {
				if empty(dir) {
					c.Fix = &Fix{Desc: "remove empty folder " + dir, Apply: func() error { return os.Remove(dir) }}
				} else {
					c.Detail += "; not a git worktree, kept (remove it by hand)"
				}
				out = append(out, worktreeCheck{c, dir})
				continue
			}
			repo := common
			if filepath.Base(common) == ".git" {
				repo = filepath.Dir(common)
			}
			if st, _ := d.Run(dir, "git", "status", "--porcelain"); st != "" {
				c.Detail += "; has uncommitted changes, kept"
				out = append(out, worktreeCheck{c, dir})
				continue
			}
			c.Fix = &Fix{Desc: "remove worktree " + dir, Apply: func() error {
				err := worktree.Remove(repo, dir)
				if errors.Is(err, worktree.ErrDirty) {
					return fmt.Errorf("kept %s: it has uncommitted changes now", dir)
				}
				return err
			}}
			out = append(out, worktreeCheck{c, dir})
		}
	}
	return out
}

func empty(dir string) bool {
	es, err := os.ReadDir(dir)
	return err == nil && len(es) == 0
}

// defaultBase is origin's default branch if known locally, else HEAD. No
// fetch: doctor stays offline.
func defaultBase(d Deps, repo string) string {
	if ref, err := d.Run(repo, "git", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return ref
	}
	if ref, err := d.Run(repo, "git", "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && ref != "" {
		return ref
	}
	return "HEAD"
}

func leftoverBranches(d Deps, ix *threadIndex, removable map[string]bool) []Check {
	const g = "leftovers"
	var out []Check
	for _, repo := range ix.repos {
		if _, err := os.Stat(repo); err != nil {
			continue
		}
		refs, err := d.Run(repo, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads/tm/")
		if err != nil || refs == "" {
			continue
		}
		base := defaultBase(d, repo)
		used := checkedOut(d, repo)
		for _, br := range strings.Split(refs, "\n") {
			parts := strings.SplitN(br, "/", 3)
			if len(parts) != 3 {
				continue
			}
			unused, reason := ix.why(parts[1], parts[2])
			if !unused {
				continue
			}
			if _, err := d.Run(repo, "git", "merge-base", "--is-ancestor", br, base); err != nil {
				out = append(out, Check{Group: g, Name: "branch", Status: OK,
					Detail: fmt.Sprintf("%s in %s: %s, but not merged into %s; kept (a squash-merged PR looks like this; git branch -D removes it)", br, repo, reason, base)})
				continue
			}
			if wt, ok := used[br]; ok && !removable[wt] {
				out = append(out, Check{Group: g, Name: "branch", Status: OK,
					Detail: fmt.Sprintf("%s in %s: %s and merged into %s, but checked out in %s; kept", br, repo, reason, base, wt)})
				continue
			}
			out = append(out, Check{Group: g, Name: "branch", Status: Warn,
				Detail: fmt.Sprintf("%s in %s: %s and merged into %s", br, repo, reason, base),
				Fix:    &Fix{Desc: "delete branch " + br + " in " + repo, Apply: func() error { return worktree.DeleteBranch(repo, br) }}})
		}
	}
	return out
}
