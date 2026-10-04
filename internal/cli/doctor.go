package cli

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/theclifmeister/termalator/internal/doctor"
	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/server"
	"github.com/theclifmeister/termalator/internal/version"
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
		fmt.Fprintf(e.Stdout, "\n%d thing(s) can be removed: tm doctor --fix\n", len(fixes))
		return code
	}
	fmt.Fprintln(e.Stdout, "\ntm doctor --fix will:")
	for _, f := range fixes {
		fmt.Fprintf(e.Stdout, "  - %s\n", f.Desc)
	}
	if !*yes {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(e.Stderr, "tm doctor: not a terminal; pass --yes to remove these")
			return ExitRefused
		}
		fmt.Fprint(e.Stderr, "Remove them? [y/N] ")
		line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(e.Stdout, "nothing removed")
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
