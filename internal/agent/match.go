package agent

import (
	"fmt"
	"path/filepath"
	"strings"
)

// matches reports whether payload satisfies every pattern in want. Keys
// are dotted paths. Patterns:
//
//	"value"   the field equals value
//	"!value"  the field is absent or differs from value
//	"*"       the field is present and non-empty
//	"!*"      the field is absent or empty
func matches(want map[string]string, payload map[string]any) bool {
	for path, pat := range want {
		got, present := lookupString(payload, path)
		present = present && got != ""
		switch {
		case pat == "*":
			if !present {
				return false
			}
		case pat == "!*":
			if present {
				return false
			}
		case strings.HasPrefix(pat, "!"):
			if got == pat[1:] {
				return false
			}
		default:
			if got != pat {
				return false
			}
		}
	}
	return true
}

// lookup follows a dotted path through nested JSON objects.
func lookup(v map[string]any, path string) (any, bool) {
	var cur any = v
	for _, key := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// lookupString returns the value at path as a string. Numbers and booleans
// are formatted, because agents are not consistent about id types.
func lookupString(v map[string]any, path string) (string, bool) {
	x, ok := lookup(v, path)
	if !ok || x == nil {
		return "", false
	}
	switch t := x.(type) {
	case string:
		return t, true
	case float64:
		return fmt.Sprintf("%g", t), true
	case bool:
		return fmt.Sprint(t), true
	}
	return "", false
}

// FilterEnv removes variables named by patterns from env (KEY=VALUE
// entries). A pattern ending in * matches a prefix.
func FilterEnv(env, patterns []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, p := range patterns {
			if strings.HasSuffix(p, "*") && strings.HasPrefix(key, strings.TrimSuffix(p, "*")) || key == p {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// UnwrapArgv strips interpreters and shells from a process's argv, so an
// agent started as `node /usr/lib/node_modules/.bin/claude` or
// `sh -c "claude --resume x"` is seen as `claude …` (docs/SPEC.md §8.2).
func UnwrapArgv(argv []string) []string {
	for i := 0; i < 4 && len(argv) > 0; i++ {
		switch filepath.Base(argv[0]) {
		case "node", "bun", "deno":
			rest := argv[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				rest = rest[1:]
			}
			if len(rest) > 0 && rest[0] == "run" && filepath.Base(argv[0]) == "deno" {
				rest = rest[1:]
			}
			argv = rest
		case "sh", "bash", "zsh", "dash":
			if len(argv) >= 3 && argv[1] == "-c" {
				argv = strings.Fields(argv[2])
				if len(argv) > 0 && argv[0] == "exec" {
					argv = argv[1:]
				}
				continue
			}
			return argv
		default:
			return argv
		}
	}
	return argv
}
