// Package skill holds the standing rules of each agent role, embedded in
// tm and versioned with it (docs/SPEC.md §7.7–7.8). Role files and briefs
// only point at `tm skill <role>`, so the rules can't drift from the
// binary.
package skill

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed rules/*.md
var rules embed.FS

// Text returns the rules of a role. The first line is
// "tm skill <role> v<version>".
func Text(role, version string) (string, bool) {
	body, err := rules.ReadFile("rules/" + role + ".md")
	if err != nil || strings.ContainsAny(role, "/.") {
		return "", false
	}
	return fmt.Sprintf("tm skill %s v%s\n\n%s", role, strings.TrimPrefix(version, "v"), body), true
}
