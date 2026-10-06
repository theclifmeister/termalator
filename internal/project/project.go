package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/mdfile"
	"github.com/theclifmeister/termilator/internal/tasks"
)

// Meta is PROJECT.md's front matter.
type Meta struct {
	Name    string    `toml:"name"`
	Goal    string    `toml:"goal"`
	Repos   []string  `toml:"repos"`
	Created time.Time `toml:"created"`
}

// Project is one project folder, ~/.termilator/projects/<slug>/.
type Project struct {
	Slug string
	Dir  string
	Meta Meta
	// Instructions is PROJECT.md's body: the standing instructions.
	Instructions string
}

// Error is a refusal with a stable code, like tasks.Error.
type Error = tasks.Error

func refuse(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidSlug reports whether s can name a project folder.
func ValidSlug(s string) bool { return len(s) <= 64 && slugRE.MatchString(s) }

// Slugify turns a project name into a slug: lower case, runs of other
// characters become one '-'.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

// Dir returns the folder of a project slug.
func Dir(slug string) (string, error) {
	if !ValidSlug(slug) {
		return "", refuse("invalid-project", "%q is not a project slug (a-z, 0-9 and -)", slug)
	}
	d, err := home.ProjectsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, slug), nil
}

// Open reads a project by slug.
func Open(slug string) (*Project, error) {
	dir, err := Dir(slug)
	if err != nil {
		return nil, err
	}
	p := &Project{Slug: slug, Dir: dir}
	body, err := mdfile.Read(p.Path("PROJECT.md"), &p.Meta)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("unknown-project", "no project %q (tm project list)", slug)
	}
	if err != nil {
		return nil, err
	}
	p.Instructions = strings.TrimSpace(string(body))
	return p, nil
}

// Path joins a name onto the project folder.
func (p *Project) Path(elem ...string) string {
	return filepath.Join(append([]string{p.Dir}, elem...)...)
}

// Tasks returns the task store for this project, journaling into it and
// raising confirmations in its inbox.
func (p *Project) Tasks() *tasks.Store {
	return &tasks.Store{Dir: p.Dir, Slug: p.Slug, Now: func() time.Time { return now() }, Events: events{p}}
}

// Options for New.
type Options struct {
	Name  string
	Goal  string
	Repos []string // absolute or relative paths of existing directories
	Now   time.Time
}

// New creates a project folder with the layout of §5.1. It refuses with
// project-exists if the slug is taken.
func New(o Options) (*Project, error) {
	name := strings.TrimSpace(o.Name)
	slug := Slugify(name)
	if slug == "" {
		return nil, refuse("invalid-name", "project name %q has no letters or digits", o.Name)
	}
	if strings.ContainsAny(o.Goal, "\r\n") {
		return nil, refuse("invalid-goal", "the goal must be one line")
	}
	var repos []string
	for _, r := range o.Repos {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return nil, refuse("invalid-repo", "%s is not a directory", abs)
		}
		repos = append(repos, abs)
	}
	dir, err := Dir(slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, refuse("project-exists", "project %q already exists at %s", slug, dir)
		}
		return nil, err
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	p := &Project{Slug: slug, Dir: dir, Meta: Meta{Name: name, Goal: strings.TrimSpace(o.Goal), Repos: repos, Created: now.UTC().Truncate(time.Second)}}
	p.Instructions = strings.TrimSpace(instructionsTemplate)
	if err := p.scaffold(); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	return p, nil
}

