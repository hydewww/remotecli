//go:build !windows

package remotecli

import (
	"context"
	"os/exec"
)

func buildOpenCLICommand(ctx context.Context, path string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, path, args...)
}
