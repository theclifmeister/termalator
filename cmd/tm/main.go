// Command tm is termalator: an agent session host, a dashboard and the CLI
// that coordinator and thread agents call. This is a placeholder; the
// commands are specified in docs/SPEC.md.
package main

import (
	"fmt"
	"os"

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/version"
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
	fmt.Fprintln(os.Stderr, "tm: not implemented yet; see docs/SPEC.md")
	fmt.Fprintln(os.Stderr, "usage: tm version | tm selftest")
	os.Exit(2)
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
