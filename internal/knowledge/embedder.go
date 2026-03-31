package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/openai"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// Embedder 文本嵌入器
type Embedder struct {
	openAIClient   *openai.Client
	config         *config.KnowledgeConfig
	openAIConfig   *config.OpenAIConfig // 用于获取 API Key
	logger         *zap.Logger
	rateLimiter    *rate.Limiter       // 速率限制器
	rateLimitDelay time.Duration       // 请求间隔时间
	maxRetries     int                 // 最大重试次数
	retryDelay     time.Duration       // 重试间隔
	mu             sync.Mutex          // 保护 rateLimiter
}

// NewEmbedder 创建新的嵌入器
func NewEmbedder(cfg *config.KnowledgeConfig, openAIConfig *config.OpenAIConfig, openAIClient *openai.Client, logger *zap.Logger) *Embedder {
	// 初始化速率限制器
	var rateLimiter *rate.Limiter
	var rateLimitDelay time.Duration

	// 如果配置了 MaxRPM，根据 RPM 计算速率限制
	if cfg.Indexing.MaxRPM > 0 {
		rpm := cfg.Indexing.MaxRPM
		rateLimiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), rpm)
		logger.Info("知识库索引速率限制已启用", zap.Int("maxRPM", rpm))
	} else if cfg.Indexing.RateLimitDelayMs > 0 {
		// 如果没有配置 MaxRPM 但配置了固定延迟，使用固定延迟模式
		rateLimitDelay = time.Duration(cfg.Indexing.RateLimitDelayMs) * time.Millisecond
		logger.Info("知识库索引固定延迟已启用", zap.Duration("delay", rateLimitDelay))
	}

	// 重试配置
	maxRetries := 3
	retryDelay := 1000 * time.Millisecond
	if cfg.Indexing.MaxRetries > 0 {
		maxRetries = cfg.Indexing.MaxRetries
	}
	if cfg.Indexing.RetryDelayMs > 0 {
		retryDelay = time.Duration(cfg.Indexing.RetryDelayMs) * time.Millisecond
	}

	return &Embedder{
		openAIClient:   openAIClient,
		config:         cfg,
		openAIConfig:   openAIConfig,
		logger:         logger,
		rateLimiter:    rateLimiter,
		rateLimitDelay: rateLimitDelay,
		maxRetries:     maxRetries,
		retryDelay:     retryDelay,
	}
}

// EmbeddingRequest OpenAI 嵌入请求
type EmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbeddingResponse OpenAI 嵌入响应
type EmbeddingResponse struct {
	Data []EmbeddingData `json:"data"`
	Error *EmbeddingError `json:"error,omitempty"`
}

// EmbeddingData 嵌入数据
type EmbeddingData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// EmbeddingError 嵌入错误
type EmbeddingError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// waitRateLimiter 等待速率限制器
func (e *Embedder) waitRateLimiter() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.rateLimiter != nil {
		// 等待令牌
		ctx := context.Background()
		if err := e.rateLimiter.Wait(ctx); err != nil {
			e.logger.Warn("速率限制器等待失败", zap.Error(err))
		}
	}

	if e.rateLimitDelay > 0 {
		time.Sleep(e.rateLimitDelay)
	}
}

// EmbedText 对文本进行嵌入（带重试和速率限制）
func (e *Embedder) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if e.openAIClient == nil {
		return nil, fmt.Errorf("OpenAI 客户端未初始化")
	}

	var lastErr error
	for attempt := 0; attempt < e.maxRetries; attempt++ {
		// 速率限制
		if attempt > 0 {
			// 重试时等待更长时间
			waitTime := e.retryDelay * time.Duration(attempt)
			e.logger.Debug("重试前等待", zap.Int("attempt", attempt+1), zap.Duration("waitTime", waitTime))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(waitTime):
			}
		} else {
			e.waitRateLimiter()
		}

		result, err := e.doEmbedText(ctx, text)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// 检查是否是可重试的错误（429 速率限制、5xx 服务器错误、网络错误）
		if !e.isRetryableError(err) {
			return nil, err
		}

		e.logger.Debug("嵌入请求失败，准备重试",
			zap.Int("attempt", attempt+1),
			zap.Int("maxRetries", e.maxRetries),
			zap.Error(err))
	}

	return nil, fmt.Errorf("达到最大重试次数 (%d): %v", e.maxRetries, lastErr)
}

