package cli

import (
	"fmt"

	"github.com/theclifmeister/termilator/internal/legacy"
	"github.com/theclifmeister/termilator/internal/server"
)

// legacyGate moves a Termalator (v0.1.0) state directory to
// ~/.termilator before any command runs (internal/legacy). While a
// Termalator server still holds it, it is not moved: tm server stop and
// restart stop that server first and then move it, tm doctor reports it,
// and every other command is refused, so no second server starts beside
// the old one. Hooks are never held up. done says the gate answered the
// command itself.
func (e *Env) legacyGate(args []string) (code int, done bool) {
	if len(args) > 0 && args[0] == "hook" {
		return 0, false
	}
	old, nu, ok := legacy.Pending()
	if !ok {
		return 0, false
	}
	op := legacy.OldPaths(old)
	pid, running := legacy.OldServer(op)
	if !running {
		if err := e.legacyMove(old, nu); err != nil {
			return ExitIO, true
		}
		return 0, false
	}
	switch {
	case len(args) > 1 && args[0] == "server" && (args[1] == "stop" || args[1] == "restart"):
		return e.legacyStop(args[1], args[2:], old, nu), true
	case len(args) > 0 && args[0] == "doctor":
		return 0, false
	}
	fmt.Fprintf(e.Stderr, "tm: Termalator is now Termilator, and the Termalator server (pid %d) still runs on %s.\n"+
		"Run tm server restart: it stops that server, moves %s to %s and starts the new server; agents are resumed.\n", pid, old, old, nu)
	return ExitRefused, true
}

// legacyStop is tm server stop|restart while a Termalator server runs.
func (e *Env) legacyStop(cmd string, args []string, old, nu string) int {
	op := legacy.OldPaths(old)
	if code := serverStopAt(e, args, func() (server.Paths, error) { return op, nil }); code != ExitOK {
		return code
	}
	if err := e.legacyMove(old, nu); err != nil {
		return ExitIO
	}
	if cmd == "restart" {
		return serverStart(e, nil)
	}
	return ExitOK
}

func (e *Env) legacyMove(old, nu string) error {
	if err := legacy.Migrate(old, nu, e.Stderr); err != nil {
		fmt.Fprintf(e.Stderr, "tm: Termalator is now Termilator, but %s couldn't be moved to %s: %v\n", old, nu, err)
		return err
	}
	fmt.Fprintf(e.Stderr, "tm: Termalator is now Termilator: moved %s to %s\n", old, nu)
	return nil
}
