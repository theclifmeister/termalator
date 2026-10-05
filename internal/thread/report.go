package thread

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theclifmeister/termilator/internal/mdfile"
	"github.com/theclifmeister/termilator/internal/project"
)

// Limits of a report (docs/SPEC.md §7.2).
const (
	MaxReport   = 64 << 10
	MaxNextLine = 100
)

var prRE = regexp.MustCompile(`^PR: https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+$`)

// Report is a validated report.
type Report struct {
	PR       string   `json:"pr,omitempty"`
	Next     []string `json:"next"`
	Remember []string `json:"remember,omitempty"`
	Text     string   `json:"text"`
}

// Validate checks a report's format: an optional "PR: <url>" first line,
// "## Report", a required "## Next" with one action per line of at most
// 100 characters, and an optional "## Remember". Errors name the problem
// so the thread can fix it at once.
func Validate(text string) (*Report, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len(text) > MaxReport {
		return nil, refuse("invalid-report", "report is over %d bytes", MaxReport)
	}
	if !utf8.ValidString(text) {
		return nil, refuse("invalid-report", "report is not UTF-8")
	}
	if strings.TrimSpace(text) == "" {
		return nil, refuse("invalid-report", "empty report")
	}
	r := &Report{Text: strings.TrimSpace(text) + "\n"}
	lines := strings.Split(r.Text, "\n")
	section := ""
	seen := map[string]bool{}
	first := true
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if first && t != "" {
			first = false
			if strings.HasPrefix(t, "PR:") || strings.HasPrefix(t, "PR ") {
				if !prRE.MatchString(t) {
					return nil, refuse("invalid-report", "line %d: bad PR line; want PR: https://github.com/<owner>/<repo>/pull/<n>", i+1)
				}
				r.PR = strings.TrimSpace(strings.TrimPrefix(t, "PR:"))
				continue
			}
		}
		if h, ok := strings.CutPrefix(t, "## "); ok {
			section = strings.TrimSpace(h)
			if seen[section] && (section == "Report" || section == "Next" || section == "Remember") {
				return nil, refuse("invalid-report", "line %d: second ## %s", i+1, section)
			}
			seen[section] = true
			continue
		}
		if t == "" {
			continue
		}
		switch section {
		case "Next":
			n := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "- "), "* "))
			if utf8.RuneCountInString(n) > MaxNextLine {
				return nil, refuse("invalid-report", "line %d over %d chars", i+1, MaxNextLine)
			}
			r.Next = append(r.Next, n)
		case "Remember":
			r.Remember = append(r.Remember, t)
		}
	}
	switch {
	case !seen["Report"]:
		return nil, refuse("invalid-report", "missing ## Report")
	case !seen["Next"]:
		return nil, refuse("invalid-report", "missing ## Next")
	}
	if r.Next == nil {
		r.Next = []string{}
	}
	return r, nil
}

// ReadReport parses a thread's stored REPORT.md; nil when there is none.
func ReadReport(p *project.Project, id string) (*Report, error) {
	data, err := os.ReadFile(Path(p, id, "REPORT.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := Validate(string(data))
	if err != nil {
		// Stored reports were validated; one edited by hand still shows.
		return &Report{Text: string(data), Next: []string{}}, nil
	}
	return r, nil
}

// StoreReport validates text and stores it as REPORT.md, moving the
// previous one to reports/<n>.md, and copies attachments into library/
// (docs/SPEC.md §7.2). It returns the report's number.
func StoreReport(p *project.Project, id, text string, attach []string, now time.Time) (int, error) {
	if _, err := Validate(text); err != nil {
		return 0, err
	}
	for _, a := range attach {
		fi, err := os.Stat(a)
		if err != nil {
			return 0, refuse("invalid-attachment", "%v", err)
		}
		if !fi.Mode().IsRegular() {
			return 0, refuse("invalid-attachment", "%s is not a regular file", a)
		}
	}
	var n int
	_, err := Update(p, id, func(r *Record) error {
		if r.State == Resolved {
			return refuse("resolved", "thread %s is resolved", id)
		}
		if r.Reports > 0 {
			if err := os.MkdirAll(Path(p, id, "reports"), 0o755); err != nil {
				return err
			}
			prev := Path(p, id, "REPORT.md")
			if err := os.Rename(prev, Path(p, id, "reports", fmt.Sprintf("%d.md", r.Reports))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := mdfile.WriteAtomic(Path(p, id, "REPORT.md"), []byte(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))+"\n"), 0o644); err != nil {
			return err
		}
		for _, a := range attach {
			if err := copyFile(a, Path(p, id, "library", filepath.Base(a))); err != nil {
				return err
			}
		}
		r.Reports++
		r.ReportAt = now.UTC()
		n = r.Reports
		return nil
	})
	return n, err
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
