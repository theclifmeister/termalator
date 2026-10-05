package project

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// Memory is what every thread of the project is told besides its task:
// the living plan (CONTEXT.md), the memory index (MEMORY.md) and the
// titles of the memory notes (memory/*.md). The project popup's Memory
// tab shows it, read-only (docs/SPEC.md §4).
type Memory struct {
	Context, Index string
	// Notes are the memory notes' titles, sorted: each one's first
	// heading, else its file name without .md.
	Notes []string
}

// ReadMemory reads the project's memory. Missing files are empty.
func (p *Project) ReadMemory() (Memory, error) {
	var m Memory
	for _, f := range []struct {
		name string
		to   *string
	}{{"CONTEXT.md", &m.Context}, {"MEMORY.md", &m.Index}} {
		data, err := os.ReadFile(p.Path(f.name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Memory{}, err
		}
		*f.to = string(data)
	}
	ents, err := os.ReadDir(p.Path("memory"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Memory{}, err
	}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() {
			continue
		}
		data, err := os.ReadFile(p.Path("memory", e.Name()))
		if err != nil {
			return Memory{}, err
		}
		m.Notes = append(m.Notes, noteTitle(string(data), name))
	}
	slices.Sort(m.Notes)
	return m, nil
}

// noteTitle is a note's first heading, else name.
func noteTitle(text, name string) string {
	for _, l := range strings.Split(text, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(l), "#"); ok {
			if h = strings.TrimSpace(strings.TrimLeft(h, "#")); h != "" {
				return h
			}
		}
	}
	return name
}
