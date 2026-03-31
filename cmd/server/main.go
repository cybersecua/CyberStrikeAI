package main

import (
	"cyberstrike-ai/internal/app"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/logger"
	"flag"
	"fmt"
)

func main() {
	var configPath = flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		return
	}

	// MCP 启用且 auth_header_value 为空时，自动生成随机密钥并写回配置
	if err := config.EnsureMCPAuth(*configPath, cfg); err != nil {
		fmt.Printf("MCP 鉴权配置失败: %v\n", err)
		return
	}
	if cfg.MCP.Enabled {
		config.PrintMCPConfigJSON(cfg.MCP)
	}

	// 初始化日志
	log := logger.New(cfg.Log.Level, cfg.Log.Output)

	// 创建应用
	application, err := app.New(cfg, log)
	if err != nil {
		log.Fatal("应用初始化失败", "error", err)
	}

	// 启动服务器
	if err := application.Run(); err != nil {
		log.Fatal("服务器启动失败", "error", err)
	}
}

