package server

// terminatr's mod (docs/SPEC.md §8.6, Mods): an agent that ships one
// (agent.Modder) loads it only when [mods] enabled is on and the agent's
// version is at least the one the mod was tested on. Otherwise the
// session starts as before, on its command hooks alone, which stay in
// either case.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
)

// versionTimeout bounds `<agent> --version`, run with s.mu held, once per
// agent binary (versions caches it).
const versionTimeout = 5 * time.Second

// modsSetting is [mods] enabled; a config.toml that doesn't load leaves
// mods off.
func modsSetting() bool {
	cfg, err := config.Load()
	return err == nil && cfg.Mods
}

// envBand is set to "off" in a session with the mod when [mods] band is
// false: the mod then draws no band, status entry or toast.
const envBand = "TERMINATR_BAND"

// bandSetting is [mods] band; a config.toml that doesn't load leaves it
// on, as when unset.
func bandSetting() bool {
	cfg, err := config.Load()
	return err != nil || cfg.ModsBand
}

// modsFor reports whether a session of a gets terminatr's mod, and logs
// why not when the setting asks for it.
func (s *Server) modsFor(a agent.Agent, id string) bool {
	m, ok := a.(agent.Modder)
	if !ok || !modsSetting() {
		return false
	}
	v, err := s.versions.of(a, s.baseEnv())
	if err != nil {
		s.log.Printf("session %s: no mod: %s version: %v", id, a.Name(), err)
		return false
	}
	if !agent.VersionAtLeast(v, m.ModsMinVersion()) {
		s.log.Printf("session %s: no mod: %s %s is older than %s", id, a.Name(), v, m.ModsMinVersion())
		return false
	}
	return true
}

// versions caches each agent binary's version by its path, size and
// modification time, so an upgrade is seen at the next launch.
type versions struct {
	mu   sync.Mutex
	seen map[string]versionSeen
	// run runs the binary with args under env (exec by default; tests).
	run func(ctx context.Context, path string, args, env []string) ([]byte, error)
}

type versionSeen struct {
	size    int64
	mod     time.Time
	version string
}

// of returns a's version as its version_args print it, the binary found
// on env's PATH as a session would find it.
func (vs *versions) of(a agent.Agent, env []string) (string, error) {
	m := agent.ManifestOf(a)
	if m == nil || len(m.Identify.VersionArgs) == 0 {
		return "", errors.New("the manifest has no version_args")
	}
	path, err := lookPathIn(m.Launch.Command, envValue(env, "PATH"))
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if c, ok := vs.seen[path]; ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.version, nil
	}
	run := vs.run
	if run == nil {
		run = runVersion
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	out, err := run(ctx, path, m.Identify.VersionArgs, env)
	if err != nil {
		return "", err
	}
	v := agent.ParseVersion(string(out))
	if v == "" {
		return "", errors.New("no version in " + strings.TrimSpace(string(out)))
	}
	if vs.seen == nil {
		vs.seen = map[string]versionSeen{}
	}
	vs.seen[path] = versionSeen{size: fi.Size(), mod: fi.ModTime(), version: v}
	return v, nil
}

func runVersion(ctx context.Context, path string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	return cmd.Output()
}

// lookPathIn finds an executable named cmd in the directories of path
// (a PATH value); a cmd with a slash is taken as it is.
func lookPathIn(cmd, path string) (string, error) {
	if strings.Contains(cmd, "/") {
		return cmd, nil
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, cmd)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New(cmd + " not found on the sessions' PATH")
}

// envValue is the value of key in env (KEY=VALUE entries), the last one
// winning.
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}
