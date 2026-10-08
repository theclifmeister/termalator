package thread

import (
	"archive/tar"
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
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/caller"
	"github.com/theclifmeister/terminatr/internal/mdfile"
	"github.com/theclifmeister/terminatr/internal/plat/fsx"
	"github.com/theclifmeister/terminatr/internal/project"
)

// The library (docs/SPEC.md §7.2, §4 Project popup): the files threads
// attached to their reports, threads/<id>/library/, of every thread of
// a project, the resolved and archived ones too (an archived thread's
// files are in its tarball).

// LibFile is one file of a thread's library. It never carries a path:
// the thread and the name say where it is.
type LibFile struct {
	Thread string `json:"thread"`
	Task   string `json:"task,omitempty"`
	Title  string `json:"title"`
	// State is the thread's: running, stopped, resolved or archived.
	State string    `json:"state"`
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	Time  time.Time `json:"time"`
}

// Library lists every file in the library of every thread of p, newest
// file first (ties by thread, then name).
func Library(p *project.Project) ([]LibFile, error) {
	var out []LibFile
	recs, err := List(p)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, r := range recs {
		live[r.ID] = true
		ents, err := os.ReadDir(Path(p, r.ID, "library"))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, LibFile{Thread: r.ID, Task: r.Task, Title: r.Title, State: r.State,
				Name: e.Name(), Size: fi.Size(), Time: fi.ModTime()})
		}
	}
	arch, _ := ListArchived(p)
	for _, a := range arch {
		if live[a.ID] {
			continue // archived, then not yet removed: the folder is the truth
		}
		for _, f := range archivedLibrary(p, a.ID) {
			f.Task, f.Title, f.State = a.Task, a.Title, "archived"
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Time.Equal(b.Time) {
			return a.Time.After(b.Time)
		}
		if a.Thread != b.Thread {
			return a.Thread < b.Thread
		}
		return a.Name < b.Name
	})
	return out, nil
}

// archiveLibCache remembers an archive's library listing while its
// tarball is unchanged, since reading one means unpacking it.
var archiveLibCache sync.Map // path -> cachedLib

type cachedLib struct {
	mod   time.Time
	size  int64
	files []LibFile
}

// archivedLibrary lists the library files in thread id's tarball.
func archivedLibrary(p *project.Project, id string) []LibFile {
	ap := archivePath(p, id)
	fi, err := os.Stat(ap)
	if err != nil {
		return nil
	}
	if c, ok := archiveLibCache.Load(ap); ok {
		if c := c.(cachedLib); c.mod.Equal(fi.ModTime()) && c.size == fi.Size() {
			return c.files
		}
	}
	var out []LibFile
	_ = walkArchive(ap, func(h *tar.Header, _ io.Reader) error {
		if name, ok := libraryName(id, h); ok {
			out = append(out, LibFile{Thread: id, Name: name, Size: h.Size, Time: h.ModTime})
		}
		return nil
	})
	archiveLibCache.Store(ap, cachedLib{fi.ModTime(), fi.Size(), out})
	return out
}

// libraryName is the library file a tar entry of thread id's archive
// is: its name, else false.
func libraryName(id string, h *tar.Header) (string, bool) {
	if h.Typeflag != tar.TypeReg {
		return "", false
	}
	rest, ok := strings.CutPrefix(path.Clean(h.Name), id+"/library/")
	if !ok || rest == "" || strings.Contains(rest, "/") || strings.HasPrefix(rest, ".") {
		return "", false
	}
	return rest, true
}

func walkArchive(ap string, fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(ap)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

// checkLibName refuses a name that isn't a plain file name: no path
// goes in or out of a library.
func checkLibName(name string) error {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return refuse("invalid-file", "%q is not a library file name", name)
	}
	return nil
}

