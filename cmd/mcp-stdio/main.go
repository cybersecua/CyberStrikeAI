package main

import (
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/logger"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"
	"flag"
	"fmt"
	"os"

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

	// Create MCP server
	mcpServer := mcp.NewServer(log.Logger)

	// Create security tool executor
	executor := security.NewExecutor(&cfg.Security, mcpServer, log.Logger)

	// Register tools
	executor.RegisterTools(mcpServer)

	log.Logger.Info("MCP server (stdio mode) started, waiting for messages...")

	// Run stdio loop
	if err := mcpServer.HandleStdio(); err != nil {
		log.Logger.Error("MCP server failed", zap.Error(err))
		os.Exit(1)
	}
}
