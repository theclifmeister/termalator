package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

var (
	errCancelled = errors.New("cancelled")
	errStop      = errors.New("stop")
)

// job is one thing the worker does: a prompt turn, a slash command, or
// the turn a finished background agent starts.
type job struct {
	kind string // prompt, slash, notification
	text string
	via  string // typed, paste, socket, kickoff
}

// userJob reports whether j came from the user (shown as queued, and
// put back into the input box when cancelled).
func (j job) userJob() bool { return j.kind == "prompt" }

// enqueue adds a job; the worker runs it when the current one is done.
func (a *app) enqueue(j job) {
	a.mu.Lock()
	a.queue = append(a.queue, j)
	a.syncSessionLocked()
	a.mu.Unlock()
	a.poke()
	a.requestRedraw()
}

func (a *app) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *app) queuedPromptsLocked() int {
	if a.running == nil && a.started {
		return 0
	}
	n := 0
	for _, j := range a.queue {
		if j.userJob() {
			n++
		}
	}
	return n
}

// worker runs jobs one at a time, forever.
func (a *app) worker() {
	for {
		a.mu.Lock()
		if !a.started || len(a.queue) == 0 {
			a.mu.Unlock()
			<-a.wake
			continue
		}
		j := a.queue[0]
		a.queue = a.queue[1:]
		a.mu.Unlock()
		switch j.kind {
		case "slash":
			a.runSlash(j)
		default:
			a.runTurn(j)
		}
	}
}

// runSlash runs /clear, /compact, /exit or /remote-control.
func (a *app) runSlash(j job) {
	a.mu.Lock()
	a.running = &j
	a.conv = append(a.conv, &convLine{prefix: "> ", text: j.text})
	a.syncSessionLocked()
	a.mu.Unlock()
	a.requestRedraw()
	a.log("slash", map[string]any{"text": j.text, "via": j.via})
	switch {
	case strings.HasPrefix(j.text, "/remote-control"):
		a.doRemote()
	}
	switch j.text {
	case "/clear":
		a.doClear()
	case "/compact":
		a.mu.Lock()
		a.turnStart = time.Now()
		a.mu.Unlock()
		a.doCompact(context.Background())
	case "/exit":
		a.exit(0, "prompt_input_exit")
	}
	a.mu.Lock()
	a.running = nil
	a.syncSessionLocked()
	a.mu.Unlock()
	a.requestRedraw()
}

// doRemote is /remote-control as 2.1.289 has it: off, it connects; on,
// it opens a menu whose first entry disconnects, on Continue.
func (a *app) doRemote() {
	a.mu.Lock()
	on := a.remote
	a.mu.Unlock()
	if !on {
		a.mu.Lock()
		a.remote = true
		a.syncSessionLocked()
		a.mu.Unlock()
		a.say("  ", "/remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_fake")
		return
	}
	d := &dialog{kind: "remote", options: []string{"Disconnect this session", "Show QR code", "Continue"}, sel: 2}
	if n, _ := a.waitDialog(context.Background(), d); n == 1 {
		a.mu.Lock()
		a.remote = false
		a.syncSessionLocked()
		a.mu.Unlock()
		a.say("  ⎿  ", "Remote Control disconnected.")
	}
}

// doClear ends the session and starts a new one with a new id.
func (a *app) doClear() {
	_, _ = a.fireHook(context.Background(), "SessionEnd", map[string]any{"reason": "clear"})
	a.mu.Lock()
	a.sid = newUUID()
	a.conv = nil
	a.syncSessionLocked()
	path := a.transcript()
	a.mu.Unlock()
	_ = ensureTranscript(path)
	a.requestRedraw()
	a.sessionStart("clear")
}

// doCompact compacts: same id, tasks kept.
func (a *app) doCompact(ctx context.Context) {
	a.mu.Lock()
	label, spin := a.spinLabel, a.spinning
	a.spinLabel, a.spinning = "Compacting…", true
	a.mu.Unlock()
	a.requestRedraw()
	_, _ = a.fireHook(context.Background(), "PreCompact", map[string]any{"trigger": "manual", "custom_instructions": ""})
	_ = sleepCtx(context.Background(), 300*time.Millisecond)
	a.sessionStart("compact")
	a.mu.Lock()
	a.spinLabel, a.spinning = label, spin
	a.conv = append(a.conv, &convLine{prefix: "  ⎿  ", text: "Compacted"})
	a.mu.Unlock()
	a.requestRedraw()
}

