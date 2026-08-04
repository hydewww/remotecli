//go:build windows

package remotecli

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

func buildOpenCLICommand(ctx context.Context, path string, args []string) *exec.Cmd {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return exec.CommandContext(ctx, path, args...)
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteCmdArgument(path))
	for _, arg := range args {
		parts = append(parts, quoteCmdArgument(arg))
	}
	// npm installs commonly expose opencli as a .cmd shim. cmd.exe is only
	// used for that OS-native launcher; ordinary arguments are still passed
	// through one quoted token at a time and never concatenated with operators.
	return exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", strings.Join(parts, " "))
}

func quoteCmdArgument(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '%':
			b.WriteString(`^%`)
		case '!':
			b.WriteString(`^!`)
		case '^':
			b.WriteString(`^^`)
		case '&', '|', '<', '>', '(', ')':
			b.WriteByte('^')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