// doEmbedText 执行实际的嵌入请求（内部方法）
func (e *Embedder) doEmbedText(ctx context.Context, text string) ([]float32, error) {
	// 使用配置的嵌入模型
	model := e.config.Embedding.Model
	if model == "" {
		model = "text-embedding-3-small"
	}

	req := EmbeddingRequest{
		Model: model,
		Input: []string{text},
	}

	// 清理 baseURL：去除前后空格和尾部斜杠
	baseURL := strings.TrimSpace(e.config.Embedding.BaseURL)
	baseURL = strings.TrimSuffix(baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	// 构建请求
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败：%w", err)
	}

	requestURL := baseURL + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败：%w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// 使用配置的 API Key，如果没有则使用 OpenAI 配置的
	apiKey := strings.TrimSpace(e.config.Embedding.APIKey)
	if apiKey == "" && e.openAIConfig != nil {
		apiKey = e.openAIConfig.APIKey
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API Key 未配置")
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	// 发送请求
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败：%w", err)
	}
	defer resp.Body.Close()

	// 读取响应体以便在错误时输出详细信息
	bodyBytes := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			bodyBytes = append(bodyBytes, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	// 记录请求和响应信息（用于调试）
	requestBodyPreview := string(body)
	if len(requestBodyPreview) > 200 {
		requestBodyPreview = requestBodyPreview[:200] + "..."
	}
	e.logger.Debug("嵌入 API 请求",
		zap.String("url", httpReq.URL.String()),
		zap.String("model", model),
		zap.String("requestBody", requestBodyPreview),
		zap.Int("status", resp.StatusCode),
		zap.Int("bodySize", len(bodyBytes)),
		zap.String("contentType", resp.Header.Get("Content-Type")),
	)

	var embeddingResp EmbeddingResponse
	if err := json.Unmarshal(bodyBytes, &embeddingResp); err != nil {
		// 输出详细的错误信息
		bodyPreview := string(bodyBytes)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		return nil, fmt.Errorf("解析响应失败 (URL: %s, 状态码：%d, 响应长度：%d字节): %w\n请求体：%s\n响应内容预览：%s",
			requestURL, resp.StatusCode, len(bodyBytes), err, requestBodyPreview, bodyPreview)
	}

	if embeddingResp.Error != nil {
		return nil, fmt.Errorf("OpenAI API 错误 (状态码：%d): 类型=%s, 消息=%s",
			resp.StatusCode, embeddingResp.Error.Type, embeddingResp.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		bodyPreview := string(bodyBytes)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		return nil, fmt.Errorf("HTTP 请求失败 (URL: %s, 状态码：%d): 响应内容=%s", requestURL, resp.StatusCode, bodyPreview)
	}

	if len(embeddingResp.Data) == 0 {
		bodyPreview := string(bodyBytes)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		return nil, fmt.Errorf("未收到嵌入数据 (状态码：%d, 响应长度：%d字节)\n响应内容：%s",
			resp.StatusCode, len(bodyBytes), bodyPreview)
	}

	// 转换为 float32
	embedding := make([]float32, len(embeddingResp.Data[0].Embedding))
	for i, v := range embeddingResp.Data[0].Embedding {
		embedding[i] = float32(v)
	}

	return embedding, nil
}

// isRetryableError 判断是否是可重试的错误
func (e *Embedder) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// 429 速率限制错误
	if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
		return true
	}

	// 5xx 服务器错误
	if strings.Contains(errStr, "500") || strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") || strings.Contains(errStr, "504") {
		return true
	}

	// 网络错误
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "network") || strings.Contains(errStr, "EOF") {
		return true
	}

	return false
}

// EmbedTexts 批量嵌入文本
func (e *Embedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	embeddings := make([][]float32, len(texts))
	for i, text := range texts {
		embedding, err := e.EmbedText(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("嵌入文本 [%d] 失败：%w", i, err)
		}
		embeddings[i] = embedding
	}

	return embeddings, nil
}
