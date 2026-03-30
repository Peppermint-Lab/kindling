//go:build linux

package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/kindlingvm/kindling/internal/shellwire"
)

const maxShellwireLineBytes = 1 << 20

// shellwirePTYBridge adapts a local PTY to the JSON line shellwire protocol used by
// the guest-agent and the dashboard WebSocket terminal.
type shellwirePTYBridge struct {
	ptmx *os.File
	cmd  *exec.Cmd

	outReader *io.PipeReader
	outWriter *io.PipeWriter

	mu      sync.Mutex
	lineBuf []byte
	closed  bool

	closeOnce sync.Once
}

func newShellwirePTYBridge(ptmx *os.File, cmd *exec.Cmd) *shellwirePTYBridge {
	pr, pw := io.Pipe()
	b := &shellwirePTYBridge{
		ptmx:      ptmx,
		cmd:       cmd,
		outReader: pr,
		outWriter: pw,
	}
	go b.pump()
	return b
}

func (b *shellwirePTYBridge) pump() {
	defer b.outWriter.Close()
	enc := shellwire.NewEncoder(b.outWriter)
	send := func(f shellwire.Frame) {
		_ = enc.Encode(f)
	}
	send(shellwire.Frame{Type: "ready"})

	heartbeatStop := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-t.C:
				send(shellwire.Frame{Type: "heartbeat"})
			}
		}
	}()

	buf := make([]byte, 4096)
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		for {
			n, err := b.ptmx.Read(buf)
			if n > 0 {
				send(shellwire.Frame{Type: "stdout", Data: string(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}()

	waitErr := b.cmd.Wait()
	close(heartbeatStop)
	<-outputDone

	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
			send(shellwire.Frame{Type: "error", Error: waitErr.Error()})
		}
	}
	send(shellwire.Frame{Type: "exit", ExitCode: &exitCode})
}

func (b *shellwirePTYBridge) Read(p []byte) (int, error) {
	return b.outReader.Read(p)
}

func (b *shellwirePTYBridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if len(b.lineBuf)+len(p) > maxShellwireLineBytes {
		return 0, fmt.Errorf("shellwire frame exceeds %d bytes", maxShellwireLineBytes)
	}
	b.lineBuf = append(b.lineBuf, p...)
	for {
		idx := bytes.IndexByte(b.lineBuf, '\n')
		if idx < 0 {
			return len(p), nil
		}
		line := b.lineBuf[:idx]
		b.lineBuf = b.lineBuf[idx+1:]
		if len(line) == 0 {
			continue
		}
		var frame shellwire.Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "stdin":
			if frame.Data != "" {
				_, _ = io.WriteString(b.ptmx, frame.Data)
			}
		case "resize":
			if frame.Width > 0 && frame.Height > 0 {
				_ = pty.Setsize(b.ptmx, &pty.Winsize{
					Cols: uint16(frame.Width),
					Rows: uint16(frame.Height),
				})
			}
		}
	}
}

func (b *shellwirePTYBridge) Close() error {
	var err error
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		if b.cmd != nil && b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
		_ = b.ptmx.Close()
		err = b.outReader.Close()
	})
	return err
}
