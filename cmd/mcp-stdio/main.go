package main

import (
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/logger"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
)

func main() {
	var configPath = flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger (in stdio mode, log to stderr to avoid interfering with JSON-RPC communication)
	log := logger.New(cfg.Log.Level, "stderr")
	// Flush the zap buffer on any normal or signal-driven exit so log lines
	// aren't silently lost when the client (Cursor / Claude Code / VS Code)
	// closes the stdio pipe or the user hits Ctrl+C.
	defer func() { _ = log.Logger.Sync() }()

	// Create MCP server
	mcpServer := mcp.NewServer(log.Logger)

	// Create security tool executor
	executor := security.NewExecutor(&cfg.Security, mcpServer, log.Logger)

	// Register tools
	executor.RegisterTools(mcpServer)

	// Handle SIGINT / SIGTERM: log the shutdown signal, let the deferred
	// log.Sync() flush, then exit. HandleStdio already exits cleanly on
	// stdin EOF (the normal client-close path), so this only matters for
	// interactive use where the user Ctrl+C's the process.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Logger.Info("received shutdown signal", zap.String("signal", sig.String()))
		_ = log.Logger.Sync()
		os.Exit(0)
	}()

	log.Logger.Info("MCP server (stdio mode) started, waiting for messages...")

	// Run stdio loop
	if err := mcpServer.HandleStdio(); err != nil {
		log.Logger.Error("MCP server failed", zap.Error(err))
		os.Exit(1)
	}
}
