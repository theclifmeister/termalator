package ticker

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/theclifmeister/terminatr/internal/mdfile"
)

// A renamed project keeps its memos (tm project rename, docs/SPEC.md
// §5.1): which items its coordinator was told about, which tasks were
// completed for which merge, its threads' PR states. Without them the
// next sweep would treat it as a new project and say it all again.

// RenameProject runs move, which renames project from to to, between
// two sweeps, and then files the project's memos under to. move's error
// means nothing was renamed, and the memos stay.
func (t *Ticker) RenameProject(from, to string, move func() error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := move(); err != nil {
		return err
	}
	t.st.rename(from, to)
	delete(t.gh, from)
	delete(t.ghErr, from)
	t.save()
	return nil
}

// RenameProjectState does the same to the state file at path, for a
// rename while no server runs.
func RenameProjectState(path, from, to string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st := &state{}
	if err := json.Unmarshal(b, st); err != nil {
		return err
	}
	st.rename(from, to)
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return mdfile.WriteAtomic(path, append(out, '\n'), 0o600)
}

func (st *state) rename(from, to string) {
	if m, ok := st.Projects[from]; ok {
		if st.Projects == nil {
			st.Projects = map[string]*projectMemo{}
		}
		st.Projects[to] = m
		delete(st.Projects, from)
	}
	for k, m := range st.Threads {
		if id, ok := strings.CutPrefix(k, from+"/"); ok {
			st.Threads[to+"/"+id] = m
			delete(st.Threads, k)
		}
	}
}
