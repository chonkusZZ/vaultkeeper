package agent

import (
	"runtime"
	"testing"
)

func TestEscapeInclude(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	if got := escapeInclude("/data/a*b[1]?.txt"); got != `/data/a\*b\[1]\?.txt` {
		t.Errorf("got %q", got)
	}
}
