//go:build !windows

package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// terminalResize is the control message sent by the xterm.js frontend when the
// browser terminal is resized. Received as a TextMessage with {"type":"resize",
// "cols":N,"rows":N} and used to resize the backing PTY.
type terminalResize struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// wsUpgrader is used only for the terminal WebSocket in system settings, reusing the existing login protection (JWT middleware in parent route group).
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			return true
		}
		return sameOriginRequest(origin, r.Host)
	},
}

func sameOriginRequest(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// RunCommandWS provides a truly interactive shell: a long-lived session based on WebSocket + PTY.
// After the frontend establishes a WebSocket connection, all keyboard input is forwarded to the shell, and shell output is written back to the frontend in real time.
func (h *TerminalHandler) RunCommandWS(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Start interactive shell, prefer bash, fall back to sh if not found
	shell := "bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "sh"
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(),
		"COLUMNS=256",
		"LINES=40",
		"TERM=xterm-256color",
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: ptyCols, Rows: ptyRows})
	if err != nil {
		return
	}
	defer ptmx.Close()

	// Shell -> WebSocket: send PTY output to the frontend in real time
	doneChan := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				_ = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				break
			}
		}
		close(doneChan)
	}()

	// WebSocket -> Shell: write frontend input to PTY (including sudo password, Ctrl+C, etc.)
	conn.SetReadLimit(64 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(terminalTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(terminalTimeout))
		return nil
	})

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			_ = cmd.Process.Kill()
			break
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		if len(data) == 0 {
			continue
		}
		// The xterm.js frontend sends JSON-encoded resize messages as TextMessage;
		// route them to pty.Setsize instead of writing them to the shell as input.
		if msgType == websocket.TextMessage && data[0] == '{' {
			var resize terminalResize
			if json.Unmarshal(data, &resize) == nil && resize.Type == "resize" && resize.Cols > 0 && resize.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: resize.Cols, Rows: resize.Rows})
				continue
			}
		}
		if _, err := ptmx.Write(data); err != nil {
			_ = cmd.Process.Kill()
			break
		}
	}

	<-doneChan
}
