package thread

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/mdfile"
	"github.com/theclifmeister/termilator/internal/project"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// States of a thread record. The live agent state (working, blocked, …)
// comes from the server; this is the thread's own lifecycle.
const (
	Running  = "running"  // a session was started and not stopped by tm
	Stopped  = "stopped"  // tm thread stop; restart brings it back
	Resolved = "resolved" // tm thread resolve; final
)

// Record is threads/<id>/thread.toml (docs/SPEC.md §5.1). Only tm writes
// it, always under its lock.
type Record struct {
	ID       string `toml:"id" json:"id"`
	Title    string `toml:"title" json:"title"`
	Task     string `toml:"task,omitempty" json:"task,omitempty"` // "T12"
	Agent    string `toml:"agent" json:"agent"`
	Repo     string `toml:"repo,omitempty" json:"repo,omitempty"`
	Base     string `toml:"base,omitempty" json:"base,omitempty"`
	Branch   string `toml:"branch,omitempty" json:"branch,omitempty"`
	Worktree string `toml:"worktree" json:"worktree"`
	State    string `toml:"state" json:"state"`
	// Session is the termilator session running the thread, if any.
	Session string `toml:"session,omitempty" json:"session,omitempty"`
	// AgentSID is the agent's latest own session id, for resume (§8.5);
	// Prompted says whether it ever worked on a prompt (Claude can't
	// resume a session that never did).
	AgentSID string    `toml:"agent_session_id,omitempty" json:"agent_session_id,omitempty"`
	Prompted bool      `toml:"prompted,omitempty" json:"prompted,omitempty"`
	Created  time.Time `toml:"created" json:"created"`
	// LastPrompt is when the thread was last given work (start, prompt,
	// restart); tm done needs a report stored after it.
	LastPrompt time.Time `toml:"last_prompt" json:"last_prompt"`
	Reports    int       `toml:"reports" json:"reports"`
	ReportAt   time.Time `toml:"report_at,omitempty" json:"report_at,omitzero"`
	ReportAck  int       `toml:"report_acked" json:"report_acked"`
	Done       bool      `toml:"done,omitempty" json:"done,omitempty"`
	DoneAt     time.Time `toml:"done_at,omitempty" json:"done_at,omitzero"`
	// Adopted: the thread was a running agent session made a thread by
	// tm thread adopt, not started by tm (docs/SPEC.md §9, Adopt).
	// Checkout: its worktree is a repository's main checkout, which
	// resolve never removes, nor deletes its branch.
	Adopted  bool `toml:"adopted,omitempty" json:"adopted,omitempty"`
	Checkout bool `toml:"checkout,omitempty" json:"checkout,omitempty"`
}

// ReportState is "none", "new" (unacknowledged) or "acked".
func (r *Record) ReportState() string {
	switch {
	case r.Reports == 0:
		return "none"
	case r.ReportAck < r.Reports:
		return "new"
	}
	return "acked"
}

// TaskID is the linked task's number, 0 when there is none.
func (r *Record) TaskID() int {
	id, _ := tasks.ParseRef(r.Task)
	return id
}

// Error is a refusal with a stable code (exit 1).
type Error = tasks.Error

func refuse(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

var idRE = regexp.MustCompile(`^t-[0-9]{4,}$`)

// ValidID reports whether s looks like a thread id ("t-0003").
func ValidID(s string) bool { return idRE.MatchString(s) }

// Dir is threads/<id>/ of a project.
func Dir(p *project.Project, id string) string { return p.Path("threads", id) }

// Path joins names onto a thread's folder.
func Path(p *project.Project, id string, elem ...string) string {
	return filepath.Join(append([]string{Dir(p, id)}, elem...)...)
}

func recordPath(p *project.Project, id string) string { return Path(p, id, "thread.toml") }

// Load reads one thread's record.
func Load(p *project.Project, id string) (*Record, error) {
	if !ValidID(id) {
		return nil, refuse("unknown-thread", "%q is not a thread id like t-0003", id)
	}
	data, err := os.ReadFile(recordPath(p, id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("unknown-thread", "no thread %s in project %s (tm thread list)", id, p.Slug)
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if _, err := toml.Decode(string(data), &r); err != nil {
		return nil, fmt.Errorf("%s: %w", recordPath(p, id), err)
	}
	return &r, nil
}

// List reads every thread record of a project, by id.
func List(p *project.Project) ([]*Record, error) {
	entries, err := os.ReadDir(p.Path("threads"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		r, err := Load(p, e.Name())
		if err != nil {
			var te *Error
			if errors.As(err, &te) {
				continue // a folder without a record yet
			}
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Update is a locked read-modify-write of a thread's record.
func Update(p *project.Project, id string, fn func(r *Record) error) (*Record, error) {
	var out Record
	err := mdfile.Update(recordPath(p, id), func(old []byte) ([]byte, error) {
		if old == nil {
			return nil, refuse("unknown-thread", "no thread %s in project %s", id, p.Slug)
		}
		var r Record
		if _, err := toml.Decode(string(old), &r); err != nil {
			return nil, err
		}
		if err := fn(&r); err != nil {
			return nil, err
		}
		out = r
		return encode(&r)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func encode(r *Record) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# Written by tm; don't edit.\n")
	if err := toml.NewEncoder(&b).Encode(r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Create allocates the next thread id, creates threads/<id>/ and writes
// rec with that id. Ids are never reused.
func Create(p *project.Project, rec Record) (*Record, error) {
	if err := os.MkdirAll(p.Path("threads"), 0o755); err != nil {
		return nil, err
	}
	unlock, err := mdfile.Lock(p.Path("threads", "ids"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	entries, err := os.ReadDir(p.Path("threads"))
	if err != nil {
		return nil, err
	}
	next := 1
	for _, e := range entries {
		if n, ok := strings.CutPrefix(e.Name(), "t-"); ok && e.IsDir() {
			if v, err := strconv.Atoi(n); err == nil && v >= next {
				next = v + 1
			}
		}
	}
	rec.ID = fmt.Sprintf("t-%04d", next)
	if err := os.Mkdir(Dir(p, rec.ID), 0o755); err != nil {
		return nil, err
	}
	data, err := encode(&rec)
	if err != nil {
		return nil, err
	}
	if err := mdfile.WriteAtomic(recordPath(p, rec.ID), data, 0o644); err != nil {
		return nil, err
	}
	return &rec, nil
}

// WorktreeDir is ~/.termilator/worktrees/<slug>/<id>-<title-slug> and
// BranchName tm/<slug>/<id>-<title-slug> (docs/SPEC.md §9).
func WorktreeDir(slug, id, title string) (string, error) {
	root, err := home.WorktreesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, slug, id+titleSuffix(title)), nil
}

// BranchName is the thread's branch.
func BranchName(slug, id, title string) string { return "tm/" + slug + "/" + id + titleSuffix(title) }

func titleSuffix(title string) string {
	s := project.Slugify(title)
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return ""
	}
	return "-" + s
}
