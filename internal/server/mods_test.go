package server

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/home"
)

// modder is a manifest agent with a mod tested on 2.1.289.
type modder struct {
	agent.Agent
	m *agent.Manifest
}

func (modder) ModsMinVersion() string      { return "2.1.289" }
func (modder) ModRequired() bool           { return false }
func (x modder) Manifest() *agent.Manifest { return x.m }

func modsServer(t *testing.T, enabled bool, version string) (*Server, agent.Agent, *int, *bytes.Buffer) {
	t.Helper()
	h := t.TempDir()
	t.Setenv(home.Env, h)
	if enabled {
		os.WriteFile(filepath.Join(h, "config.toml"), []byte("[mods]\nenabled = true\n"), 0o600)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o700)
	m, err := agent.ParseManifest([]byte("manifest_version = 1\nname = \"claude\"\n[identify]\nversion_args = [\"--version\"]\n[launch]\ncommand = \"claude\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	runs := new(int)
	var buf bytes.Buffer
	s := &Server{opts: Options{Env: []string{"PATH=/nowhere:" + bin}}, log: log.New(&buf, "", 0)}
	s.versions.run = func(_ context.Context, path string, args, _ []string) ([]byte, error) {
		*runs++
		if path != filepath.Join(bin, "claude") || strings.Join(args, " ") != "--version" {
			t.Errorf("ran %s %v", path, args)
		}
		return []byte(version + " (Claude Code)\n"), nil
	}
	return s, modder{agent.FromManifest(m), m}, runs, &buf
}

// TestModsFor: the mod loads only with [mods] enabled and a version at
// least the tested one, which is read once per binary.
func TestModsFor(t *testing.T) {
	s, a, runs, _ := modsServer(t, true, "2.1.291")
	if !s.modsFor(a, "s-1") || !s.modsFor(a, "s-2") || *runs != 1 {
		t.Fatalf("2.1.291 enabled: runs %d", *runs)
	}
	// Not a Modder: no mod, nothing run.
	if s.modsFor(a.(modder).Agent, "s-3") || *runs != 1 {
		t.Fatal("a plain agent got the mod")
	}

	s, a, _, buf := modsServer(t, true, "2.1.288")
	if s.modsFor(a, "s-1") || !strings.Contains(buf.String(), "claude 2.1.288 is older than 2.1.289") {
		t.Fatalf("2.1.288: %q", buf.String())
	}

	s, a, runs, _ = modsServer(t, false, "2.1.291")
	if s.modsFor(a, "s-1") || *runs != 0 {
		t.Fatalf("disabled: runs %d", *runs)
	}
}

// TestVersionsSeeUpgrade: a changed binary is asked again.
func TestVersionsSeeUpgrade(t *testing.T) {
	s, a, runs, _ := modsServer(t, true, "2.1.291")
	s.modsFor(a, "s-1")
	path := filepath.Join(strings.TrimPrefix(s.opts.Env[0], "PATH=/nowhere:"), "claude")
	os.WriteFile(path, []byte("#!/bin/sh\n# upgraded\n"), 0o700)
	os.Chtimes(path, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	s.modsFor(a, "s-2")
	if *runs != 2 {
		t.Fatalf("runs %d, want 2", *runs)
	}
}

// TestBandSetting: [mods] band is on unless set false, and on when
// config.toml doesn't load.
func TestBandSetting(t *testing.T) {
	h := t.TempDir()
	t.Setenv(home.Env, h)
	cfg := filepath.Join(h, "config.toml")
	for _, c := range []struct {
		body string
		want bool
	}{{"", true}, {"[mods]\nenabled = true\n", true}, {"[mods]\nband = false\n", false}, {"[mods\n", true}} {
		os.WriteFile(cfg, []byte(c.body), 0o600)
		if got := bandSetting(); got != c.want {
			t.Errorf("%q: band %v, want %v", c.body, got, c.want)
		}
	}
}

// TestFeatureVersion: a launch is judged by the agent's version when its
// manifest has [identify] features, and a feature it lacks is logged.
func TestFeatureVersion(t *testing.T) {
	featured := func(a agent.Agent) agent.Agent {
		m := agent.ManifestOf(a)
		m.Identify.Features = map[string]string{"sandbox": "2.1.290"}
		return a
	}
	s, a, runs, buf := modsServer(t, false, "2.1.291")
	if v := s.featureVersion(a, "s-1"); v != "" || *runs != 0 {
		t.Fatalf("no features: version %q, runs %d", v, *runs)
	}
	if v := s.featureVersion(featured(a), "s-1"); v != "2.1.291" || buf.Len() != 0 {
		t.Fatalf("2.1.291: version %q, log %q", v, buf.String())
	}
	s, a, _, buf = modsServer(t, false, "2.1.289")
	if v := s.featureVersion(featured(a), "s-1"); v != "2.1.289" || !strings.Contains(buf.String(), "claude 2.1.289 is too old for sandbox (needs 2.1.290)") {
		t.Fatalf("2.1.289: version %q, log %q", v, buf.String())
	}
	s, a, _, buf = modsServer(t, false, "garbage")
	if v := s.featureVersion(featured(a), "s-1"); v != "" || !strings.Contains(buf.String(), "version unknown") || !strings.Contains(buf.String(), "without sandbox (needs 2.1.290)") {
		t.Fatalf("garbage: version %q, log %q", v, buf.String())
	}
}