// ReadLibraryFile reads up to max bytes of a library file, from the
// folder or, for an archived thread, from the tarball. truncated says
// the file was longer.
func ReadLibraryFile(p *project.Project, id, name string, max int64) (data []byte, truncated bool, err error) {
	if !ValidID(id) {
		return nil, false, refuse("unknown-thread", "%q is not a thread id like t-0003", id)
	}
	if err := checkLibName(name); err != nil {
		return nil, false, err
	}
	f, err := os.Open(Path(p, id, "library", name))
	if err == nil {
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, max+1))
		if err != nil {
			return nil, false, err
		}
		if int64(len(data)) > max {
			return data[:max], true, nil
		}
		return data, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	found := false
	werr := walkArchive(archivePath(p, id), func(h *tar.Header, r io.Reader) error {
		if n, ok := libraryName(id, h); ok && n == name && !found {
			found = true
			data, err = io.ReadAll(io.LimitReader(r, max+1))
			if err == nil && int64(len(data)) > max {
				data, truncated = data[:max], true
			}
			return err
		}
		return nil
	})
	if werr != nil && !errors.Is(werr, fs.ErrNotExist) {
		return nil, false, werr
	}
	if !found {
		return nil, false, refuse("unknown-file", "thread %s has no library file %s", id, name)
	}
	return data, truncated, err
}

// RemoveLibrary deletes library files of thread id: the named one, or
// every one when name is "". It journals each deletion as library.rm and
// returns how many it removed. A resolved thread's archive is rewritten
// without them.
func RemoveLibrary(p *project.Project, c caller.Caller, id, name string) (int, error) {
	if !ValidID(id) {
		return 0, refuse("unknown-thread", "%q is not a thread id like t-0003", id)
	}
	if name != "" {
		if err := checkLibName(name); err != nil {
			return 0, err
		}
	}
	var gone []string
	dir := Path(p, id, "library")
	if _, err := os.Stat(Dir(p, id)); err == nil {
		ents, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		for _, e := range ents {
			if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") || name != "" && e.Name() != name {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return len(gone), err
			}
			gone = append(gone, e.Name())
		}
		_ = os.Remove(dir) // when it is empty
	} else {
		var err error
		if gone, err = removeArchived(p, id, name); err != nil {
			return 0, err
		}
	}
	if name != "" && len(gone) == 0 {
		return 0, refuse("unknown-file", "thread %s has no library file %s", id, name)
	}
	for _, g := range gone {
		if err := p.Journal(c, "library.rm", id, g); err != nil {
			return len(gone), err
		}
	}
	return len(gone), nil
}

// errNoneGone keeps an archive that removeArchived would not change.
var errNoneGone = errors.New("no library file to remove")

// removeArchived rewrites thread id's tarball without the library file
// name (every one when ""), returning the names it dropped. The new
// tarball replaces the old atomically, under the lock Archive takes.
func removeArchived(p *project.Project, id, name string) ([]string, error) {
	ap := archivePath(p, id)
	if _, err := os.Stat(ap); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, refuse("unknown-thread", "no thread %s in project %s (tm thread list --all)", id, p.Slug)
		}
		return nil, err
	}
	unlock, err := mdfile.Lock(p.Path("threads", "ids"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	var gone []string
	err = fsx.Stream(ap, 0o644, func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		tw := tar.NewWriter(zw)
		err := walkArchive(ap, func(h *tar.Header, r io.Reader) error {
			if n, ok := libraryName(id, h); ok && (name == "" || n == name) {
				gone = append(gone, n)
				return nil
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			_, err := io.Copy(tw, r)
			return err
		})
		if err == nil {
			err = tw.Close()
		}
		if err == nil {
			err = zw.Close()
		}
		if err != nil {
			return fmt.Errorf("%s: %w", ap, err)
		}
		if len(gone) == 0 {
			return errNoneGone
		}
		return nil
	})
	if errors.Is(err, errNoneGone) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	archiveLibCache.Delete(ap)
	return gone, nil
}
