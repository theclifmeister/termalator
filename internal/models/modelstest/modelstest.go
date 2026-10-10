// Package modelstest sets up which agents a test sees installed and
// what they answered for their models, so no test depends on the agents
// of the machine it runs on.
package modelstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/models"
)

// Agents makes exactly the named built-in agents installed: a stub
// program for each (it fails if run) in a new directory first on PATH,
// and every PATH directory that holds another agent's program left out,
// so a developer's own claude or codex never shows. It returns the
// stubs' directory.
func Agents(t testing.TB, names ...string) string {
	t.Helper()
	reg, err := agent.Load("")
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, n := range reg.Names() {
		a, _ := reg.Get(n)
		cmds = append(cmds, agent.ManifestOf(a).Launch.Command)
	}
	dir := t.TempDir()
	for _, n := range names {
		a, ok := reg.Get(n)
		if !ok {
			t.Fatalf("no built-in agent %s", n)
		}
		Stub(t, dir, agent.ManifestOf(a).Launch.Command)
	}
	path := []string{dir}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if !holdsAny(d, cmds) {
			path = append(path, d)
		}
	}
	t.Setenv("PATH", strings.Join(path, string(os.PathListSeparator)))
	return dir
}

// Stub writes a program named cmd in dir that fails when run.
func Stub(t testing.TB, dir, cmd string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, cmd), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func holdsAny(dir string, cmds []string) bool {
	for _, c := range cmds {
		if fi, err := os.Stat(filepath.Join(dir, c)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// Answer saves what agent answered for a logged-in account: one model
// per name, "*" after a name marking the agent's default and "~" an
// older version.
func Answer(t testing.TB, agentName string, names ...string) {
	t.Helper()
	c := models.Cache{Agent: agentName, Version: "1.0", Asked: time.Now(), Status: models.StatusOK, Account: "test", Fingerprint: "f1"}
	for _, n := range names {
		m := agent.ListedModel{Name: strings.TrimRight(n, "*~"), Description: "about " + strings.TrimRight(n, "*~")}
		m.Default = strings.Contains(n, "*")
		m.Older = strings.Contains(n, "~")
		c.Models = append(c.Models, m)
	}
	Save(t, c)
}

// LoggedOut saves that no one is logged in to the agent.
func LoggedOut(t testing.TB, agentName string) {
	t.Helper()
	Save(t, models.Cache{Agent: agentName, Version: "1.0", Asked: time.Now(), Status: models.StatusLoggedOut})
}

// Save writes c as the agent's cache.
func Save(t testing.TB, c models.Cache) {
	t.Helper()
	if err := models.SaveForTest(c); err != nil {
		t.Fatal(err)
	}
}
