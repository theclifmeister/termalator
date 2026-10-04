package server

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/termalator/internal/agent"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/session"
)

// remoteName is the name the remote side lists a session under: its
// project, else its id.
func remoteName(r SessionRecord) string {
	if r.Project != "" {
		return r.Project
	}
	return r.ID
}

// remote turns a coordinator's remote control on or off in the running
// session (docs/SPEC.md §8.2): through the agent's in-session text when
// its manifest has one, else by resuming the agent with or without the
// flag. config.toml isn't touched; it decides only how a new coordinator
// starts.
func (s *Server) remote(p proto.SessionRemoteParams) (any, *proto.Error) {
	s.mu.Lock()
	sess, ok := s.sessions[p.ID]
	r := s.records[p.ID]
	if !ok {
		s.mu.Unlock()
		return nil, proto.Errorf(proto.ErrUnknownSession, "no session %s", p.ID)
	}
	if r.Role != proto.RoleCoordinator {
		s.mu.Unlock()
		return nil, proto.Errorf(proto.ErrRefused, "remote control is for coordinators; threads are reached through theirs")
	}
	rc := agent.RemoteControlOf(sess.Agent())
	if rc == nil {
		s.mu.Unlock()
		return nil, proto.Errorf(proto.ErrRefused, "%s has no remote control", r.Agent)
	}
	if st, _ := sess.AgentState(); st.RemoteKnown {
		r.RemoteControl = st.RemoteControl // the agent's own word
	}
	if r.RemoteControl == p.On {
		s.mu.Unlock()
		return proto.SessionRemoteResult{RemoteControl: p.On, How: proto.RemoteUnchanged}, nil
	}
	text, err := rc.Text(p.On, agent.LaunchSpec{Role: agent.Role(r.Role), SessionID: r.ID, AgentSID: r.AgentSessionID,
		Cwd: r.Cwd, RemoteControl: p.On, RemoteName: remoteName(r)})
	if err != nil {
		s.mu.Unlock()
		return nil, proto.Errorf(proto.ErrRefused, "agent %s: remote_control: %v", r.Agent, err)
	}
	if text == "" {
		// A restart ends the agent's turn or open dialog: only when idle.
		if st, _ := sess.AgentState(); st.State != agent.StateIdle {
			s.mu.Unlock()
			return nil, proto.Errorf(proto.ErrRefused, "the coordinator is %s; try again once it is idle", st.State)
		}
	}
	r.RemoteControl = p.On
	s.records[p.ID] = r
	sess.SetRemoteControl(p.On)
	if err := s.saveLocked(""); err != nil {
		s.log.Printf("sessions.json: %v", err)
	}
	if text != "" {
		s.mu.Unlock()
		var before int
		if d := rc.DisableDialog; d != nil && !p.On {
			before = screenCount(sess, d.Done)
		}
		if _, err := sess.Prompt(text); err != nil {
			s.setRemote(p.ID, !p.On)
			return nil, sessionError(p.ID, err)
		}
		s.log.Printf("session %s: remote control %v (in-session)", p.ID, p.On)
		if d := rc.DisableDialog; d != nil && !p.On {
			go s.answerDialog(sess, *d, before)
		}
		return proto.SessionRemoteResult{RemoteControl: p.On, How: proto.RemotePrompted}, nil
	}
	s.relaunch[p.ID] = p.On
	s.mu.Unlock()
	s.log.Printf("session %s: remote control %v: restarting the agent", p.ID, p.On)
	sess.SetCloseNote(proto.ClosedRestarting)
	sess.Stop(StopGrace)
	return proto.SessionRemoteResult{RemoteControl: p.On, How: proto.RemoteRestarted}, nil
}

// Timing of answerDialog: the pasted text waits for the agent to be idle;
// keys typed right after a dialog paints can be dropped (Claude).
const (
	dialogWait   = 2 * time.Minute
	dialogSettle = 700 * time.Millisecond
	dialogDone   = 10 * time.Second
)

// answerDialog answers the dialog the disable text opened (manifest
// remote_control.disable_dialog): once the screen shows it, it types the
// keys and waits for the done text to appear once more than before. If
// any of that fails, remote control is still on: the change is undone.
func (s *Server) answerDialog(sess *session.Session, d agent.RemoteDialog, before int) {
	id := sess.ID()
	fail := func(why string) {
		s.log.Printf("session %s: remote control off failed: %s", id, why)
		s.setRemote(id, true)
	}
	if !pollScreen(sess, dialogWait, func(t string) bool { return strings.Contains(t, d.Contains) }) {
		fail("no dialog showing " + strconv.Quote(d.Contains))
		return
	}
	time.Sleep(dialogSettle)
	if err := sess.Input([]byte(d.Keys)); err != nil {
		fail(err.Error())
		return
	}
	if !pollScreen(sess, dialogDone, func(string) bool { return screenCount(sess, d.Done) > before }) {
		fail("no " + strconv.Quote(d.Done) + " after the keys")
		return
	}
	s.log.Printf("session %s: remote control off (dialog answered)", id)
}

// screenCount counts text on the screen and in its scrollback.
func screenCount(sess *session.Session, text string) int {
	r, err := sess.Read(true)
	if err != nil {
		return 0
	}
	return strings.Count(r.Text, text)
}

// pollScreen waits until cond holds for the session's screen.
func pollScreen(sess *session.Session, timeout time.Duration, cond func(string) bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		r, err := sess.Read(false)
		if err != nil {
			return false
		}
		if cond(r.Text) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// setRemote undoes a remote control change that failed.
func (s *Server) setRemote(id string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[id]; ok {
		r.RemoteControl = on
		s.records[id] = r
		if sess := s.sessions[id]; sess != nil {
			sess.SetRemoteControl(on)
		}
		if err := s.saveLocked(""); err != nil {
			s.log.Printf("sessions.json: %v", err)
		}
	}
}

// relaunchLocked starts an agent session again under the same id, after
// remote stopped it: resumed, or fresh if it was never prompted (there
// is nothing to resume then). ok is false when it couldn't. s.mu held.
func (s *Server) relaunchLocked(old *session.Session, remote bool) (ok bool) {
	id := old.ID()
	r := s.records[id]
	r.RemoteControl = remote
	info := old.Info()
	l := agentLaunch{rec: r, resume: r.Prompted, cols: info.Cols, rows: info.Rows}
	if !r.Prompted {
		l.kick, l.rec.AgentSessionID = r.Kickoff, newUUID()
	}
	if _, perr := s.launchAgent(l); perr != nil {
		s.log.Printf("session %s: relaunch failed: %v", id, perr)
		os.RemoveAll(s.runtimeDir(id))
		delete(s.records, id)
		if err := s.saveLocked(""); err != nil {
			s.log.Printf("sessions.json: %v", err)
		}
		return false
	}
	return true
}

// agentOr is the loaded agent of that name, or nil. s.mu held.
func (s *Server) agentOr(name string) agent.Agent {
	if s.agents == nil {
		return nil
	}
	a, _ := s.agents.Get(name)
	return a
}