// runTurn submits a prompt, runs its script and ends the turn.
func (a *app) runTurn(j job) {
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.mu.Lock()
	a.running = &j
	a.cancel = cancel
	a.turnStart = time.Now()
	a.spinning, a.spinLabel = true, "Churning…"
	a.suggestion = ""
	a.lastText = ""
	if j.kind == "notification" {
		a.conv = append(a.conv, &convLine{prefix: "⏺ ", text: "Background agent finished"})
	} else {
		a.conv = append(a.conv, &convLine{prefix: "> ", text: j.text})
	}
	a.syncSessionLocked()
	a.mu.Unlock()
	a.requestRedraw()

	a.log("prompt", map[string]any{"text": j.text, "via": j.via})
	a.transcriptAppend(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": j.text}})
	_, err := a.fireHook(ctx, "UserPromptSubmit", map[string]any{"prompt": j.text})
	if err == nil {
		err = a.runPrompt(ctx, j)
	}
	if errors.Is(err, errCancelled) || ctx.Err() != nil {
		a.finishCancelled(j)
		return
	}
	a.finishTurn()
}

// finishTurn ends a turn normally: turn_duration, status, Stop.
func (a *app) finishTurn() {
	a.mu.Lock()
	dur := time.Since(a.turnStart).Milliseconds()
	last := a.lastText
	a.mu.Unlock()
	a.transcriptAppend(map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": dur})
	a.mu.Lock()
	a.running, a.cancel, a.spinning, a.inTool = nil, nil, false, false
	a.notify = nil
	a.syncSessionLocked()
	a.mu.Unlock()
	a.requestRedraw()
	_, _ = a.fireHook(context.Background(), "Stop", map[string]any{"stop_hook_active": false, "last_assistant_message": last})
	if s := os.Getenv("FAKEAGENT_SUGGEST"); s != "" {
		a.mu.Lock()
		a.suggestion = s
		a.mu.Unlock()
		a.requestRedraw()
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, _ = a.fireHook(context.Background(), "SubagentStop", map[string]any{"agent_id": randID("a", 17), "last_assistant_message": s, "stop_hook_active": false})
		}()
	}
}

// finishCancelled ends a turn silently, as Esc does: no hook, the
// interrupt in the transcript, the prompt back in the input box.
func (a *app) finishCancelled(j job) {
	a.mu.Lock()
	now := time.Now()
	for _, l := range a.conv {
		l.freeze(now)
	}
	text := "[Request interrupted by user]"
	if a.inTool {
		text = "[Request interrupted by user for tool use]"
	}
	a.mu.Unlock()
	a.transcriptAppend(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
	a.mu.Lock()
	a.running, a.cancel, a.spinning, a.inTool = nil, nil, false, false
	a.notify = nil
	a.dialog = nil
	a.awaitKey = nil
	a.conv = append(a.conv, &convLine{prefix: "  ⎿  ", text: "Interrupted · What should Claude do instead?"})
	if j.userJob() {
		restored := []rune(j.text)
		if len(a.input) > 0 {
			restored = append(append(restored, ' '), a.input...)
		}
		a.input = restored
	}
	a.syncSessionLocked()
	a.mu.Unlock()
	a.log("cancel", map[string]any{"prompt": j.text, "interrupt": text})
	a.requestRedraw()
}

// cancelTurnLocked cancels the running prompt turn, if any.
func (a *app) cancelTurnLocked() {
	if a.running != nil && a.running.kind != "slash" && a.cancel != nil {
		a.cancel()
	}
}

// waitDialog shows d and waits for a choice (1-based). The startup
// screens pass context.Background(): 0 means exit. Esc on a turn dialog cancels ctx.
func (a *app) waitDialog(ctx context.Context, d *dialog) (int, error) {
	d.result = make(chan int, 1)
	a.mu.Lock()
	d.shownAt = time.Now()
	a.dialog = d
	if d.kind == "permission" || d.kind == "question" {
		a.inTool = true
		if n := a.notify; n != nil {
			a.notify = nil
			go a.notifyLater(d, *n)
		}
	}
	a.syncSessionLocked()
	a.mu.Unlock()
	a.requestRedraw()
	var n int
	var err error
	select {
	case n = <-d.result:
	case <-ctx.Done():
		err = errCancelled
	}
	a.mu.Lock()
	if a.dialog == d {
		a.dialog = nil
	}
	a.syncSessionLocked()
	a.mu.Unlock()
	a.requestRedraw()
	return n, err
}

// notifyLater fires a Notification if d is still open after the delay.
func (a *app) notifyLater(d *dialog, st step) {
	time.Sleep(time.Duration(st.AfterMS) * time.Millisecond)
	a.mu.Lock()
	open := a.dialog == d
	a.mu.Unlock()
	if open {
		_, _ = a.fireHook(context.Background(), "Notification", notifyPayload(st))
	}
}

// handleKey applies one key to the dialog, the await_key step or the
// input box.
func (a *app) handleKey(k key) {
	a.mu.Lock()
	if d := a.dialog; d != nil {
		a.dialogKeyLocked(d, k)
		a.mu.Unlock()
		a.requestRedraw()
		return
	}
	if a.awaitKey != nil && k.kind != kEsc && k.kind != kCtrlC {
		close(a.awaitKey)
		a.awaitKey = nil
		a.mu.Unlock()
		return
	}
	switch k.kind {
	case kEsc:
		a.cancelTurnLocked()
	case kCtrlC:
		switch {
		case a.running != nil && a.running.kind != "slash":
			a.cancelTurnLocked()
		case len(a.input) > 0:
			a.input, a.pasted = nil, false
		default:
			a.mu.Unlock()
			a.exit(0, "prompt_input_exit")
			return
		}
	case kCtrlU:
		a.input, a.pasted = nil, false
	case kBackspace:
		if len(a.input) > 0 {
			a.input = a.input[:len(a.input)-1]
		}
	case kRune:
		a.input = append(a.input, k.r)
	case kPaste:
		a.input = append(a.input, []rune(k.text)...)
		a.pasted = true
	case kEnter:
		text := strings.TrimSpace(string(a.input))
		if text == "" {
			break
		}
		via := "typed"
		if a.pasted {
			via = "paste"
		}
		a.input, a.pasted = nil, false
		a.suggestion = ""
		kind := "prompt"
		switch {
		case text == "/clear", text == "/compact", text == "/exit",
			text == "/remote-control", strings.HasPrefix(text, "/remote-control "):
			kind = "slash"
		}
		a.mu.Unlock()
		a.enqueue(job{kind: kind, text: text, via: via})
		return
	}
	a.mu.Unlock()
	a.requestRedraw()
}

// dialogKeyLocked handles a key while d is open.
func (a *app) dialogKeyLocked(d *dialog, k key) {
	if d.debounce > 0 && time.Since(d.shownAt) < d.debounce {
		return
	}
	choose := func(n int) {
		select {
		case d.result <- n:
		default:
		}
	}
	switch k.kind {
	case kRune:
		if k.r >= '1' && k.r <= '9' && int(k.r-'0') <= len(d.options) {
			d.sel = int(k.r - '1')
			choose(d.sel + 1)
		}
	case kUp:
		if d.sel > 0 {
			d.sel--
		}
	case kDown:
		if d.sel < len(d.options)-1 {
			d.sel++
		}
	case kEnter:
		choose(d.sel + 1)
	case kEsc, kCtrlC:
		if d.kind == "remote" {
			choose(3) // Esc to continue
		} else if d.kind == "trust" || d.kind == "bypass" {
			choose(0)
		} else {
			a.cancelTurnLocked()
		}
	}
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if ctx.Err() != nil {
			return errCancelled
		}
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return errCancelled
	}
}
