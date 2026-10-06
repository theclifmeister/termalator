package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/theclifmeister/termilator/internal/mdfile"
)

// Writing settings (docs/SPEC.md §11.2). Only the TUI's settings popups
// call these, on the human's keypress, and the human-only tm project
// pause|resume|archive|unarchive: no socket method reaches them, so
// agents can't change safety settings. The file is
// edited line by line, so the user's comments, order and formatting stay
// as they were; the result is parsed before it replaces the file, which
// happens atomically under the file's lock.

// ErrForm: the file sets the setting in a form the line editor doesn't
// change (a dotted key, an inline table); the user edits it by hand.
var ErrForm = errors.New("this setting is set in a form tm can't change here")

// SetProject sets one of a project's safety settings by its key
// (start_threads, yolo, …), checked against the setting's type.
func SetProject(slug, key string, value any) error {
	return setSafety("projects."+slug, key, value)
}

// SetDefaults sets one of the all-projects settings ([defaults]): every
// project that doesn't set key itself follows it.
func SetDefaults(key string, value any) error {
	if slices.Contains(ProjectOnly, key) {
		return fmt.Errorf("%s is a project's own setting, not all projects'", key)
	}
	return setSafety(DefaultsTable, key, value)
}

// DefaultsTable is the all-projects settings' table.
const DefaultsTable = "defaults"

// UnsetProject removes a project's own value of key, so it follows all
// projects again; for auto_close the older auto_resolve goes too. ErrForm
// when the file sets it in a form the line editor doesn't change.
func UnsetProject(slug, key string) error {
	if !validKey(key) {
		return fmt.Errorf("unknown setting %q", key)
	}
	table := "projects." + slug
	keys := []string{key}
	if key == "auto_close" {
		keys = append(keys, "auto_resolve")
	}
	return edit(func(data []byte) ([]byte, error) {
		for _, k := range keys {
			out := Remove(data, table, k)
			if bytes.Equal(out, data) && sets(data, table, k) {
				return nil, ErrForm
			}
			data = out
		}
		return data, nil
	})
}

// sets reports whether data sets key in table, in any form.
func sets(data []byte, table, key string) bool {
	var got map[string]any
	if _, err := toml.Decode(string(data), &got); err != nil {
		return false
	}
	v := any(got)
	for _, k := range append(splitTable(table), key) {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if v, ok = m[k]; !ok {
			return false
		}
	}
	return true
}

// setSafety sets a safety setting in table, a project's or [defaults].
func setSafety(table, key string, value any) error {
	if !validKey(key) {
		return fmt.Errorf("unknown setting %q", key)
	}
	if key == "start_threads" && value != StartPropose && value != StartAuto {
		return fmt.Errorf("start threads must be %q or %q", StartPropose, StartAuto)
	}
	if key == "auto_close" && value != CloseOff && value != CloseMerged && value != CloseDays {
		return fmt.Errorf("auto-close must be %q, %q or %q", CloseOff, CloseMerged, CloseDays)
	}
	if key == "complete_tasks" && value != CompleteUser && value != CompleteMerged {
		return fmt.Errorf("complete tasks must be %q or %q", CompleteUser, CompleteMerged)
	}
	if n, ok := value.(int); key == "parallel_threads" && (!ok || n < 1 || n > MaxParallelThreads) {
		return fmt.Errorf("parallel threads must be 1 to %d", MaxParallelThreads)
	}
	if n, ok := value.(int); key == "auto_close_days" && (!ok || n < 1 || n > MaxAutoCloseDays) {
		return fmt.Errorf("auto-close days must be 1 to %d", MaxAutoCloseDays)
	}
	if key == "auto_close" {
		// auto_close replaces the older auto_resolve: its line goes in the
		// same write, so nothing obsolete is left ignored in the file.
		return edit(func(data []byte) ([]byte, error) {
			out, err := Edit(data, table, key, value)
			if err != nil {
				return nil, err
			}
			return Remove(out, table, "auto_resolve"), nil
		})
	}
	return Set(table, key, value)
}

// validKey reports whether key is a project setting.
func validKey(key string) bool {
	for _, k := range ProjectKeys {
		if k == key {
			return true
		}
	}
	return false
}

// ProjectKeys are the settings of a [projects.<slug>] table; all but
// ProjectOnly are also those of [defaults].
var ProjectKeys = []string{"start_threads", "yolo", "coordinator_approves", "parallel_threads", "auto_close", "auto_close_days", "auto_resolve", "pr_followup", "complete_tasks", "coordinator_remote_control", "fast_forward_checkout", "paused", "archived"}

// ProjectOnly are a project's own state, never all projects': a paused
// or archived [defaults] would stop or hide every project.
var ProjectOnly = []string{"paused", "archived"}

// Set sets key in table ("" is the top level, "keys", "projects.<slug>")
// to value: a bool, an int or a string.
func Set(table, key string, value any) error {
	return edit(func(data []byte) ([]byte, error) { return Edit(data, table, key, value) })
}

// edit rewrites the settings file with fn, under its lock and
// atomically, keeping its mode; an unchanged file isn't written.
func edit(fn func(data []byte) ([]byte, error)) error {
	path, err := Path()
	if err != nil {
		return err
	}
	unlock, err := mdfile.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	perm := fs.FileMode(0o600)
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if fi, err := os.Stat(path); err == nil {
			perm = fi.Mode().Perm()
		}
	}
	data, err := fn(old)
	if err != nil {
		return err
	}
	if bytes.Equal(old, data) {
		return nil
	}
	return mdfile.WriteAtomic(path, data, perm)
}

