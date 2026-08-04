//go:build windows

package remotecli

import (
	"strings"
	"testing"
)

func TestQuoteCmdArgumentEscapesShellCharacters(t *testing.T) {
	quoted := quoteCmdArgument(`C:\tmp\a&b%name!.txt`)
	for _, want := range []string{"^&", "^%", "^!"} {
		if !strings.Contains(quoted, want) {
			t.Fatalf("%q missing %q", quoted, want)
		}
	}
}
