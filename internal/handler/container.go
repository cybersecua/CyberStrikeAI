package handler

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/database"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	defaultKaliImage = "cyberstrike/kali:latest"
	defaultKaliDir   = "containers/kali"
)

// ContainerHandler manages the remote Kali container fleet.
type ContainerHandler struct {
	logger *zap.Logger
	db     *database.DB
}

func NewContainerHandler(logger *zap.Logger, db *database.DB) *ContainerHandler {
	return &ContainerHandler{logger: logger, db: db}
}

func generateGSSecret() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b)[:24], nil
}

// ListContainers GET /api/containers
func (h *ContainerHandler) ListContainers(c *gin.Context) {
	list, err := h.db.ListContainers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []database.Container{}
	}
	c.JSON(http.StatusOK, list)
}

// CreateContainer POST /api/containers
func (h *ContainerHandler) CreateContainer(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		PanelURL string `json:"panelUrl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Name == "" {
		req.Name = "kali-cs"
	}

	secret, err := generateGSSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate secret"})
		return
	}

	ct, err := h.db.CreateContainer(req.Name, secret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"container": ct,
		"dockerRun": buildDockerRun(ct.Name, ct.GSSecret, req.PanelURL),
	})
}

// GetContainer GET /api/containers/:id
func (h *ContainerHandler) GetContainer(c *gin.Context) {
	ct, err := h.db.GetContainer(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ct == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, ct)
}

// GetDeployCmd GET /api/containers/:id/deploy-cmd
func (h *ContainerHandler) GetDeployCmd(c *gin.Context) {
	ct, err := h.db.GetContainer(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ct == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	panelURL := c.Query("panelUrl")
	c.JSON(http.StatusOK, gin.H{
		"dockerRun": buildDockerRun(ct.Name, ct.GSSecret, panelURL),
		"gsSecret":  ct.GSSecret,
	})
}

// DeleteContainer DELETE /api/containers/:id
func (h *ContainerHandler) DeleteContainer(c *gin.Context) {
	if err := h.db.DeleteContainer(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RegisterContainer POST /api/containers/register  (public — called by container on boot)
func (h *ContainerHandler) RegisterContainer(c *gin.Context) {
	var req struct {
		GSSecret string `json:"gs_secret"`
		Hostname string `json:"hostname"`
		IP       string `json:"ip"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.GSSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gs_secret required"})
		return
	}

	ct, err := h.db.GetContainerBySecret(req.GSSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ct == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown secret"})
		return
	}

	if err := h.db.UpdateContainerStatus(req.GSSecret, req.Hostname, req.IP, true); err != nil {
		h.logger.Warn("container register: update status", zap.Error(err), zap.String("id", ct.ID))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": ct.ID})
}

// ── Build + Run stream ────────────────────────────────────────────────────────

