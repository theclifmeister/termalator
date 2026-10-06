package agent

import (
	"regexp"
	"strconv"
	"strings"
)

var versionRE = regexp.MustCompile(`\d+(\.\d+)*`)

// ParseVersion finds the first dotted version number in s, e.g.
// "2.1.291" in "2.1.291 (Claude Code)"; "" when there is none.
func ParseVersion(s string) string { return versionRE.FindString(s) }

// VersionAtLeast reports whether version v is min or later, comparing
// dotted numbers part by part (a missing part is 0). A v without a
// number is never at least anything.
func VersionAtLeast(v, min string) bool {
	a, b := versionParts(ParseVersion(v)), versionParts(ParseVersion(min))
	if len(a) == 0 {
		return false
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := part(a, i), part(b, i)
		if x != y {
			return x > y
		}
	}
	return true
}

func versionParts(v string) []int {
	if v == "" {
		return nil
	}
	var out []int
	for _, m := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(m)
		out = append(out, n)
	}
	return out
}

func part(p []int, i int) int {
	if i < len(p) {
		return p[i]
	}
	return 0
}
