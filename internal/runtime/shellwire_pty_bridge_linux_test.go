//go:build linux

package runtime

import (
	"bytes"
	"testing"
)

func TestShellwirePTYBridgeRejectsOversizedLine(t *testing.T) {
	t.Parallel()

	b := &shellwirePTYBridge{}
	oversized := bytes.Repeat([]byte("x"), maxShellwireLineBytes+1)
	if _, err := b.Write(oversized); err == nil {
		t.Fatal("expected oversized shellwire write to fail")
	}
}
