package doctor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/theclifmeister/termalator/internal/agent"
)

var versionRE = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// Agents loads the manifests as the server would and checks each agent's
// installed version against its tested_versions (docs/SPEC.md §14 #4).
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
	case !m.Tested(v):
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s %s is not in tested_versions %v%s; its status file and messaging socket are ignored, state comes from hooks and the screen",
			path, v, m.TestedVersions, src)
	default:
		c.Status, c.Detail = OK, fmt.Sprintf("%s %s (tested)%s", path, v, src)
	}
	return c
}

// Sandbox checks what Claude Code's sandbox needs, which holds a thread's
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
			Detail: "not found" + hint + "; Claude's sandbox can't run, so threads rely on permission rules alone"})
	}
	return out
}
