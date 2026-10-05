package doctor

import (
	"fmt"
	"path/filepath"

	"github.com/theclifmeister/termilator/internal/legacy"
)

// Legacy checks what a Termalator (v0.1.0) install left for Termilator:
// a state directory that a running Termalator server keeps from being
// moved, agent files generated under the old names, and the old login
// service. Remove with internal/legacy.
func Legacy(d Deps) []Check {
	const g = "rename"
	var out []Check
	if old, nu, ok := legacy.Pending(); ok {
		c := Check{Group: g, Name: "state", Status: Fail, Detail: fmt.Sprintf("%s isn't moved to %s yet", old, nu)}
		if pid, running := legacy.OldServer(legacy.OldPaths(old)); running {
			c.Detail = fmt.Sprintf("the Termalator server (pid %d) still runs on %s: tm server restart moves it to %s", pid, old, nu)
			if d.LegacyRestart != nil {
				c.Fix = &Fix{Desc: fmt.Sprintf("restart the server: stop the Termalator one, move %s to %s, start the new one", old, nu), Apply: d.LegacyRestart}
			}
		}
		out = append(out, c)
	}
	old, _, err := legacy.Homes()
	if err != nil {
		return out
	}
	if files := legacy.Stale(filepath.Join(d.Paths.RunDir, "s"), old); len(files) > 0 {
		out = append(out, Check{Group: g, Name: "agent files", Status: Warn,
			Detail: fmt.Sprintf("%d generated agent file(s) still name Termalator, e.g. %s", len(files), files[0]),
			Fix:    &Fix{Desc: fmt.Sprintf("rewrite %d agent file(s) for Termilator", len(files)), Apply: func() error { return legacy.Fix(files) }}})
	}
	if d.UserHome != "" {
		if path, ok := legacy.OldServiceFile(d.GOOS, d.UserHome); ok {
			c := Check{Group: g, Name: "service", Status: Warn, Detail: "the Termalator login service is installed: " + path}
			if d.ServiceRun != nil {
				c.Fix = &Fix{Desc: "replace the Termalator login service with the Termilator one", Apply: func() error {
					if err := legacy.RemoveOldService(d.GOOS, path, d.ServiceRun); err != nil {
						return err
					}
					if d.InstallService == nil {
						return nil
					}
					return d.InstallService()
				}}
			}
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
