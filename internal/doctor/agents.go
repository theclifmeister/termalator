package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/theclifmeister/terminatr/internal/agent"
)

var versionRE = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// Agents loads the manifests as the server would and reports each agent's
// installed version beside its last tested one, which is information
// only, and warns below a manifest's min_version (docs/SPEC.md §8.8).
// An agent that isn't installed is only a warning: nobody may use it.
func Agents(d Deps) []Check {
	const g = "agents"
	var out []Check
	reg, err := agent.Load(d.Paths.AgentsDir())
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			out = append(out, Check{Group: g, Name: "manifest", Status: Warn, Detail: "broken, skipped: " + line})
		}
	}
	if reg == nil {
		return append(out, Check{Group: g, Name: "manifests", Status: Fail, Detail: "the built-in manifests don't load"})
	}
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		m := agent.ManifestOf(a)
		if m == nil {
			continue
		}
		out = append(out, agentCheck(d, name, reg.Source[name], m))
	}
	return out
}

func agentCheck(d Deps, name, source string, m *agent.Manifest) Check {
	c := Check{Group: "agents", Name: name}
	src := ""
	if !strings.HasPrefix(source, "builtin:") {
		src = " (manifest " + source + ")"
	}
	path, err := d.LookPath(m.Launch.Command)
	if err != nil {
		c.Status, c.Detail = Warn, fmt.Sprintf("%s not found on PATH%s", m.Launch.Command, src)
		return c
	}
	if len(m.Identify.VersionArgs) == 0 {
		c.Status, c.Detail = OK, path+" (manifest has no version_args)"+src
		return c
	}
	raw, err := d.Run("", path, m.Identify.VersionArgs...)
	if err != nil {
		c.Status, c.Detail = Warn, fmt.Sprintf("%s: %v", path, err)
		return c
	}
	v := versionRE.FindString(raw)
	switch {
	case v == "":
		c.Status, c.Detail = Warn, fmt.Sprintf("%s: no version in %q", path, firstLine(raw))
	case m.Identify.MinVersion != "" && !agent.VersionAtLeast(v, m.Identify.MinVersion):
		why := ""
		if m.Identify.MinVersionWhy != "" {
			why = " (" + m.Identify.MinVersionWhy + ")"
		}
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s %s is older than %s, the oldest terminatr's generated files work with%s%s; update %s",
			path, v, m.Identify.MinVersion, why, src, m.Display)
	case !m.Tested(v):
		// Only a user manifest that sets tested_versions gets here.
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s %s is not in tested_versions %v%s; its status file and messaging socket are ignored, state comes from hooks and the screen",
			path, v, m.TestedVersions, src)
	default:
		// A newer version than the last tested is supported, not a
		// warning: tm logs what it finds changed (docs/SPEC.md §8.8).
		c.Status, c.Detail = OK, path+" "+v+lastTested(v, m.Identify.LastTested)+src
	}
	return c
}

// lastTested says how v stands to the manifest's last tested version,
// as information.
func lastTested(v, last string) string {
	switch {
	case last == "":
		return ""
	case agent.VersionAtLeast(last, v):
		return " (tested)"
	default:
		return " (newer than the last tested " + last + ": supported; tm logs any change it runs into)"
	}
}

// Sandbox checks what the agents' sandbox needs, which holds a thread's
// read-only access to the project folder (docs/SPEC.md §5.2). Warnings
// only: the permission rules still apply without it.
func Sandbox(d Deps) []Check {
	const g = "sandbox"
	var need []string
	switch d.GOOS {
	case "darwin":
		need = []string{"sandbox-exec"}
	case "linux":
		need = []string{"bwrap", "socat"}
	default:
		return nil
	}
	var out []Check
	for _, n := range need {
		if p, err := d.LookPath(n); err == nil {
			out = append(out, Check{Group: g, Name: n, Status: OK, Detail: p})
			continue
		}
		hint := ""
		switch n {
		case "bwrap":
			hint = " (install bubblewrap)"
		case "socat":
			hint = " (install socat)"
		}
		out = append(out, Check{Group: g, Name: n, Status: Warn,
			Detail: "not found" + hint + "; the agents' sandbox can't run, so threads rely on permission rules alone"})
	}
	if d.GOOS == "linux" {
		if c, ok := userns(); ok {
			out = append(out, c)
		}
	}
	return out
}

// procSys is /proc/sys, moved by tests.
var procSys = "/proc/sys"

// userns checks that an unprivileged process may map a user namespace,
// which bwrap needs. Ubuntu 24.04 restricts it through AppArmor: Codex
// 0.160's sandbox then fails every command with "bwrap: setting up uid
// map: Permission denied" (T189), unless AppArmor gives its bwrap a
// userns profile. ok is false when /proc says nothing.
func userns() (Check, bool) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(procSys, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	c := Check{Group: "sandbox", Name: "user namespaces", Status: Warn}
	switch {
	case read("kernel/apparmor_restrict_unprivileged_userns") == "1":
		c.Detail = "restricted by AppArmor (kernel.apparmor_restrict_unprivileged_userns=1): bwrap fails with \"setting up uid map\", so the agents' sandbox can't start (a Codex thread's every command fails); allow them with sysctl kernel.apparmor_restrict_unprivileged_userns=0 or an AppArmor profile for bwrap"
	case read("kernel/unprivileged_userns_clone") == "0":
		c.Detail = "off (kernel.unprivileged_userns_clone=0): bwrap can't run, so the agents' sandbox can't start (a Codex thread's every command fails); sysctl kernel.unprivileged_userns_clone=1"
	case read("user/max_user_namespaces") == "0":
		c.Detail = "off (user.max_user_namespaces=0): bwrap can't run, so the agents' sandbox can't start (a Codex thread's every command fails)"
	case read("user/max_user_namespaces") == "":
		return c, false
	default:
		c.Status, c.Detail = OK, "an unprivileged process may create one (bwrap)"
	}
	return c, true
}
