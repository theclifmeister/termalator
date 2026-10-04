package server

import (
	"sort"
	"strings"

	"github.com/theclifmeister/termalator/internal/project"
	"github.com/theclifmeister/termalator/internal/proto"
)

// KindServer is the inbox kind of the item a restart writes
// (docs/SPEC.md §3.6).
const KindServer = "server"

// restartOutcome is what became of one session of the previous server.
type restartOutcome struct {
	rec SessionRecord
	how string // "resumed", "fresh" (relaunched with no conversation) or "lost"
}

// restartLabel names a session in the restart item: the thread id, the
// coordinator, "<agent> <id>" for a plain agent session, or "<role> <id>".
func restartLabel(r SessionRecord) string {
	switch {
	case r.Thread != "":
		return r.Thread
	case r.Role == proto.RoleCoordinator:
		return "coordinator"
	case r.Agent != "" && (r.Role == proto.RoleShell || r.Role == ""):
		return r.Agent + " " + r.ID
	case r.Role == "":
		return "session " + r.ID
	}
	return r.Role + " " + r.ID
}

// restartSummary is one project's line: "server restarted after crash;
// resumed coordinator, t-0003; started fresh t-0005; lost shell s-12".
// Sessions with no project (plain shells) are listed in every project's
// item, so nothing is reported nowhere.
func restartSummary(prevShut string, outs []restartOutcome) string {
	by := map[string][]string{}
	for _, o := range outs {
		by[o.how] = append(by[o.how], restartLabel(o.rec))
	}
	head := "server restarted"
	if prevShut == "crash" {
		head = "server restarted after crash"
	}
	parts := []string{head}
	for _, k := range []struct{ how, verb string }{{"resumed", "resumed"}, {"fresh", "started fresh"}, {"lost", "lost"}} {
		if l := by[k.how]; len(l) > 0 {
			parts = append(parts, k.verb+" "+strings.Join(l, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

// reportRestart writes a "server" inbox item to every project that had a
// session on the previous server; the coordinator decides what to
// re-prompt.
func (s *Server) reportRestart(prevShut string, outs []restartOutcome) {
	byProject := map[string][]restartOutcome{}
	var loose []restartOutcome
	for _, o := range outs {
		if o.rec.Project == "" {
			loose = append(loose, o)
			continue
		}
		byProject[o.rec.Project] = append(byProject[o.rec.Project], o)
	}
	slugs := make([]string, 0, len(byProject))
	for slug := range byProject {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		p, err := project.Open(slug)
		if err != nil {
			s.log.Printf("restart item for %s: %v", slug, err)
			continue
		}
		summary := restartSummary(prevShut, append(byProject[slug], loose...))
		if _, err := p.AddItem(KindServer, "restart", summary, false); err != nil {
			s.log.Printf("restart item for %s: %v", slug, err)
			continue
		}
		s.log.Printf("project %s: %s", slug, summary)
	}
	if len(slugs) == 0 && len(loose) > 0 {
		s.log.Printf("%s", restartSummary(prevShut, loose))
	}
}
