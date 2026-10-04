package server

import (
	"strings"

	"github.com/theclifmeister/termalator/internal/proto"
)

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

// restartSummary is the log line: "server restarted after crash;
// resumed coordinator, t-0003; started fresh t-0005; lost shell s-12".
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

// logRestart logs what became of each session of the previous server,
// by name. The per-project inbox item is the ticker's (ServerRestarted).
func (s *Server) logRestart(prevShut string, outs []restartOutcome) {
	if len(outs) > 0 {
		s.log.Printf("%s", restartSummary(prevShut, outs))
	}
}
