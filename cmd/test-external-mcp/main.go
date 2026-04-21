package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/logger"
	"cyberstrike-ai/internal/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run cmd/test-external-mcp/main.go <config.yaml>")
		os.Exit(1)
	}

	configPath := os.Args[1]
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	if cfg.ExternalMCP.Servers == nil || len(cfg.ExternalMCP.Servers) == 0 {
		fmt.Println("No external MCP servers configured")
		os.Exit(0)
	}

	fmt.Printf("Found %d external MCP server(s)\n\n", len(cfg.ExternalMCP.Servers))

	// Create logger
	log := logger.New("info", "stdout")

	// Create external MCP manager
	manager := mcp.NewExternalMCPManager(log.Logger)
	manager.LoadConfigs(&cfg.ExternalMCP)

	// Display configuration
	fmt.Println("=== Configuration ===")
	for name, srv := range cfg.ExternalMCP.Servers {
		fmt.Printf("\n%s:\n", name)
		fmt.Printf("  Transport: %s\n", getTransport(srv))
		if srv.Command != "" {
			fmt.Printf("  Command: %s\n", srv.Command)
			fmt.Printf("  Args: %v\n", srv.Args)
		}
		if srv.URL != "" {
			fmt.Printf("  URL: %s\n", srv.URL)
		}
		fmt.Printf("  Description: %s\n", srv.Description)
		fmt.Printf("  Timeout: %d seconds\n", srv.Timeout)
		fmt.Printf("  Enabled: %v\n", srv.Enabled)
		fmt.Printf("  Disabled: %v\n", srv.Disabled)
	}

	// Get statistics
	fmt.Println("\n=== Statistics ===")
	stats := manager.GetStats()
	fmt.Printf("Total: %d\n", stats["total"])
	fmt.Printf("Enabled: %d\n", stats["enabled"])
	fmt.Printf("Disabled: %d\n", stats["disabled"])
	fmt.Printf("Connected: %d\n", stats["connected"])

	// Test startup (only test enabled ones)
	fmt.Println("\n=== Test Startup ===")
	for name, srv := range cfg.ExternalMCP.Servers {
		if srv.Enabled && !srv.Disabled {
			fmt.Printf("\nAttempting to start %s...\n", name)
			// Note: actual startup may fail if no real MCP server is available
			err := manager.StartClient(name)
			if err != nil {
				fmt.Printf("  Startup failed (this is expected if no real MCP server exists): %v\n", err)
			} else {
				fmt.Printf("  Startup succeeded\n")
				// Get client status
				if client, exists := manager.GetClient(name); exists {
					fmt.Printf("  Status: %s\n", client.GetStatus())
					fmt.Printf("  Connected: %v\n", client.IsConnected())
				}
			}
		}
	}

	// Wait a moment
	time.Sleep(2 * time.Second)

	// Test getting tool list
	fmt.Println("\n=== Test Get Tool List ===")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools, err := manager.GetAllTools(ctx)
	if err != nil {
		fmt.Printf("Failed to get tool list: %v\n", err)
	} else {
		fmt.Printf("Retrieved %d tool(s)\n", len(tools))
		for i, tool := range tools {
			if i < 5 { // Only show first 5
				fmt.Printf("  - %s: %s\n", tool.Name, tool.Description)
			}
		}
		if len(tools) > 5 {
			fmt.Printf("  ... and %d more tool(s)\n", len(tools)-5)
		}
	}

	// Test stop
	fmt.Println("\n=== Test Stop ===")
	for name := range cfg.ExternalMCP.Servers {
		fmt.Printf("\nStopping %s...\n", name)
		err := manager.StopClient(name)
		if err != nil {
			fmt.Printf("  Stop failed: %v\n", err)
		} else {
			fmt.Printf("  Stop succeeded\n")
		}
	}

	// Final statistics
	fmt.Println("\n=== Final Statistics ===")
	stats = manager.GetStats()
	fmt.Printf("Total: %d\n", stats["total"])
	fmt.Printf("Enabled: %d\n", stats["enabled"])
	fmt.Printf("Disabled: %d\n", stats["disabled"])
	fmt.Printf("Connected: %d\n", stats["connected"])

	fmt.Println("\n=== Test Complete ===")
}

func getTransport(srv config.ExternalMCPServerConfig) string {
	if srv.Transport != "" {
		return srv.Transport
	}
	if srv.Command != "" {
		return "stdio"
	}
	if srv.URL != "" {
		return "http"
	}
	return "unknown"
}