// buildSSEEvent marshals an SSE payload and writes it to the response writer.
type containerSSEEvent struct {
	Type    string      `json:"type"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// BuildAndRunStream POST /api/containers/build-and-run/stream
// Builds the Kali image (if needed), creates a container record, runs it on
// the local Docker host, and waits for the container to call back as online.
// Streams progress as SSE events: { type: "log"|"done"|"error", message: "..." }
func (h *ContainerHandler) BuildAndRunStream(c *gin.Context) {
	var req struct {
		Name       string `json:"name"`
		PanelURL   string `json:"panelUrl"`
		ImageName  string `json:"imageName"`
		ForceBuild bool   `json:"forceBuild"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Name == "" {
		req.Name = fmt.Sprintf("kali-%d", time.Now().Unix()%10000)
	}
	if req.ImageName == "" {
		req.ImageName = defaultKaliImage
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	var mu sync.Mutex
	disconnected := false

	send := func(evtType, message string, data interface{}) {
		mu.Lock()
		defer mu.Unlock()
		if disconnected {
			return
		}
		select {
		case <-c.Request.Context().Done():
			disconnected = true
			return
		default:
		}
		evt := containerSSEEvent{Type: evtType, Message: message, Data: data}
		b, _ := json.Marshal(evt)
		fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		if f, ok := c.Writer.(http.Flusher); ok {
			f.Flush()
		}
	}

	logLine := func(line string) { send("log", line, nil) }
	fail := func(msg string) { send("error", msg, nil) }
	done := func(msg string, data interface{}) { send("done", msg, data) }

	// ── locate containers/kali/ ───────────────────────────────────────────────
	kaliDir := os.Getenv("CSAI_KALI_DIR")
	if kaliDir == "" {
		kaliDir = defaultKaliDir
	}
	if !filepath.IsAbs(kaliDir) {
		cwd, _ := os.Getwd()
		kaliDir = filepath.Join(cwd, kaliDir)
	}
	if _, err := os.Stat(filepath.Join(kaliDir, "Dockerfile")); err != nil {
		fail(fmt.Sprintf("Kali Dockerfile not found at %s — is CyberStrikeAI running from project root?", kaliDir))
		return
	}

	ctx := c.Request.Context()

	// ── check if image already exists ────────────────────────────────────────
	imageExists := false
	if !req.ForceBuild {
		if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", req.ImageName).CombinedOutput(); err == nil && len(out) > 2 {
			imageExists = true
		}
	}

	// ── docker build ─────────────────────────────────────────────────────────
	if !imageExists || req.ForceBuild {
		logLine(fmt.Sprintf("Building %s — this takes 10-20 min on first run…", req.ImageName))
		cmd := exec.CommandContext(ctx, "docker", "build", "-t", req.ImageName, kaliDir)
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			fail("docker build failed to start: " + err.Error())
			return
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 512*1024), 512*1024)
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			if line != "" {
				logLine(line)
			}
			select {
			case <-ctx.Done():
				cmd.Process.Kill()
				fail("build cancelled by client")
				return
			default:
			}
		}
		if err := cmd.Wait(); err != nil {
			fail("docker build failed: " + err.Error())
			return
		}
		logLine("Image built successfully.")
	} else {
		logLine(fmt.Sprintf("Image %s already exists — skipping build (use force rebuild to update).", req.ImageName))
	}

	// ── create container record in DB ────────────────────────────────────────
	secret, err := generateGSSecret()
	if err != nil {
		fail("failed to generate gsocket secret: " + err.Error())
		return
	}
	ct, err := h.db.CreateContainer(req.Name, secret)
	if err != nil {
		fail("DB error: " + err.Error())
		return
	}

	// ── docker run ───────────────────────────────────────────────────────────
	panelURL := req.PanelURL
	if panelURL == "" {
		panelURL = "http://localhost:8080"
	}

	logLine(fmt.Sprintf("Starting container %s…", req.Name))
	runArgs := []string{
		"run", "-d",
		"--name", ct.Name,
		"-e", "GS_SECRET=" + ct.GSSecret,
		"-e", "PANEL_URL=" + panelURL,
		"--restart", "unless-stopped",
		req.ImageName,
	}
	out, err := exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput()
	if err != nil {
		// Container name conflict — try to remove and retry once
		if strings.Contains(string(out), "already in use") || strings.Contains(string(out), "Conflict") {
			logLine(fmt.Sprintf("Container name %s already in use — removing old container and retrying…", req.Name))
			exec.CommandContext(ctx, "docker", "rm", "-f", ct.Name).Run()
			out, err = exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput()
		}
		if err != nil {
			h.db.DeleteContainer(ct.ID)
			fail("docker run failed: " + strings.TrimSpace(string(out)))
			return
		}
	}
	containerDockerID := strings.TrimSpace(string(out))
	logLine(fmt.Sprintf("Container started (Docker ID: %s…)", containerDockerID[:min(12, len(containerDockerID))]))
	logLine(fmt.Sprintf("Waiting for container to register (gsocket secret: %s…)…", ct.GSSecret[:6]))

	// ── poll for registration ─────────────────────────────────────────────────
	deadline := time.Now().Add(90 * time.Second)
	pollCtx, pollCancel := context.WithDeadline(ctx, deadline)
	defer pollCancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Print a dot every 10s so the UI knows we're still alive
	lastDot := time.Now()
	for {
		select {
		case <-pollCtx.Done():
			logLine("Timeout waiting for registration — container may still be starting.")
			logLine("Check the Containers tab in a moment; it will appear once Docker pulls the image layers.")
			done("Container deployed (registration pending)", map[string]interface{}{
				"id": ct.ID, "name": ct.Name, "gsSecret": ct.GSSecret,
			})
			return
		case <-ticker.C:
			updated, err := h.db.GetContainer(ct.ID)
			if err == nil && updated != nil && updated.IsOnline {
				logLine(fmt.Sprintf("Container online! Hostname: %s  IP: %s", updated.Hostname, updated.IPAddress))
				done("Container is online and ready.", map[string]interface{}{
					"id": ct.ID, "name": ct.Name, "gsSecret": ct.GSSecret,
					"hostname": updated.Hostname, "ip": updated.IPAddress,
				})
				return
			}
			if time.Since(lastDot) > 10*time.Second {
				logLine("…waiting…")
				lastDot = time.Now()
			}
		}
	}
}

// streamCmdOutput pipes cmd stdout+stderr line-by-line to send. Blocks until done.
func streamCmdOutput(cmd *exec.Cmd, send func(string), ctx context.Context) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(io.Reader(stdout))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line != "" {
			send(line)
		}
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			return ctx.Err()
		default:
		}
	}
	return cmd.Wait()
}

func buildDockerRun(name, secret, panelURL string) string {
	base := fmt.Sprintf("docker run -d --name %s -e GS_SECRET=%s", name, secret)
	if panelURL != "" {
		base += fmt.Sprintf(" -e PANEL_URL=%s", panelURL)
	}
	return base + " --restart unless-stopped " + defaultKaliImage
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
