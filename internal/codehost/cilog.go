package codehost

import (
	"regexp"
	"strings"
)

// The failing job's log, put in the checks-failed prompt (docs/SPEC.md
// §7.5) so the thread starts fixing without a round-trip to the host.
const (
	// logBytes caps the excerpt; logLead is how many lines before the
	// first error it starts.
	logBytes = 3000
	logLead  = 8
	// logRuns bounds the failed runs asked for their log.
	logRuns = 3
)

var (
	ansiRE  = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	stampRE = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[\d:.]+Z ?`)
	errRE   = regexp.MustCompile(`(?i)(^|[^a-z])(error|fail(ed|ure)?|panic|fatal|--- FAIL|exit code [1-9])([^a-z]|$)`)
)

// excerpt reads `gh run view --log-failed` output (lines of
// "job<TAB>step<TAB>timestamp text") and answers the first failing job's
// name and its lines from logLead before the first error on, at most
// logBytes (the tail when no line looks like an error).
func excerpt(log string) (job, text string) {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		parts := strings.SplitN(l, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		if job == "" {
			job = clean(parts[0])
			if job == "" {
				return "", ""
			}
		}
		if clean(parts[0]) != job {
			break // only the first failing job
		}
		lines = append(lines, clean(stampRE.ReplaceAllString(ansiRE.ReplaceAllString(parts[2], ""), "")))
	}
	if len(lines) == 0 {
		return "", ""
	}
	first := -1
	for i, l := range lines {
		if errRE.MatchString(l) {
			first = i
			break
		}
	}
	if first >= 0 {
		text = strings.Join(lines[max(first-logLead, 0):], "\n")
		if len(text) > logBytes {
			text = strings.ToValidUTF8(text[:logBytes], "") + "\n[... cut]"
		}
	} else {
		// no line looks like an error: the tail is what is left
		text = strings.Join(lines, "\n")
		if len(text) > logBytes {
			text = "[... cut]\n" + strings.ToValidUTF8(text[len(text)-logBytes:], "")
		}
	}
	return job, strings.ReplaceAll(strings.TrimSpace(text), "```", "'''")
}

// clean drops control characters from one line.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}
