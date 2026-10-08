package doctor

import "github.com/theclifmeister/terminatr/internal/agent"

// Plugins runs the checks of each agent that has its own (agent.Doctor),
// such as Claude Code's known-unsafe plugins. Warnings only, with no fix.
func Plugins(d Deps) []Check {
	const g = "plugins"
	reg, _ := agent.Load(d.Paths.AgentsDir()) // Agents reports a broken manifest
	if reg == nil {
		return nil
	}
	var out []Check
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		dr, ok := a.(agent.Doctor)
		if !ok {
			continue
		}
		for _, c := range dr.DoctorChecks(d.LookPath, d.Run) {
			st := OK
			if c.Warn {
				st = Warn
			}
			out = append(out, Check{Group: g, Name: c.Name, Status: st, Detail: c.Detail})
		}
	}
	return out
}
