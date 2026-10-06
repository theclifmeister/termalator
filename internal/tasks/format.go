package tasks

//lint:file-ignore ST1005 parse errors start with the task ID (T12: …)

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/theclifmeister/terminatr/internal/mdfile"
)

// The TASKS.md format (§6.2): TOML front matter with next_id, "# Tasks",
// one "## <group>" heading per non-empty group, and per task a
// "### T<n> <title>" heading, a metadata line straight after it, notes, and
// a trailing run of "- [ ]"/"- [x]" steps.

var (
	taskHeading = regexp.MustCompile(`^### T([0-9]+) (.*\S)\s*$`)
	stepLine    = regexp.MustCompile(`^- \[([ xX])\] (.*\S)\s*$`)
)

const sep = " · "

// maxID bounds task ids, so next_id can never overflow.
const maxID = 1_000_000_000

type front struct {
	NextID int `toml:"next_id"`
}

// ParseError is a TASKS.md line tm can't read. tm refuses to write the
// file until it is fixed (§5.1).
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

// isStructural reports whether a line is a heading the parser keys on.
func isStructural(line string) bool {
	return strings.HasPrefix(line, "# ") || line == "#" ||
		strings.HasPrefix(line, "## ") || line == "##" ||
		strings.HasPrefix(line, "### T") && taskHeading.MatchString(line)
}

// Parse reads TASKS.md or ARCHIVE.md. Empty input is an empty board.
func Parse(data []byte) (*Board, error) {
	var fm front
	frontRaw, body, err := mdfile.Split(data)
	if err != nil {
		return nil, &ParseError{Line: 1, Msg: err.Error()}
	}
	b := &Board{NextID: 1}
	lineNo := 0
	if frontRaw != nil {
		if _, err := mdfile.Decode(data, &fm); err != nil {
			return nil, &ParseError{Line: 1, Msg: err.Error()}
		}
		lineNo = strings.Count(string(frontRaw), "\n") + 2
		if fm.NextID > maxID+1 {
			return nil, &ParseError{Line: 1, Msg: fmt.Sprintf("next_id must be at most %d", maxID+1)}
		}
		if fm.NextID > 0 {
			b.NextID = fm.NextID
		}
	}

	lines := strings.Split(string(body), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r") // CRLF files read like LF ones
	}
	var cur *Task
	var curBody []string
	seen := map[int]int{}
	flush := func() {
		if cur != nil {
			cur.Notes, cur.Steps = splitBody(curBody)
			b.Tasks = append(b.Tasks, cur)
		}
		cur, curBody = nil, nil
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		n := lineNo + i + 1
		switch {
		case strings.HasPrefix(line, "### ") && cur == nil && !taskHeading.MatchString(line):
			return nil, &ParseError{n, fmt.Sprintf("expected a task heading \"### T<n> <title>\", got %q", line)}
		case taskHeading.MatchString(line):
			m := taskHeading.FindStringSubmatch(line)
			flush()
			id, err := strconv.Atoi(m[1])
			if err != nil || id <= 0 || id > maxID {
				return nil, &ParseError{n, fmt.Sprintf("task id must be between 1 and %d", maxID)}
			}
			if prev, dup := seen[id]; dup {
				return nil, &ParseError{n, fmt.Sprintf("T%d already defined on line %d", id, prev)}
			}
			seen[id] = n
			if i+1 >= len(lines) {
				return nil, &ParseError{n + 1, fmt.Sprintf("T%d has no \"status: …\" line", id)}
			}
			cur = &Task{ID: id, Title: m[2]}
			if err := parseMeta(cur, lines[i+1]); err != nil {
				return nil, &ParseError{n + 1, err.Error()}
			}
			i++
		case strings.HasPrefix(line, "## ") || line == "##" || strings.HasPrefix(line, "# ") || line == "#":
			flush()
		case cur != nil:
			curBody = append(curBody, line)
		case strings.TrimSpace(line) != "":
			return nil, &ParseError{n, fmt.Sprintf("text outside any task: %q", line)}
		}
	}
	flush()
	for _, t := range b.Tasks {
		if t.ID >= b.NextID {
			b.NextID = t.ID + 1
		}
	}
	return b, nil
}

func parseMeta(t *Task, line string) error {
	if !strings.HasPrefix(line, "status:") {
		return fmt.Errorf("T%d: expected \"status: …\" straight after the heading, got %q", t.ID, line)
	}
	for _, part := range strings.Split(line, "·") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			return fmt.Errorf("T%d: metadata %q is not \"key: value\"", t.ID, part)
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "status":
			st, ok := ParseStatus(v)
			if !ok || v == "start" {
				return fmt.Errorf("T%d: unknown status %q", t.ID, v)
			}
			t.Status = st
		case "owner":
			t.Owner = v
		case "thread":
			t.Thread = v
		case "created":
			t.Created = v
		case "updated":
			t.Updated = v
		default:
			return fmt.Errorf("T%d: unknown metadata key %q", t.ID, k)
		}
	}
	return nil
}

// splitBody separates notes from the trailing run of checklist items.
func splitBody(lines []string) (string, []Step) {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && stepLine.MatchString(lines[start-1]) {
		start--
	}
	var steps []Step
	for i, l := range lines[start:end] {
		m := stepLine.FindStringSubmatch(l)
		steps = append(steps, Step{N: i + 1, Text: m[2], Done: m[1] != " "})
	}
	return trimBlankLines(strings.Join(lines[:start], "\n")), steps
}

// trimBlankLines drops blank lines at both ends but keeps indentation,
// which can matter: "  # x" is text, "# x" is a heading.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// Render writes the board in canonical order with front matter, as
// TASKS.md.
func (b *Board) Render() []byte {
	b.Sort()
	var w bytes.Buffer
	fmt.Fprintf(&w, "+++\nnext_id = %d\n+++\n# Tasks\n", b.NextID)
	var g Group
	for _, t := range b.Tasks {
		if gg := GroupOf(t.Status); gg != g {
			g = gg
			fmt.Fprintf(&w, "\n## %s\n", g)
		}
		writeTask(&w, t)
	}
	return w.Bytes()
}

// RenderArchive writes the board by id, as tasks/ARCHIVE.md.
func (b *Board) RenderArchive() []byte {
	b.SortByID()
	var w bytes.Buffer
	w.WriteString("# Archive\n")
	for _, t := range b.Tasks {
		writeTask(&w, t)
	}
	return w.Bytes()
}

func writeTask(w *bytes.Buffer, t *Task) {
	fmt.Fprintf(w, "\n### %s %s\n", t.Ref(), t.Title)
	meta := []string{"status: " + string(t.Status)}
	for _, kv := range [][2]string{{"owner", t.Owner}, {"thread", t.Thread}, {"created", t.Created}, {"updated", t.Updated}} {
		if kv[1] != "" {
			meta = append(meta, kv[0]+": "+kv[1])
		}
	}
	w.WriteString(strings.Join(meta, sep) + "\n")
	if t.Notes != "" {
		w.WriteString("\n" + t.Notes + "\n")
	}
	if len(t.Steps) > 0 {
		w.WriteString("\n")
		for _, s := range t.Steps {
			mark := " "
			if s.Done {
				mark = "x"
			}
			fmt.Fprintf(w, "- [%s] %s\n", mark, s.Text)
		}
	}
}
