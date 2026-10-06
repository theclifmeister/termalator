package thread

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/theclifmeister/terminatr/internal/mdfile"
	"github.com/theclifmeister/terminatr/internal/project"
)

// Archived threads (docs/SPEC.md §7.6): the ticker packs a thread
// resolved for a while into threads/archive/<id>.tar.gz and adds one
// line to threads/archive/index, so tm context and tm thread list stay
// short and the threads folder small. tm thread show reads an archived
// thread from its tarball; nothing else does.

// ArchiveDir is threads/archive/ of a project.
func ArchiveDir(p *project.Project) string { return p.Path("threads", "archive") }

func archivePath(p *project.Project, id string) string {
	return filepath.Join(ArchiveDir(p), id+".tar.gz")
}

func indexPath(p *project.Project) string { return filepath.Join(ArchiveDir(p), "index") }

// Archived is one line of threads/archive/index.
type Archived struct {
	ID       string `json:"id"`
	Task     string `json:"task,omitempty"`
	Title    string `json:"title"`
	Resolved string `json:"resolved"` // 2006-01-02
}

// Line is how tm thread list --all shows an archived thread.
func (a Archived) Line() string {
	name := a.ID + " " + a.Title
	if a.Task != "" {
		name = a.Task + " (" + a.ID + ") " + a.Title
	}
	return name + "  archived (resolved " + a.Resolved + "; tm thread show " + a.ID + ")"
}

// indexLine is "<id> <resolved> <task or -> <title>".
func (a Archived) indexLine() string {
	task := a.Task
	if task == "" {
		task = "-"
	}
	return strings.Join([]string{a.ID, a.Resolved, task, cleanTitle(a.Title)}, " ")
}

func parseIndexLine(l string) (Archived, bool) {
	f := strings.SplitN(strings.TrimSpace(l), " ", 4)
	if len(f) < 3 || !ValidID(f[0]) {
		return Archived{}, false
	}
	a := Archived{ID: f[0], Resolved: f[1], Task: f[2]}
	if a.Task == "-" {
		a.Task = ""
	}
	if len(f) == 4 {
		a.Title = f[3]
	}
	return a, true
}

// ListArchived reads threads/archive/index, by id; an id listed twice
// (an archive retried after a crash) shows once.
func ListArchived(p *project.Project) ([]Archived, error) {
	data, err := os.ReadFile(indexPath(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Archived
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if a, ok := parseIndexLine(sc.Text()); ok && !seen[a.ID] {
			seen[a.ID] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CountArchived is how many threads are archived; 0 when the index
// can't be read.
func CountArchived(p *project.Project) int {
	a, _ := ListArchived(p)
	return len(a)
}

// archivedIDs are the ids of the tarballs in threads/archive/, which
// Create counts so an archived thread's id is never reused.
func archivedIDs(p *project.Project) []string {
	ents, _ := os.ReadDir(ArchiveDir(p))
	var out []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".tar.gz"); ok && ValidID(id) {
			out = append(out, id)
		}
	}
	return out
}

// ResolvedAt is when a resolved thread was resolved: its record's
// resolved_at, else (resolved before tm kept it) the record's last
// write, which resolve was.
func ResolvedAt(p *project.Project, r *Record) time.Time {
	if !r.ResolvedAt.IsZero() {
		return r.ResolvedAt
	}
	if fi, err := os.Stat(recordPath(p, r.ID)); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// Archive packs resolved thread r's folder into threads/archive/<id>.tar.gz,
// adds its index line and removes the folder. The tarball is in place
// before the folder goes, so a crash leaves both, and the next archive
// replaces the tarball. It refuses a thread that isn't resolved.
func Archive(p *project.Project, r *Record) error {
	if r.State != Resolved {
		return refuse("not-resolved", "thread %s is not resolved", r.ID)
	}
	// Under the ids lock, so Create sees either the folder or the
	// tarball; under the record's, so no update lands in between.
	unlockIDs, err := mdfile.Lock(p.Path("threads", "ids"))
	if err != nil {
		return err
	}
	defer unlockIDs()
	unlock, err := mdfile.Lock(recordPath(p, r.ID))
	if err != nil {
		return err
	}
	defer unlock()
	data, err := packDir(Dir(p, r.ID), r.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ArchiveDir(p), 0o755); err != nil {
		return err
	}
	if err := mdfile.WriteAtomic(archivePath(p, r.ID), data, 0o644); err != nil {
		return err
	}
	a := Archived{ID: r.ID, Task: r.Task, Title: r.Title, Resolved: ResolvedAt(p, r).Format("2006-01-02")}
	if err := mdfile.Append(indexPath(p), []byte(a.indexLine()+"\n")); err != nil {
		return err
	}
	return os.RemoveAll(Dir(p, r.ID))
}

// packDir is a gzipped tar of dir's regular files under id/; lock files
// (hidden) stay out.
func packDir(dir, id string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		hdr := &tar.Header{Name: path.Join(id, filepath.ToSlash(rel)), Mode: 0o644, Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ArchivedThread is an archived thread as its tarball holds it.
type ArchivedThread struct {
	Record *Record
	// Files are the folder's files by path inside it ("REPORT.md",
	// "reports/1.md", "library/x.png").
	Files map[string][]byte
}

// maxArchivedFile caps what LoadArchived reads of one file.
const maxArchivedFile = 16 << 20

// LoadArchived reads an archived thread; unknown-thread when there is no
// tarball for id.
func LoadArchived(p *project.Project, id string) (*ArchivedThread, error) {
	if !ValidID(id) {
		return nil, refuse("unknown-thread", "%q is not a thread id like t-0003", id)
	}
	f, err := os.Open(archivePath(p, id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("unknown-thread", "no thread %s in project %s (tm thread list --all)", id, p.Slug)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", archivePath(p, id), err)
	}
	at := &ArchivedThread{Files: map[string][]byte{}}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", archivePath(p, id), err)
		}
		name, ok := strings.CutPrefix(path.Clean(h.Name), id+"/")
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxArchivedFile))
		if err != nil {
			return nil, err
		}
		at.Files[name] = b
	}
	data, ok := at.Files["thread.toml"]
	if !ok {
		return nil, fmt.Errorf("%s: no thread.toml", archivePath(p, id))
	}
	var r Record
	if _, err := toml.Decode(string(data), &r); err != nil {
		return nil, fmt.Errorf("%s: thread.toml: %w", archivePath(p, id), err)
	}
	at.Record = &r
	return at, nil
}
