package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/theclifmeister/termilator/internal/doctor"
	"github.com/theclifmeister/termilator/internal/emu"
	"github.com/theclifmeister/termilator/internal/legacy"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/update"
	"github.com/theclifmeister/termilator/internal/version"
)

const doctorUsage = `usage: tm doctor [--fix [--yes]] [--json]`

func init() {
	commands["doctor"] = func(e *Env, args []string) error { return codeErr(doctorCmd(e, args)) }
}

// doctorCmd implements `tm doctor [--fix]` (docs/SPEC.md §15 M8). It
// never starts the server and removes nothing without --fix and a
// confirmation (a y on a TTY, or --yes).
func doctorCmd(e *Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	fix := fs.Bool("fix", false, "remove what the checks found left over, after confirmation")
	yes := fs.Bool("yes", false, "with --fix: don't ask")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return e.srvUsage("doctor", doctorUsage)
	}
	p, err := server.ResolvePaths()
	if err != nil {
		return e.srvFail("doctor", err)
	}
	d := doctor.DefaultDeps(p, version.Version, version.BuildID())
	d.Selftest = libghosttySelftest
	exe, _ := os.Executable()
	in := update.Detect(exe, version.Channel)
	d.Install = &in
	d.Latest = func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := update.NewClient(e.Getenv).Latest(ctx)
		return r.Tag, err
	}
	d.Restart = func() error {
		// The same path as tm server restart, which never refuses on
		// version (docs/SPEC.md §3.3).
		if code := serverCmd(e, []string{"restart", "--yes"}); code != ExitOK {
			return fmt.Errorf("tm server restart exited %d", code)
		}
		return nil
	}
	d.UserHome, _ = os.UserHomeDir()
	d.LegacyRestart = func() error {
		old, nu, ok := legacy.Pending()
		if !ok {
			return nil
		}
		if code := e.legacyStop("restart", []string{"--yes"}, old, nu); code != ExitOK {
			return fmt.Errorf("tm server restart exited %d", code)
		}
		return nil
	}
	d.ServiceRun = func(name string, args ...string) error {
		if serviceRun != nil {
			return serviceRun(name, args...)
		}
		return exec.Command(name, args...).Run()
	}
	d.InstallService = func() error {
		c, err := serviceConfig(e)
		if err != nil {
			return err
		}
		_, err = c.Install()
		return err
	}
	checks := doctor.Run(d)
	fixes := doctor.Fixes(checks)
	code := ExitOK
	if doctor.Worst(checks) == doctor.Fail {
		code = ExitRefused
	}
	if *asJSON && !*fix {
		if e.srvJSON(map[string]any{"status": doctor.Worst(checks), "checks": checks}) != ExitOK {
			return ExitIO
		}
		return code
	}
	printChecks(e, checks)
	if len(fixes) == 0 {
		return code
	}
	if !*fix {
		fmt.Fprintf(e.Stdout, "\n%d thing(s) can be fixed: tm doctor --fix\n", len(fixes))
		return code
	}
	fmt.Fprintln(e.Stdout, "\ntm doctor --fix will:")
	for _, f := range fixes {
		fmt.Fprintf(e.Stdout, "  - %s\n", f.Desc)
	}
	if !*yes {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(e.Stderr, "tm doctor: not a terminal; pass --yes to do these")
			return ExitRefused
		}
		fmt.Fprint(e.Stderr, "Do them? [y/N] ")
		line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(e.Stdout, "nothing done")
			return ExitRefused
		}
	}
	for _, f := range fixes {
		if err := f.Apply(); err != nil {
			fmt.Fprintf(e.Stdout, "  fail  %s: %v\n", f.Desc, err)
			code = ExitRefused
			continue
		}
		fmt.Fprintf(e.Stdout, "  done  %s\n", f.Desc)
	}
	return code
}

func printChecks(e *Env, checks []doctor.Check) {
	group := ""
	counts := map[doctor.Status]int{}
	for _, c := range checks {
		if c.Group != group {
			group = c.Group
			fmt.Fprintln(e.Stdout, group)
		}
		counts[c.Status]++
		fmt.Fprintf(e.Stdout, "  %-4s  %-14s %s\n", c.Status, c.Name, c.Detail)
	}
	fmt.Fprintf(e.Stdout, "\n%d ok, %s, %s\n", counts[doctor.OK], plural(counts[doctor.Warn], "warning"), plural(counts[doctor.Fail], "failure"))
}

// libghosttySelftest feeds a line through the emulator, as tm selftest.
func libghosttySelftest() error {
	t, err := emu.New(80, 24)
	if err != nil {
		return err
	}
	defer t.Close()
	if _, err := t.Write([]byte("ok\r\n")); err != nil {
		return err
	}
	got, err := t.PlainText()
	if err != nil {
		return err
	}
	if got != "ok" {
		return fmt.Errorf("unexpected screen %q", got)
	}
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
