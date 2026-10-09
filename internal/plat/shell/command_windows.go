package shell

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// CLICommand is exec.CommandContext, except that a program that is a batch
// file (az.cmd, gh.cmd, an npm shim) runs through cmd.exe with its
// arguments quoted for cmd.exe's parsing (batchLine), so a URL with & or
// % reaches it as it is. Set the environment from cmd.Environ(), not
// os.Environ(): it carries the variables the quoting needs.
func CLICommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	p, err := exec.LookPath(name)
	low := strings.ToLower(p)
	if err != nil || !strings.HasSuffix(low, ".cmd") && !strings.HasSuffix(low, ".bat") {
		return exec.CommandContext(ctx, name, args...)
	}
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	line, env, lerr := batchLine(comspec, p, args)
	cmd := exec.CommandContext(ctx, comspec)
	if lerr != nil {
		cmd.Err = lerr
		return cmd
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	cmd.Env = append(os.Environ(), env...)
	return cmd
}
