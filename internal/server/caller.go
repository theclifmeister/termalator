package server

import (
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/pty"
)

// maxAncestors bounds the walk up the process tree.
const maxAncestors = 64

// whoIs says who the process pid is (docs/SPEC.md §11.1): an agent call
// if it descends from a hosted coordinator or thread session, a human
// call otherwise (a shell outside termalator, or a hosted shell session).
func (s *Server) whoIs(pid int) proto.CallerInfo {
	s.mu.Lock()
	byPID := make(map[int]SessionRecord, len(s.sessions))
	for id, sess := range s.sessions {
		byPID[sess.PID()] = s.records[id]
	}
	s.mu.Unlock()
	for i := 0; i < maxAncestors && pid > 1; i++ {
		if r, ok := byPID[pid]; ok {
			switch r.Role {
			case proto.RoleCoordinator, proto.RoleThread:
				return proto.CallerInfo{Kind: r.Role, Session: r.ID, Project: r.Project, Thread: r.Thread}
			}
			return proto.CallerInfo{Kind: "human", Session: r.ID}
		}
		ppid, err := pty.ParentPID(pid)
		if err != nil || ppid == pid {
			break
		}
		pid = ppid
	}
	return proto.CallerInfo{Kind: "human"}
}
