//go:build !windows

package handler

import (
	"bufio"
	"context"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

const ptyCols = 256
const ptyRows = 40

// runCommandStreamImpl executes using PTY on Unix, so commands like ping format output by terminal width (isatty is true).
func runCommandStreamImpl(cmd *exec.Cmd, sendEvent func(streamEvent), ctx context.Context) {
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: ptyCols, Rows: ptyRows})
	if err != nil {
		sendEvent(streamEvent{T: "exit", C: -1})
		return
	}
	defer ptmx.Close()

	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		return strings.ReplaceAll(s, "\r", "\n")
	}
	sc := bufio.NewScanner(ptmx)
	for sc.Scan() {
		sendEvent(streamEvent{T: "out", D: normalize(sc.Text())})
	}
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		exitCode = -1
	}
	sendEvent(streamEvent{T: "exit", C: exitCode})
}
