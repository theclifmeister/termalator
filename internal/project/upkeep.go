package project

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/mdfile"
	"github.com/theclifmeister/terminatr/internal/plat/fsx"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/tasks"
)

// Upkeep of the coordinator's context files (docs/SPEC.md §7.6).
// CONTEXT.md and the memory index are printed by every `tm context`, and
// memory files are read into threads' work, so each has a size budget;
// `tm context` and `tm doctor` say when one is over it, and the
// coordinator's skill says to consolidate it.
const (
	BudgetContext = 6 << 10 // CONTEXT.md
	BudgetMemory  = 6 << 10 // MEMORY.md, and each file under memory/
)

// Oversize is a context file over its budget.
type Oversize struct {
	File   string // relative to the project folder, e.g. "memory/decisions.md"
	Size   int64
	Budget int64
}

func (o Oversize) String() string {
	return fmt.Sprintf("%s is %s, over its %s budget: consolidate it", o.File, kb(o.Size), kb(o.Budget))
}

func kb(n int64) string {
	if n%1024 == 0 {
		return fmt.Sprintf("%d KB", n/1024)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// Oversized lists the context files over their budgets, in a stable
// order: CONTEXT.md, MEMORY.md, then memory/ by path.
func (p *Project) Oversized() ([]Oversize, error) {
	var out []Oversize
	check := func(rel string, budget int64) error {
		fi, err := os.Stat(p.Path(rel))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() && fi.Size() > budget {
			out = append(out, Oversize{File: rel, Size: fi.Size(), Budget: budget})
		}
		return nil
	}
	if err := check("CONTEXT.md", BudgetContext); err != nil {
		return nil, err
	}
	if err := check("MEMORY.md", BudgetMemory); err != nil {
		return nil, err
	}
	var files []string
	err := filepath.WalkDir(p.Path("memory"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(p.Dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		if err := check(f, BudgetMemory); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ArchiveOldDone moves each task done for at least age (by its updated
// date) to the task archive, as caller c; it returns their ids.
func (p *Project) ArchiveOldDone(c caller.Caller, now time.Time, age time.Duration) ([]int, error) {
	s := p.Tasks()
	b, err := s.Load()
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, t := range b.Tasks {
		if t.Status != tasks.Done {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02", t.Updated, now.Location())
		if err != nil || now.Sub(at) < age {
			continue
		}
		if _, err := s.Archive(c, t.ID); err != nil {
			return ids, err
		}
		ids = append(ids, t.ID)
	}
	return ids, nil
}

// Retention (docs/SPEC.md §7.6): the ticker moves what is old out of the
// files people and agents read, into compressed monthly files beside
// them; nothing is deleted. Handled inbox items go to
// inbox/done/<yyyy-mm>.tar.gz, journal lines to journal/<yyyy-mm>.md.gz
// (gzip members appended, so zcat reads a month whole).

// month is the yyyy-mm a time falls in, UTC.
func month(t time.Time) string { return t.UTC().Format("2006-01") }

// ArchiveInbox bundles the handled items older than age (by the file's
// modification time, which DoneItem's rename keeps from the item's
// creation) into inbox/done/<yyyy-mm>.tar.gz by the month they were
// created, and removes the loose files; it returns how many it bundled.
func (p *Project) ArchiveInbox(now time.Time, age time.Duration) (int, error) {
	dir := p.Path("inbox", "done")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-age)
	byMonth := map[string][]string{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		at := fi.ModTime()
		if t, err := time.Parse("20060102T150405Z", strings.SplitN(e.Name(), "-", 2)[0]); err == nil {
			at = t
		}
		byMonth[month(at)] = append(byMonth[month(at)], e.Name())
	}
	months := make([]string, 0, len(byMonth))
	for m := range byMonth {
		months = append(months, m)
	}
	sort.Strings(months)
	n := 0
	for _, m := range months {
		names := byMonth[m]
		sort.Strings(names)
		if err := addToTar(filepath.Join(dir, m+".tar.gz"), dir, names); err != nil {
			return n, err
		}
		for _, name := range names {
			if os.Remove(filepath.Join(dir, name)) == nil {
				n++
			}
		}
	}
	return n, nil
}

// addToTar adds files (names in dir) to the gzipped tar at path, under
// its lock, keeping what it holds; a name it holds already is replaced.
func addToTar(path, dir string, names []string) error {
	unlock, err := mdfile.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	adding := map[string]bool{}
	for _, n := range names {
		adding[n] = true
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if f, err := os.Open(path); err == nil {
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return fmt.Errorf("%s: %w", path, err)
		}
		tr := tar.NewReader(zr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return fmt.Errorf("%s: %w", path, err)
			}
			if adding[h.Name] {
				continue
			}
			if err := tw.WriteHeader(h); err != nil {
				f.Close()
				return err
			}
			if _, err := io.Copy(tw, tr); err != nil {
				f.Close()
				return err
			}
		}
		f.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: fi.ModTime(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return fsx.WriteAtomic(path, buf.Bytes(), 0o644)
}

// ArchiveJournal moves the JOURNAL.md lines older than age (by their
// timestamp) to journal/<yyyy-mm>.md.gz, by month; the heading and
// newer lines stay. It returns how many lines moved.
func (p *Project) ArchiveJournal(now time.Time, age time.Duration) (int, error) {
	path := p.Path("JOURNAL.md")
	unlock, err := mdfile.Lock(path)
	if err != nil {
		return 0, err
	}
	defer unlock()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-age)
	var keep []string
	old := map[string][]string{}
	var months []string
	n := 0
	for _, l := range strings.SplitAfter(string(data), "\n") {
		if l == "" {
			continue
		}
		ts, _, _ := strings.Cut(l, " ")
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil || !t.Before(cutoff) {
			keep = append(keep, l)
			continue
		}
		m := month(t)
		if old[m] == nil {
			months = append(months, m)
		}
		old[m] = append(old[m], l)
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(p.Path("journal"), 0o755); err != nil {
		return 0, err
	}
	sort.Strings(months)
	for _, m := range months {
		if err := appendGzip(p.Path("journal", m+".md.gz"), []byte(strings.Join(old[m], ""))); err != nil {
			return 0, err
		}
	}
	// The archive holds the lines before they leave JOURNAL.md: a crash
	// in between repeats them in the archive rather than losing them.
	return n, fsx.WriteAtomic(path, []byte(strings.Join(keep, "")), 0o644)
}

// appendGzip appends data to path as one more gzip member, under its
// lock.
func appendGzip(path string, data []byte) error {
	unlock, err := mdfile.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