func (p *Project) scaffold() error {
	for _, d := range []string{"memory", "tasks", "inbox/done", "threads", "uploads"} {
		if err := os.MkdirAll(p.Path(d), 0o755); err != nil {
			return err
		}
	}
	project, err := mdfile.Join(p.Meta, []byte(instructionsTemplate))
	if err != nil {
		return err
	}
	if err := mdfile.WriteAtomic(p.Path("PROJECT.md"), project, 0o644); err != nil {
		return err
	}
	if err := p.WriteRoleFile(); err != nil {
		return err
	}
	files := map[string]string{
		"CONTEXT.md": contextTemplate,
		"MEMORY.md":  memoryTemplate,
		"JOURNAL.md": "# Journal\n\n",
		"TASKS.md":   string((&tasks.Board{NextID: 1}).Render()),
	}
	for name, body := range files {
		// A fresh folder has no other writers yet: no lock needed.
		if err := mdfile.WriteAtomic(p.Path(name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return p.Journal(caller.Caller{Kind: caller.Human}, "project.new", p.Slug, p.Meta.Name)
}

// WriteRoleFile (re)generates AGENTS.md, the coordinator's role file, and
// links CLAUDE.md to it (§5.2, §7.8).
func (p *Project) WriteRoleFile() error {
	if err := mdfile.WriteAtomic(p.Path("AGENTS.md"), []byte(roleFile(p)), 0o644); err != nil {
		return err
	}
	link := p.Path("CLAUDE.md")
	if target, err := os.Readlink(link); err == nil && target == "AGENTS.md" {
		return nil
	}
	os.Remove(link)
	return os.Symlink("AGENTS.md", link)
}

// Summary is one row of `tm project list`.
type Summary struct {
	Slug   string         `json:"slug"`
	Name   string         `json:"name"`
	Goal   string         `json:"goal"`
	Dir    string         `json:"dir"`
	Repos  []string       `json:"repos"`
	Counts map[string]int `json:"tasks"`
	Safety *config.Safety `json:"safety,omitempty"`
	// Own are the settings the project sets itself; it follows all
	// projects in the rest (config.Own).
	Own   []string `json:"own_settings,omitempty"`
	Error string   `json:"error,omitempty"`
}

// List returns every project, by slug. A project whose files can't be
// read is listed with its error instead of failing the whole list.
func List() ([]Summary, error) {
	root, err := home.ProjectsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if !e.IsDir() || !ValidSlug(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "PROJECT.md")); err != nil {
			continue
		}
		s := Summary{Slug: e.Name(), Dir: filepath.Join(root, e.Name())}
		p, err := Open(e.Name())
		if err != nil {
			s.Error = err.Error()
			out = append(out, s)
			continue
		}
		s.Name, s.Goal, s.Repos = p.Meta.Name, p.Meta.Goal, p.Meta.Repos
		if safety, err := cfg.Safety(p.Slug); err == nil {
			s.Safety = &safety
		}
		s.Own = cfg.Own(p.Slug)
		if b, err := p.Tasks().Load(); err != nil {
			s.Error = err.Error()
		} else {
			s.Counts = Counts(b)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// Counts tallies a board by group, keyed "needs_you", "in_motion",
// "on_deck" and "done".
func Counts(b *tasks.Board) map[string]int {
	c := map[string]int{"needs_you": 0, "in_motion": 0, "on_deck": 0, "done": 0}
	for _, t := range b.Tasks {
		c[GroupKey(tasks.GroupOf(t.Status))]++
	}
	return c
}

// GroupKey is a group's JSON key.
func GroupKey(g tasks.Group) string {
	return strings.ReplaceAll(strings.ToLower(string(g)), " ", "_")
}

// Resolve picks the project for a command (§6.3): the --project flag,
// then $TERMILATOR_PROJECT, then the project whose folder or thread
// worktree contains cwd. It returns "" when none applies.
func Resolve(flag string, getenv func(string) string, cwd string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if s := getenv(caller.EnvProject); s != "" {
		return s, nil
	}
	if cwd == "" {
		return "", nil
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	cwd = real(cwd)
	for _, rootFn := range []func() (string, error){home.ProjectsDir, home.WorktreesDir} {
		root, err := rootFn()
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(real(root), cwd)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		slug, _, _ := strings.Cut(rel, string(filepath.Separator))
		if ValidSlug(slug) {
			return slug, nil
		}
	}
	return "", nil
}

// SetRepo adds (or removes) a repo in PROJECT.md's repo list. An added
// path must be a directory. changed is false when it was already so.
func (p *Project) SetRepo(path string, add bool) (changed bool, err error) {
	if add {
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			return false, refuse("invalid-repo", "%s is not a directory", path)
		}
	}
	err = mdfile.Update(p.Path("PROJECT.md"), func(old []byte) ([]byte, error) {
		var m Meta
		body, err := mdfile.Decode(old, &m)
		if err != nil {
			return nil, err
		}
		var repos []string
		found := false
		for _, r := range m.Repos {
			if r == path {
				found = true
				if !add {
					continue
				}
			}
			repos = append(repos, r)
		}
		if add && !found {
			repos = append(repos, path)
		}
		changed = found != add
		if !changed {
			return old, nil
		}
		m.Repos = repos
		p.Meta = m
		return mdfile.Join(m, body)
	})
	return changed, err
}
