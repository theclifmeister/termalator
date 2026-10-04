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

	"github.com/theclifmeister/termalator/internal/caller"
	"github.com/theclifmeister/termalator/internal/mdfile"
	"github.com/theclifmeister/termalator/internal/tasks"
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
		var it Item
		if _, err := mdfile.Read(p.Path("inbox", e.Name()), &it); err != nil {
			return nil, err
		}
		it.ID = strings.TrimSuffix(e.Name(), ".md")
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
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

// events connects a task store to the project's journal and inbox.
type events struct{ p *Project }

func (e events) Journal(c caller.Caller, action, ref, detail string) error {
	return e.p.Journal(c, action, ref, detail)
}

const kindConfirm = "confirm-done"

// RequestDone raises one NEEDS YOU confirmation per task; asking again
// returns the open one.
func (e events) RequestDone(c caller.Caller, t *tasks.Task) (string, error) {
	items, err := e.p.Inbox()
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Kind == kindConfirm && it.Subject == t.Ref() {
			return it.ID, nil
		}
	}
	summary := fmt.Sprintf("%s asks to mark %s done; confirm with: tm task status %s done", c.String(), t.Ref(), t.Ref())
	it, err := e.p.AddItem(kindConfirm, t.Ref(), summary, true)
	if err != nil {
		return "", err
	}
	if err := e.p.Journal(c, "task.request-done", t.Ref(), it.ID); err != nil {
		return "", err
	}
	return it.ID, nil
}

// Confirmed closes the task's open confirmations once the human set done.
func (e events) Confirmed(t *tasks.Task) error {
	items, err := e.p.Inbox()
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Kind == kindConfirm && it.Subject == t.Ref() {
			if err := e.p.DoneItem(it.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
