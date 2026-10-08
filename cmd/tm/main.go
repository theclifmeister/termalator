// Command tm is terminatr: an agent session host, a dashboard and the CLI
// that coordinator and thread agents call. The commands are specified in
// docs/SPEC.md §10; this file dispatches them.
package main

import (
	"fmt"
	"os"
	"strings"

	// The Go parts of built-in agents register themselves.
	_ "github.com/theclifmeister/terminatr/internal/agent/claude"
	_ "github.com/theclifmeister/terminatr/internal/agent/codex"
	"github.com/theclifmeister/terminatr/internal/cli"
	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("tm", version.Version, "build", version.BuildID())
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "selftest" {
		if err := selftest(); err != nil {
			fmt.Fprintln(os.Stderr, "tm selftest:", err)
			os.Exit(1)
		}
		fmt.Println("libghostty-vt: ok")
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println(usage())
		return
	}
	if code, ok := cli.Run(os.Args[1:]); ok {
		os.Exit(code)
	}
	fmt.Fprintf(os.Stderr, "tm: unknown command %q\n%s\n", os.Args[1], usage())
	os.Exit(2)
}

// own are the commands main runs itself, before the cli package.
var own = []string{"version", "selftest"}

// usage lists every command, from cli's table, so it can't drift.
func usage() string {
	return "usage: tm [--own]   (the dashboard)\n" +
		"       tm <command> ...\n" +
		"commands: " + strings.Join(append(append([]string{}, own...), cli.Commands()...), " | ") + "\n" +
		"docs/SPEC.md §10 lists every command and flag"
}

// selftest feeds a line through the emulator to show libghostty-vt is linked.
func selftest() error {
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
