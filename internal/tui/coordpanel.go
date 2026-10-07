package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/tasks"
	"github.com/theclifmeister/terminatr/internal/ticker"
)

// The coordinator's info panel (docs/SPEC.md §4, T90): beside a
// coordinator's pane, the info panel shows its project as the /tm mod
// pane does, from the same state (server.ProjectWatchOf, what
// project.watch sends): what needs the user first, then the inbox, the
// threads, the coordinator's context use and the tasks on deck. Its
// width, toggle and keyboard are the thread panel's (infopanel.go). It
// has no buttons: a click on a task opens the task view over the
// session, on a running thread shows that thread's pane, on a PR opens
// it in the browser; the user asks the coordinator for the rest.

// coordReady bounds the tasks on deck the panel lists, as /tm does.
const coordReady = 5

// loadCoordInfo reads the panel's data for coordinator session s, with
// the poll's sessions.
func loadCoordInfo(paths server.Paths, s proto.SessionInfo, sessions []proto.SessionInfo) *infoData {
	d := &infoData{slug: s.Project, session: s}
	p, err := project.Open(s.Project)
	if err != nil {
		d.watch = &proto.ProjectWatch{Project: s.Project}
		return d
	}
	ts := ""
	if paths.Sessions != "" {
		ts = ticker.StatePath(paths.Sessions)
	}
	w := server.ProjectWatchOf(p, sessions, ts)
	d.watch = &w
	return d
}

// whyWords say why a need waits, short, as /tm says it.
var whyWords = map[string]string{
	proto.WhyQueue:    "prompts held",
	proto.WhyReview:   "in review",
	proto.WhyQuestion: "asks you",
	proto.WhyCI:       "checks failed",
	proto.WhyBlocked:  "blocked",
}

// whyState is the state whose glyph a need's words get.
var whyState = map[string]string{
	proto.WhyQueue:    "blocked",
	proto.WhyReview:   "review",
	proto.WhyQuestion: "blocked",
	proto.WhyCI:       "blocked",
	proto.WhyBlocked:  "blocked",
}

// coordLines draws a coordinator's panel w cells wide, with what a click
// on each line does.
func coordLines(pw *proto.ProjectWatch, w int, now time.Time) ([]string, []infoHit) {
	pl := &panel{w: w}
	var hits []infoHit
	// line adds text as one line, h what a click on it does.
	line := func(text string, h infoHit) {
		pl.add(text)
		for len(hits) < len(pl.lines) {
			hits = append(hits, h)
		}
	}
	// wrapped adds lead then text wrapped under it, all lines doing h.
	wrapped := func(lead, text string, h infoHit) {
		hang(pl, lead, text)
		for len(hits) < len(pl.lines) {
			hits = append(hits, h)
		}
	}
	// item adds an item's text wrapped, its lines after the first
	// indented as its detail lines are (docs/STYLE.md S3), all doing h.
	item := func(text string, h infoHit) {
		for i, l := range strings.Split(ansi.Wordwrap(text, max(pl.w-4, 8), ""), "\n") {
			if i > 0 {
				l = "  " + l
			}
			pl.add(l)
		}
		for len(hits) < len(pl.lines) {
			hits = append(hits, h)
		}
	}
	gap := func() {
		pl.gap()
		for len(hits) < len(pl.lines) {
			hits = append(hits, infoHit{})
		}
	}

	line(styleTitle.Render(oneLine(pw.Project)), infoHit{})
	line(styleFaint.Render(coordSummary(pw)), infoHit{})
	if tk := pw.Ticker; tk != nil {
		look := styleFaint
		if tk.GHFailing {
			look = styleBad
		}
		wrapped("", look.Render(tk.Line(now)), infoHit{})
	}
	if c := pw.Context; c != nil {
		text, look, hint := contextWords(c)
		line(look.Render(text), infoHit{})
		if hint != "" {
			wrapped("", look.Render(hint), infoHit{})
		}
	}

	// What waits for the user.
	// heading starts a section.
	heading := func(title string) {
		pl.section(title, "")
		for len(hits) < len(pl.lines) {
			hits = append(hits, infoHit{})
		}
	}
	if len(pw.NeedsYou) == 0 {
		gap()
		line(styleFaint.Render("Nothing waits for you."), infoHit{})
	} else {
		heading("NEEDS YOU")
	}
	for _, n := range pw.NeedsYou {
		h := taskHit(n.Task)
		// What went wrong in red, what waits on the user in yellow
		// (docs/STYLE.md, colour roles), as the /tm pane draws them.
		look := styleBad
		if n.Why == proto.WhyReview || n.Why == proto.WhyQuestion {
			look = styleWarn
		}
		ref := n.Task
		if ref == "" {
			ref = n.Thread
		}
		if ref == "" {
			ref = n.Session
		}
		g, _ := stateLook(whyState[n.Why])
		item(styleHead.Render(ref)+" "+look.Render(strings.TrimSpace(g+" "+whyWords[n.Why]))+" "+oneLine(n.Title), h)
		switch {
		case n.Why == proto.WhyQueue:
			what := "the coordinator hears of nothing"
			if n.Thread != "" {
				what = "the thread gets nothing"
			}
			wrapped("  ", styleBad.Render(what+" until it clears; tm agent explain "+n.Session+" says why"), infoHit{})
		case n.Question != "":
			wrapped("  ", styleWarn.Render("“"+oneLine(n.Question)+"”"), h)
		case n.PR != "":
			look := styleFaint
			if n.Why == proto.WhyCI || prBad(n.PR) {
				look = styleBad
			}
			ph := h
			if n.PRURL != "" {
				ph = infoHit{kind: hitPR, url: n.PRURL}
			}
			wrapped("  ", look.Render(n.PR), ph)
		}
		if n.Asked != "" {
			line(styleFaint.Render("  asked the coordinator: "+kindWord(n.Asked)), h)
		}
	}

	// The inbox, a row per kind per subject.
	if len(pw.Inbox) > 0 {
		heading("INBOX")
	}
	for _, it := range pw.Inbox {
		var parts []string
		if it.Count > 1 {
			parts = append(parts, "×"+strconv.Itoa(it.Count))
		}
		for _, s := range []string{it.Task, oneLine(it.What)} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		text := strings.Join(parts, " ")
		if it.Title != "" {
			text += styleFaint.Render(" " + oneLine(it.Title))
		}
		line(kindStyle(it.Kind).Render(kindWord(it.Kind))+" "+text, taskHit(it.Task))
	}

	// The threads, compact: a click on a running one shows its pane.
	if len(pw.Threads) > 0 {
		heading("THREADS")
	}
	for _, t := range pw.Threads {
		state := watchThreadState(t)
		g, st := stateLook(state)
		if state == "asks you" {
			g, st = stateLook("blocked")
		}
		text := t.ID
		if t.Task != nil {
			text += " " + t.Task.ID
		}
		text += " " + st.Render(strings.TrimSpace(g+" "+state))
		if t.Task != nil && t.Task.StepsTotal > 0 {
			text += fmt.Sprintf(" %d/%d", t.Task.StepsDone, t.Task.StepsTotal)
		}
		var h infoHit
		if t.Session != "" && !t.Done {
			h = infoHit{kind: hitSession, session: t.Session}
		} else {
			text = styleFaint.Render(text)
		}
		line(text, h)
		switch {
		case t.Task != nil && t.Task.Current != "":
			line(styleFaint.Render("  ▸ ")+oneLine(t.Task.Current), h)
		case t.Task != nil:
			line(styleFaint.Render("  "+oneLine(t.Task.Title)), h)
		default:
			line(styleFaint.Render("  "+oneLine(t.Title)), h)
		}
		if t.PR != "" {
			look := styleFaint
			if t.PRBad {
				look = styleBad
			}
			ph := h
			if t.PRURL != "" {
				ph = infoHit{kind: hitPR, url: t.PRURL}
			}
			pr := t.PR
			if c := proto.CheckedWords(t.PRChecked, now); c != "" {
				pr += " · " + c
			}
			wrapped("  ", look.Render(pr), ph)
		}
	}

	// The tasks on deck.
	if len(pw.Ready) > 0 {
		heading("ON DECK")
	}
	for i, t := range pw.Ready {
		if i == coordReady {
			line(styleFaint.Render(fmt.Sprintf("  and %d more", len(pw.Ready)-coordReady)), infoHit{})
			break
		}
		g, st := stateLook(t.Status)
		text := st.Render(strings.TrimSpace(g+" "+t.Status)) + " " + oneLine(t.Title)
		if t.Asked != "" {
			text += styleFaint.Render(" · asked the coordinator: " + kindWord(t.Asked))
		}
		item(styleHead.Render(t.Task)+" "+text, taskHit(t.Task))
	}
	for len(hits) < len(pl.lines) {
		hits = append(hits, infoHit{})
	}
	return pl.lines, hits
}

