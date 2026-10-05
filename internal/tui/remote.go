package tui

import (
	"errors"
	"fmt"
	"time"

	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
)

// SetRemote turns remote control of a project's running coordinator on
// or off (docs/SPEC.md §8.2): tm project remote and prefix+r.
func SetRemote(call func(method string, params, result any) error, slug string, on bool) (proto.SessionRemoteResult, error) {
	var res proto.SessionRemoteResult
	var list proto.SessionListResult
	if err := call(proto.MethodSessionList, nil, &list); err != nil {
		return res, err
	}
	for _, s := range list.Sessions {
		if s.Role == proto.RoleCoordinator && s.Project == slug {
			err := call(proto.MethodSessionRemote, proto.SessionRemoteParams{ID: s.ID, On: on}, &res)
			return res, err
		}
	}
	return res, proto.Errorf(proto.ErrRefused, "no coordinator runs for %s; open the project first", slug)
}

// RemoteMessage says what SetRemote did, in the user's words: no setting
// names or files (the settings popup shows the setting itself).
func RemoteMessage(slug string, res proto.SessionRemoteResult) string {
	state := map[bool]string{true: "on", false: "off"}[res.RemoteControl]
	switch res.How {
	case proto.RemoteUnchanged:
		return fmt.Sprintf("remote control is already %s for %s", state, slug)
	case proto.RemoteRestarted:
		return fmt.Sprintf("remote control %s for %s: the coordinator restarted and continues its conversation; until it is started anew", state, slug)
	}
	if res.RemoteControl {
		return fmt.Sprintf("remote control on for %s: listed there as %q; until the coordinator is started anew", slug, slug)
	}
	return fmt.Sprintf("remote control off for %s; until the coordinator is started anew", slug)
}

// remoteQuestion asks, in the status bar, before prefix+r changes a
// coordinator's remote control.
func remoteQuestion(s proto.SessionInfo) string {
	if s.RemoteControl {
		return "turn remote control off for " + s.Project + "?"
	}
	return "turn remote control on for " + s.Project + ", so it can be continued from another device?"
}

// askRemote asks whether to turn the focused coordinator's remote control
// on or off (prefix+r).
func (c *client) askRemote() {
	if !c.lock() {
		return
	}
	if c.focus.info.Role == proto.RoleCoordinator {
		c.confirmRemote = c.focus
	} else {
		c.flash = "remote control is for coordinators"
	}
	c.status()
	c.mu.Unlock()
	c.poke()
}

// answerRemote turns p's remote control around on yes. c.mu held;
// released here.
func (c *client) answerRemote(p *pane, yes bool) {
	c.confirmRemote = nil
	yes = yes && !p.gone
	on, slug := !p.info.RemoteControl, p.info.Project
	if !yes {
		c.flash = "remote control unchanged"
	}
	c.status()
	c.mu.Unlock()
	c.poke()
	if !yes {
		return
	}
	go func() {
		ctl, err := server.Connect(c.paths, false)
		var res proto.SessionRemoteResult
		if err == nil {
			res, err = SetRemote(ctl.Call, slug, on)
			ctl.Close()
		}
		msg := ""
		if err != nil {
			msg = "remote control: " + errMessage(err)
		} else {
			msg = RemoteMessage(slug, res)
		}
		if c.lock() {
			if err == nil && !p.gone {
				p.info.RemoteControl = res.RemoteControl
			}
			c.flash = msg
			c.status()
			c.mu.Unlock()
			c.poke()
		}
	}()
}

// errMessage is an error as the user reads it: a server refusal without
// its code.
func errMessage(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

// reattachWait bounds how long a restarting session may take to be back.
const reattachWait = 15 * time.Second

// reattach attaches p to its session again once the server relaunched it
// under the same id (proto.ClosedRestarting), keeping p's place in the
// layout; if it doesn't come back, the pane ends.
func (c *client) reattach(p *pane) {
	deadline := time.Now().Add(reattachWait)
	for {
		if !c.lock() {
			return
		}
		gone, id := p.gone, p.info.ID
		c.mu.Unlock()
		if gone {
			return
		}
		np, err := c.open(id)
		if err == nil {
			p.wmu.Lock()
			if !c.lock() {
				p.wmu.Unlock()
				np.conn.Close()
				np.r.Close()
				return
			}
			old := p.conn
			if p.mirror != nil {
				p.mirror.Close()
			}
			if p.r != nil {
				p.r.Close()
			}
			p.conn, p.mirror, p.r, p.info = np.conn, np.mirror, np.r, np.info
			p.restarting = false
			// Place the new renderer; like any attach, no resize.
			c.relayout()
			c.flash = paneName(p.info) + " is back"
			c.status()
			c.mu.Unlock()
			p.wmu.Unlock()
			old.Close()
			go c.readLoop(p)
			c.poke()
			return
		}
		if time.Now().After(deadline) {
			if !c.lock() {
				return
			}
			p.restarting = false
			c.mu.Unlock()
			c.ended(p, "session restart failed: "+errMessage(err))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
