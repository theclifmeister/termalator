package server

import (
	"context"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/theclifmeister/termalator/internal/proto"
)

// notifyEnv set to 0 turns OS notifications off (tests set it).
const notifyEnv = "TERMALATOR_NOTIFY"

// blockedMessage is the notification for a session that became blocked.
func blockedMessage(info proto.SessionInfo, reason string) string {
	who := info.ID
	switch {
	case info.Project != "" && info.Thread != "":
		who = info.Project + " " + info.Thread
	case info.Project != "":
		who = info.Project + " " + info.Role
	case info.Agent != "":
		who = info.Agent + " " + info.ID
	}
	msg := who + " is blocked"
	if reason != "" {
		msg += ": " + reason
	}
	return msg
}

// notify sends an OS notification (docs/SPEC.md §4): osascript on macOS,
// notify-send on Linux, when they exist. Failures only go to the log.
func notify(l *log.Logger, msg string) {
	if os.Getenv(notifyEnv) == "0" {
		return
	}
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		// The message is an argument, never part of the script.
		argv = []string{"osascript", "-e", "on run argv", "-e",
			`display notification (item 1 of argv) with title "termalator"`, "-e", "end run", msg}
	default:
		argv = []string{"notify-send", "termalator", msg}
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput(); err != nil {
		l.Printf("notify: %v %s", err, out)
	}
}
