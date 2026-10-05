// Package home locates termilator's state directory, ~/.termilator by
// default (docs/SPEC.md §5.1). TERMILATOR_HOME moves all of it, which is
// how tests run without touching the real home.
package home

import (
	"errors"
	"os"
	"path/filepath"
)

// Env is the variable that overrides the state directory.
const Env = "TERMILATOR_HOME"

// Dir returns the absolute state directory: $TERMILATOR_HOME, or
// ~/.termilator. It does not create it.
func Dir() (string, error) {
	if d := os.Getenv(Env); d != "" {
		return filepath.Abs(d)
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if h == "" {
		return "", errors.New("no home directory; set " + Env)
	}
	return filepath.Join(h, ".termilator"), nil
}

// ProjectsDir is <home>/projects.
func ProjectsDir() (string, error) { return sub("projects") }

// WorktreesDir is <home>/worktrees.
func WorktreesDir() (string, error) { return sub("worktrees") }

func sub(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}