// Edit returns data with key in table set to value, everything else kept.
func Edit(data []byte, table, key string, value any) ([]byte, error) {
	val, err := tomlValue(value)
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), new(map[string]any)); err != nil {
		return nil, fmt.Errorf("the settings can't be read: %w", err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	want := splitTable(table)
	in := len(want) == 0 // the top level comes first
	found := in
	last := -1 // the region's last setting line, or its header
	var out []string
	done := false
	for i, l := range lines {
		if h, ok := header(l); ok {
			if in && !done {
				out = insertAt(out, last, key+" = "+val+"\n", len(out))
				done = true
			}
			in = equal(h, want)
			if in {
				found, last = true, len(out)
			}
			out = append(out, l)
			continue
		}
		if in && !done {
			if indent, comment, ok := setting(l, key); ok {
				nl := ""
				if strings.HasSuffix(l, "\n") || i < len(lines)-1 {
					nl = "\n"
				}
				out = append(out, indent+key+" = "+val+comment+nl)
				done = true
				continue
			}
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
				last = len(out)
			}
		}
		out = append(out, l)
	}
	if n := len(out); n > 0 && !strings.HasSuffix(out[n-1], "\n") {
		out[n-1] += "\n"
	}
	switch {
	case done:
	case found:
		out = insertAt(out, last, key+" = "+val+"\n", len(out))
	default:
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "\n")
		}
		out = append(out, "["+table+"]\n", key+" = "+val+"\n")
	}
	res := []byte(strings.Join(out, ""))
	// The edit must parse and say what was meant; else the file sets it
	// in another form (dotted keys, inline tables) and stays untouched.
	var got map[string]any
	if _, err := toml.Decode(string(res), &got); err != nil {
		return nil, ErrForm
	}
	v := any(got)
	for _, k := range append(want, key) {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, ErrForm
		}
		v = m[k]
	}
	if n, ok := value.(int); ok {
		value = int64(n)
	}
	if v != value {
		return nil, ErrForm
	}
	return res, nil
}

// Remove returns data without the line setting key in table, everything
// else kept: comments above it and the rest of the table too. When the
// file sets key in a form the line editor doesn't change (a dotted key,
// an inline table), or the result wouldn't parse without it, data comes
// back as it was.
func Remove(data []byte, table, key string) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	want := splitTable(table)
	in := len(want) == 0
	var out []string
	for _, l := range lines {
		if h, ok := header(l); ok {
			in = equal(h, want)
		} else if _, _, ok := setting(l, key); ok && in {
			continue
		}
		out = append(out, l)
	}
	res := []byte(strings.Join(out, ""))
	var got map[string]any
	if _, err := toml.Decode(string(res), &got); err != nil {
		return data
	}
	v := any(got)
	for _, k := range want {
		m, ok := v.(map[string]any)
		if !ok {
			return res // the table is gone: so is key
		}
		v = m[k]
	}
	if m, ok := v.(map[string]any); ok {
		if _, still := m[key]; still {
			return data
		}
	}
	return res
}

// insertAt inserts s after index i of lines (at the start for -1); at
// end appends.
func insertAt(lines []string, i int, s string, end int) []string {
	if i+1 >= end {
		return append(lines, s)
	}
	out := append([]string{}, lines[:i+1]...)
	out = append(out, s)
	return append(out, lines[i+1:]...)
}

func tomlValue(v any) (string, error) {
	switch v := v.(type) {
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case string:
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range v {
			switch {
			case r == '"' || r == '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, `\u%04X`, r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
		return b.String(), nil
	}
	return "", fmt.Errorf("can't write a %T setting", v)
}

var headerRE = regexp.MustCompile(`^\s*\[\s*([^\[\]#]+?)\s*\]\s*(#.*)?$`)

// header reads a [table] line (not an [[array]] one).
func header(l string) ([]string, bool) {
	m := headerRE.FindStringSubmatch(strings.TrimRight(l, "\r\n"))
	if m == nil {
		return nil, false
	}
	return splitTable(m[1]), true
}

// splitTable splits a table name at its dots, quotes and spaces removed.
// Slugs and our keys need no quoting, so a dot inside quotes isn't read.
func splitTable(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0] {
			p = p[1 : len(p)-1]
		}
		parts[i] = p
	}
	return parts
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// valueRE is a simple value with an optional comment after it.
var valueRE = regexp.MustCompile(`^\s*(true|false|[+-]?[0-9_]+|"(?:[^"\\]|\\.)*"|'[^']*')(\s*#.*)?$`)

// setting reads a "key = value" line for key: its indentation and the
// comment after the value, kept when the value is a simple one.
func setting(l, key string) (indent, comment string, ok bool) {
	t := strings.TrimRight(l, "\r\n")
	rest := strings.TrimLeft(t, " \t")
	indent = t[:len(t)-len(rest)]
	k, v, ok := strings.Cut(rest, "=")
	if !ok {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') && k[len(k)-1] == k[0] {
		k = k[1 : len(k)-1]
	}
	if k != key {
		return "", "", false
	}
	if m := valueRE.FindStringSubmatch(v); m != nil {
		comment = m[2]
	}
	return indent, comment, true
}

// ClearProject removes keys from a project's table, e.g. a deleted
// project's archived and paused, so a new project of the same slug
// starts without them.
func ClearProject(slug string, keys ...string) error {
	return edit(func(data []byte) ([]byte, error) {
		if data == nil {
			return nil, nil
		}
		for _, k := range keys {
			data = Remove(data, "projects."+slug, k)
		}
		return data, nil
	})
}
