package session

// Prompts through the agent's mod (docs/SPEC.md §8.6, Mods). While the
// mod is live (agent.Tracker.ModLive), the queue's head goes to the mod,
// which polls for it over its socket (server/modchan.go) and hands it
// to Claude with $.prompt.submit, or $.command.run for a slash command:
// Claude queues it and runs it once idle, so a busy turn or a half-typed
// draft in the box is no reason to wait, and nothing is typed over the
// draft. The prompt stays queued, in order, until the mod acks it:
//
//   - "taken": the mod stored the prompt's id; it hands the prompt on
//     only once this ack is accepted (an offer that timed out is the
//     paste injector's). A slash command can wait a whole turn before
//     it runs, so there is no deadline after this, only the heartbeat.
//   - "submitted": Claude has it; the next prompt is offered.
//   - "refused": Claude didn't take it; it goes to the paste injector.
//
// An offer not taken within ModAckTimeout, or the mod's heartbeat
// stopping, hands the head to the paste injector too: a prompt is never
// lost to a mod that restarted. A reloaded mod is offered the same head
// again and acks it without handing it on when its stored id says it
// had (at most once while the mod lives, at least once when it dies).

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ModAckTimeout bounds how long an offer to the mod may go without a
// "taken" ack before the prompt is pasted instead. A variable for tests.
var ModAckTimeout = 30 * time.Second

// modPollEvery is how often a waiting poll looks at the queue again.
const modPollEvery = 250 * time.Millisecond

// Mod ack results.
const (
	ModTaken     = "taken"
	ModSubmitted = "submitted"
	ModRefused   = "refused"
)

// ErrNoModPrompt is an ack for a prompt that isn't the queue's head (any
// more): delivered, pasted or dropped meanwhile.
var ErrNoModPrompt = errors.New("no such prompt at the head of the queue")

// ErrModAck is an ack with an unknown result.
var ErrModAck = errors.New("result must be taken, submitted or refused")

// ModPrompt is the queue's head as offered to the mod: a prompt, or a
// slash command (Command without the slash, Args the rest of the line).
type ModPrompt struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // "prompt" or "command"
	Text    string `json:"text"`
	Command string `json:"command,omitempty"`
	Args    string `json:"args,omitempty"`
}

// modPrompt is the offer for text.
func modPrompt(id, text string) ModPrompt {
	m := ModPrompt{ID: id, Kind: "prompt", Text: text}
	if name, args, ok := slashCommand(text); ok {
		m.Kind, m.Command, m.Args = "command", name, args
	}
	return m
}

// slashCommand splits a one-line "/name args" into its name and args.
func slashCommand(text string) (name, args string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") || strings.ContainsAny(t, "\r\n") {
		return "", "", false
	}
	name, args, _ = strings.Cut(t[1:], " ")
	if name == "" || len(name) > 64 {
		return "", "", false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == ':') {
			return "", "", false
		}
	}
	return name, strings.TrimSpace(args), true
}

// modDeliversLocked reports whether the mod delivers the queue's head
// now; rt.mu held. An offer the mod hasn't taken within ModAckTimeout
// goes to the paste injector from here on.
func (s *Session) modDeliversLocked(rt *agentRT, now time.Time) bool {
	if len(rt.prompts) == 0 || rt.cfg.ModSocket == "" || rt.observed {
		return false
	}
	p := &rt.prompts[0]
	if p.paste || !rt.tr.ModLive() {
		return false
	}
	if !p.offered.IsZero() && !p.taken && now.Sub(p.offered) >= ModAckTimeout {
		p.paste = true
		s.cfg.Logf("session %s: the mod didn't take prompt %s within %s: pasting it", s.cfg.ID, p.id, ModAckTimeout)
		return false
	}
	return true
}

// NextModPrompt waits until the mod delivers the queue's head and
// returns it, or false when ctx ends first (the mod's long poll). A nudge
// gets its text refreshed here, once, before the mod first sees it.
func (s *Session) NextModPrompt(ctx context.Context) (ModPrompt, bool, error) {
	rt := s.agentRT()
	if rt == nil || rt.observed {
		return ModPrompt{}, false, ErrNoAgent
	}
	for {
		rt.mu.Lock()
		if s.modDeliversLocked(rt, time.Now()) {
			p := rt.prompts[0]
			if p.refresh != nil && !p.refreshed {
				rt.mu.Unlock()
				text, ok := p.refresh()
				s.refreshed(rt, p, text, ok)
				continue
			}
			rt.prompts[0].offered = time.Now()
			rt.mu.Unlock()
			return modPrompt(p.id, p.text), true, nil
		}
		rt.mu.Unlock()
		select {
		case <-ctx.Done():
			return ModPrompt{}, false, nil
		case <-rt.stop:
			return ModPrompt{}, false, ErrNoAgent
		case <-s.done:
			return ModPrompt{}, false, ErrExited
		case <-time.After(modPollEvery):
		}
	}
}

// refreshed puts a nudge's refreshed text in place, or drops it as stale,
// if p is still the queue's head.
func (s *Session) refreshed(rt *agentRT, p queuedPrompt, text string, ok bool) {
	rt.mu.Lock()
	if len(rt.prompts) == 0 || rt.prompts[0].id != p.id {
		rt.mu.Unlock()
		return
	}
	if ok {
		rt.prompts[0].text, rt.prompts[0].refreshed = text, true
		rt.mu.Unlock()
		return
	}
	rt.prompts = rt.prompts[1:]
	rt.mu.Unlock()
	s.cfg.Logf("session %s: queued prompt (queued %s) is stale: not delivered", s.cfg.ID, p.at.Format(time.DateTime))
	if rt.cfg.OnPromptResolved != nil {
		rt.cfg.OnPromptResolved(s, PromptResolution{Text: p.text, Via: "stale", Queued: p.at})
	}
}

// AckModPrompt takes the mod's ack for prompt id, the queue's head:
// ModTaken, ModSubmitted, or ModRefused with why.
func (s *Session) AckModPrompt(id, result, why string) error {
	rt := s.agentRT()
	if rt == nil || rt.observed {
		return ErrNoAgent
	}
	rt.mu.Lock()
	if len(rt.prompts) == 0 || rt.prompts[0].id != id {
		rt.mu.Unlock()
		return ErrNoModPrompt
	}
	p := &rt.prompts[0]
	switch result {
	case ModTaken:
		if p.paste {
			// Too late: it's the paste injector's. The mod hands on
			// only what it took.
			rt.mu.Unlock()
			return ErrNoModPrompt
		}
		p.taken = true
	case ModSubmitted:
		rt.prompts = rt.prompts[1:]
	case ModRefused:
		p.paste, p.taken = true, false
	default:
		rt.mu.Unlock()
		return ErrModAck
	}
	rt.mu.Unlock()
	switch result {
	case ModSubmitted:
		s.cfg.Logf("session %s: prompt %s delivered by the mod", s.cfg.ID, id)
		s.notifyState()
	case ModRefused:
		s.cfg.Logf("session %s: the mod refused prompt %s (%s): pasting it", s.cfg.ID, id, why)
	}
	return nil
}
