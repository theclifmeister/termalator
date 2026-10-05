package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theclifmeister/termilator/internal/caller"
	"github.com/theclifmeister/termilator/internal/mdfile"
)

var now = time.Now

// Journal appends one line to JOURNAL.md:
// "<time> <who> <action> <ref> <detail>".
func (p *Project) Journal(c caller.Caller, action, ref, detail string) error {
	line := strings.Join(strings.Fields(fmt.Sprintf("%s %s %s %s %s",
		now().UTC().Format(time.RFC3339), c.String(), action, ref, detail)), " ")
	return mdfile.Append(p.Path("JOURNAL.md"), []byte(line+"\n"))
}

// JournalTail returns the last n journal lines, skipping the heading.
func (p *Project) JournalTail(n int) (lines []string, total int, err error) {
	data, err := os.ReadFile(p.Path("JOURNAL.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" && l[0] >= '0' && l[0] <= '9' {
			lines = append(lines, l)
		}
	}
	total = len(lines)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, total, nil
}

// Item is one inbox entry (§7.5): TOML front matter, no body taken from
// untrusted sources.
type Item struct {
	ID        string    `toml:"id" json:"id"`
	Kind      string    `toml:"kind" json:"kind"`
	Subject   string    `toml:"subject" json:"subject"`
	Created   time.Time `toml:"created" json:"created"`
	Summary   string    `toml:"summary" json:"summary"`
	NeedsUser bool      `toml:"needs_user" json:"needs_user"`
}

// AddItem writes inbox/<ts>-<kind>-<subject>.md.
func (p *Project) AddItem(kind, subject, summary string, needsUser bool) (*Item, error) {
	t := now().UTC().Truncate(time.Second)
	base := fmt.Sprintf("%s-%s-%s", t.Format("20060102T150405Z"), kind, Slugify(subject))
	id := base
	for i := 2; ; i++ {
		if _, err := os.Stat(p.Path("inbox", id+".md")); errors.Is(err, fs.ErrNotExist) {
			break
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
	it := &Item{ID: id, Kind: kind, Subject: subject, Created: t, Summary: summary, NeedsUser: needsUser}
	if err := os.MkdirAll(p.Path("inbox"), 0o755); err != nil {
		return nil, err
	}
	if err := mdfile.Write(p.Path("inbox", id+".md"), it, nil); err != nil {
		return nil, err
	}
	return it, nil
}

// KindTakeover is the inbox item that tells the coordinator the user took
// over a thread's watch-only pane and may type into it (§4).
const KindTakeover = "takeover"

// TookOver records that the user took over thread id's pane: an inbox
// item for the coordinator and a journal line.
func (p *Project) TookOver(c caller.Caller, id string) error {
	if _, err := p.AddItem(KindTakeover, id, "the user took over "+id+"'s pane and may type into it", false); err != nil {
		return err
	}
	return p.Journal(c, "thread.takeover", id, "")
}

// Inbox lists the unhandled items, oldest first.
func (p *Project) Inbox() ([]Item, error) {
	entries, err := os.ReadDir(p.Path("inbox"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(p.Path("inbox", e.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue // handled meanwhile
		}
		if err != nil {
			return nil, err
		}
		it, err := ParseItem(strings.TrimSuffix(e.Name(), ".md"), data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Path("inbox", e.Name()), err)
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// ParseItem reads one inbox file; its id is the file name. The summary
// is folded to one line, so nothing in an item can pose as more lines of
// a prompt or a listing.
func ParseItem(id string, data []byte) (Item, error) {
	var it Item
	if _, err := mdfile.Decode(data, &it); err != nil {
		return Item{}, err
	}
	it.ID = id
	it.Kind = strings.Join(strings.Fields(it.Kind), "-")
	it.Subject = strings.Join(strings.Fields(it.Subject), " ")
	it.Summary = strings.Join(strings.Fields(it.Summary), " ")
	return it, nil
}

// PruneDone deletes handled items older than maxAge (§7.5: 30 days),
// judged by the file's modification time, which DoneItem's rename keeps
// from the item's creation.
func (p *Project) PruneDone(maxAge time.Duration) (int, error) {
	dir := p.Path("inbox", "done")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	cutoff := now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n, nil
}

// DoneItem moves an item to inbox/done/. An item already done is fine.
func (p *Project) DoneItem(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.HasPrefix(id, ".") {
		return refuse("unknown-item", "%q is not an inbox item", id)
	}
	src := p.Path("inbox", id+".md")
	dst := p.Path("inbox", "done", id+".md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := os.Rename(src, dst)
	if errors.Is(err, fs.ErrNotExist) {
		if _, err2 := os.Stat(dst); err2 == nil {
			return nil
		}
		return refuse("unknown-item", "no inbox item %s", id)
	}
	return err
}

// events connects a task store to the project's journal.
type events struct{ p *Project }

func (e events) Journal(c caller.Caller, action, ref, detail string) error {
	return e.p.Journal(c, action, ref, detail)
}