// coordSummary is the panel's second line: "3 need you · 1 in inbox · 5
// threads", as /tm's title says it.
func coordSummary(pw *proto.ProjectWatch) string {
	n := len(pw.NeedsYou)
	parts := []string{fmt.Sprintf("%d need%s you", n, plural(n == 1, "s"))}
	if len(pw.Inbox) > 0 {
		parts = append(parts, fmt.Sprintf("%d in inbox", len(pw.Inbox)))
	}
	parts = append(parts, fmt.Sprintf("%d thread%s", len(pw.Threads), plural(len(pw.Threads) != 1, "s")))
	return strings.Join(parts, " · ")
}

func plural(on bool, s string) string {
	if on {
		return s
	}
	return ""
}

// contextWords is the coordinator's context use, "context 84k / 200k ·
// 42%", its look (faint below the threshold, yellow from it, red from
// 80%, as the sidebar's), and the /clear hint past the threshold.
func contextWords(c *proto.WatchContext) (text string, look lipgloss.Style, hint string) {
	text = fmt.Sprintf("context %s / %s · %d%%", kTokens(c.Tokens), kTokens(c.Window), c.Percent)
	switch {
	case c.Percent >= 80:
		look = styleBad
	case c.Hint:
		look = styleWarn
	default:
		look = styleFaint
	}
	if c.Hint {
		hint = "consider /clear: the context lives in files (tm context)"
	}
	return text, look, hint
}

// kTokens writes a token count short: 840, 84k, 1.2M.
func kTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return strconv.FormatInt(n, 10)
}

// watchThreadState is a thread's state in a word, as /tm says it:
// "done" once it called tm done, "stopped" with no session, "asks" for a
// question.
func watchThreadState(t proto.WatchThread) string {
	switch {
	case t.Done:
		return "done"
	case t.Session == "":
		return "stopped"
	case t.State == "blocked" && t.Reason == "question":
		return "asks you"
	case t.State != "":
		return t.State
	}
	return "running"
}

// prBad reports a PR the thread has to act on, in the ticker's words.
func prBad(pr string) bool {
	for _, w := range []string{"failed", "conflicts", "changes requested", "behind"} {
		if strings.Contains(pr, w) {
			return true
		}
	}
	return false
}

// taskHit opens task ref ("T12") in the task view; none for "".
func taskHit(ref string) infoHit {
	if id, ok := tasks.ParseRef(ref); ok {
		return infoHit{kind: hitTask, task: id}
	}
	return infoHit{}
}
