//go:build !windows

package shell

import (
	"context"
	"os/exec"
)

// CLICommand is exec.CommandContext; see the Windows file for why it exists.
func CLICommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
