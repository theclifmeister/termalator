package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/theclifmeister/termilator/internal/home"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/service"
)

const serviceUsage = `usage: tm server service install|uninstall [--print]`

// serviceConfig is the service for this tm and this user. serviceRun,
// when set (tests), replaces launchctl and systemctl.
var serviceRun func(name string, args ...string) error

func serviceConfig(e *Env) (service.Config, error) {
	bin, err := os.Executable()
	if err != nil {
		return service.Config{}, err
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return service.Config{}, err
	}
	p, err := server.ResolvePaths()
	if err != nil {
		return service.Config{}, err
	}
	c := service.Config{
		GOOS: runtime.GOOS, UID: os.Getuid(), UserHome: userHome, Bin: bin,
		LogDir: filepath.Dir(p.Log), Path: e.Getenv("PATH"), Run: serviceRun,
	}
	if e.Getenv(home.Env) != "" {
		c.Home = p.Home
	}
	return c, nil
}

// serverService implements `tm server service install|uninstall`
// (docs/SPEC.md §3.1): optional start at login, off by default.
func serverService(e *Env, args []string) int {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		return e.srvUsage("server service", serviceUsage)
	}
	fs := flag.NewFlagSet("server service "+args[0], flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	print := fs.Bool("print", false, "print the service file instead of installing it")
	fs.BoolVar(print, "dry-run", false, "same as --print")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return e.srvUsage("server service", serviceUsage)
	}
	c, err := serviceConfig(e)
	if err != nil {
		return e.srvFail("server service", err)
	}
	path, err := c.File()
	if err != nil {
		return e.srvFail("server service", err)
	}
	if *print {
		if args[0] == "uninstall" {
			fmt.Fprintf(e.Stdout, "would unload and remove %s\n", path)
			return ExitOK
		}
		data, err := c.Render()
		if err != nil {
			return e.srvFail("server service", err)
		}
		fmt.Fprintf(e.Stdout, "# %s\n%s", path, data)
		return ExitOK
	}
	if args[0] == "install" {
		if _, err := c.Install(); err != nil {
			return e.srvFail("server service install", err)
		}
		fmt.Fprintf(e.Stdout, "installed %s; the server now starts at login\n", path)
		return ExitOK
	}
	if _, err := c.Uninstall(); err != nil {
		return e.srvFail("server service uninstall", err)
	}
	fmt.Fprintf(e.Stdout, "removed %s; the server starts on demand again\n", path)
	return ExitOK
}
