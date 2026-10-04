package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.mitchellh.com/libghostty"
)

// shellScenario exercises the host with a plain shell: cheap, deterministic.
func shellScenario() {
	sockDir, _ := os.MkdirTemp("", "gvt")
	sock := filepath.Join(sockDir, "shell.sock")
	sh := []string{"/bin/zsh", "-f"}
	if v := os.Getenv("GVT_TEST_SHELL"); v != "" {
		sh = strings.Fields(v)
	}
	w := openWindow("A", 100, 30, append([]string{"new", "-s", sock, "-log", filepath.Join(*outDir, "shell-client.log"), "--"}, sh...)...)
	time.Sleep(500 * time.Millisecond)
	w.typeText("PS1='$ '; clear\r")
	w.quiet(300*time.Millisecond, 3*time.Second)
	w.typeText("printf 'wide: 漢字テスト | emoji: 👋🏽 🇳🇱 👨‍👩‍👧 | combining: e\\u0301 | box: ┌─┐\\n'\r")
	w.quiet(300*time.Millisecond, 3*time.Second)
	w.typeText("printf '\\e[1;31mbold red\\e[0m \\e[38;2;10;200;90mrgb\\e[0m \\e[4:3mcurly\\e[0m \\e[7minverse\\e[0m\\n'\r")
	w.quiet(300*time.Millisecond, 3*time.Second)
	w.typeText("seq 1 200\r")
	w.quiet(500*time.Millisecond, 5*time.Second)
	compare("shell-1-basic", w, sock)

	// Scrollback in the client mirror.
	before := w.screen()
	w.key(libghostty.KeyPageUp, libghostty.ModShift, "", 0)
	w.quiet(300*time.Millisecond, 3*time.Second)
	s := w.save("shell-2-scrolled")
	res("Shift+PgUp scrolls client viewport into scrollback", firstLine(s) != firstLine(before) && !strings.HasPrefix(s, "$"),
		fmt.Sprintf("top row %q -> %q", firstLine(before), firstLine(s)))
	w.key(libghostty.KeyPageDown, libghostty.ModShift, "", 0)
	time.Sleep(300 * time.Millisecond)
	w.typeText("\x15") // ctrl-u; any key returns to bottom
	w.quiet(300*time.Millisecond, 3*time.Second)
	compare("shell-3-back-to-bottom", w, sock)

	// Resize: shrink then grow.
	w.resize(70, 20)
	w.quiet(500*time.Millisecond, 3*time.Second)
	compare("shell-4-resize-70x20", w, sock)
	w.resize(130, 40)
	w.quiet(500*time.Millisecond, 3*time.Second)
	compare("shell-5-resize-130x40", w, sock)
	w.cmd.Process.Signal(sigusr1)
	time.Sleep(300 * time.Millisecond)

	// Bracketed paste: zsh -f has bracketed paste (zle) on.
	w.paste("echo pasted-line-1; echo pasted-line-2")
	w.typeText("\r")
	w.quiet(300*time.Millisecond, 3*time.Second)
	s = w.screen()
	res("bracketed paste delivered", strings.Contains(s, "pasted-line-2"), "")

	// Detach (Ctrl+\ via kitty encoding) and reattach in a new window.
	sp, cp := pids(sock)
	b := w.key(libghostty.KeyBackslash, libghostty.ModCtrl, "", '\\')
	note("Ctrl+\\ sent by outer terminal as %q", b)
	select {
	case <-w.exit:
		res("Ctrl+\\ detaches client", true, "")
	case <-time.After(2 * time.Second):
		res("Ctrl+\\ detaches client", false, "")
	}
	res("server + child alive after detach", alive(sp) && alive(cp), fmt.Sprintf("server=%d child=%d", sp, cp))
	w2 := openWindow("B", 120, 35, "attach", "-s", sock, "-log", filepath.Join(*outDir, "shell-client.log"))
	w2.quiet(500*time.Millisecond, 3*time.Second)
	compare("shell-6-reattach", w2, sock)

	// Close the window outright.
	err := w2.closeWindow()
	res("closing window kills only the client", err == nil && alive(sp) && alive(cp), fmt.Sprint(err))
	w3 := openWindow("C", 120, 35, "attach", "-s", sock)
	w3.quiet(500*time.Millisecond, 3*time.Second)
	w3.typeText("echo still-here $$\r")
	w3.quiet(300*time.Millisecond, 3*time.Second)
	res("shell still alive and usable after window close", w3.waitFor("still-here", 2*time.Second), "")
	compare("shell-7-after-close", w3, sock)

	// Digest check: mirror state == server state, exact (all screens, modes).
	w3.cmd.Process.Signal(sigusr1)
	time.Sleep(300 * time.Millisecond)

	w3.typeText("exit\r")
	select {
	case <-w3.exit:
	case <-time.After(3 * time.Second):
	}
	time.Sleep(300 * time.Millisecond)
	res("server exits when child exits", !alive(sp), "")
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

// benchScenario pushes a lot of output through server + client to measure
// throughput and CPU, which Claude's stream (~40 KB per answer) cannot.
func benchScenario() {
	sockDir, _ := os.MkdirTemp("", "gvt")
	sock := filepath.Join(sockDir, "bench.sock")
	const n = 2000000 // ~14.9 MB of output
	w := openWindow("bench", 160, 50, "new", "-s", sock, "-stats", filepath.Join(*outDir, "bench-stats.json"),
		"--", "/bin/sh", "-c", fmt.Sprintf("sleep 1; seq 1 %d; echo BENCH-DONE; sleep 30", n))
	time.Sleep(500 * time.Millisecond)
	sp, cp := pids(sock)
	cl := w.cmd.Process.Pid
	s0, c0 := cpuTime(sp), cpuTime(cl)
	t0 := time.Now()
	if !w.waitFor("BENCH-DONE", 120*time.Second) {
		res("bench finished", false, "")
	}
	wall := time.Since(t0) - 500*time.Millisecond
	ds, dc := cpuTime(sp)-s0, cpuTime(cl)-c0
	note("%d lines (~%.1f MB) through server+client in ~%v", n, 14.9, wall.Round(time.Millisecond))
	note("server CPU %v (%.0f%% of wall), client CPU %v (%.0f%% of wall)", ds, 100*ds.Seconds()/wall.Seconds(), dc, 100*dc.Seconds()/wall.Seconds())
	compare("bench-final", w, sock)
	w.key(libghostty.KeyBackslash, libghostty.ModCtrl, "", '\\')
	<-w.exit
	syscall.Kill(cp, syscall.SIGTERM)
	b, _ := os.ReadFile(filepath.Join(*outDir, "bench-stats.json"))
	var st struct {
		Frames, FrameBytes, OutputMsgs, OutputBytes, Resyncs int
		Summary                                              map[string]string
	}
	json.Unmarshal(b, &st)
	note("client: frames=%d frameBytes=%d outMsgs=%d outBytes=%d resyncs=%d", st.Frames, st.FrameBytes, st.OutputMsgs, st.OutputBytes, st.Resyncs)
	for _, k := range []string{"output_to_frame", "render_time", "client_maxrss"} {
		note("  %s: %s", k, st.Summary[k])
	}
}
