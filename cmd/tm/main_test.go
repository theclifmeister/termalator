package main

import (
	"os"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/cli"
)

// TestSPECListsEveryCommand keeps docs/SPEC.md §10 in step with the
// commands tm dispatches: each one needs a row of its own there.
func TestSPECListsEveryCommand(t *testing.T) {
	b, err := os.ReadFile("../../docs/SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(b)
	start := strings.Index(spec, "\n## 10. The `tm` CLI")
	if start < 0 {
		t.Fatal("docs/SPEC.md has no §10")
	}
	section := spec[start+1:]
	if end := strings.Index(section, "\n## 11."); end >= 0 {
		section = section[:end]
	}
	names := append(append([]string{}, own...), cli.Commands()...)
	names = append(names, "help")
	for _, name := range names {
		if !strings.Contains(section, "`tm "+name) {
			t.Errorf("docs/SPEC.md §10 has no `tm %s …` row", name)
		}
	}
}

// TestUsageListsEveryCommand checks the usage line names every command.
func TestUsageListsEveryCommand(t *testing.T) {
	u := usage()
	for _, name := range append(append([]string{}, own...), cli.Commands()...) {
		if !strings.Contains(u, " "+name) {
			t.Errorf("usage doesn't name %q:\n%s", name, u)
		}
	}
}
