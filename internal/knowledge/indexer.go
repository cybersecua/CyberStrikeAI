package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/config"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Indexer 索引器，负责将知识项分块并向量化
type Indexer struct {
	db             *sql.DB
	embedder       *Embedder
	logger         *zap.Logger
	chunkSize      int // 每个块的最大 token 数（估算）
	overlap        int // 块之间的重叠 token 数
	maxChunks      int // 单个知识项的最大块数量（0 表示不限制）

	// 错误跟踪
	mu            sync.RWMutex
	lastError     string    // 最近一次错误信息
	lastErrorTime time.Time // 最近一次错误时间
	errorCount    int       // 连续错误计数

	// 重建索引状态跟踪
	rebuildMu          sync.RWMutex
	isRebuilding       bool      // 是否正在重建索引
	rebuildTotalItems  int       // 重建总项数
	rebuildCurrent     int       // 当前已处理项数
	rebuildFailed      int       // 重建失败项数
	rebuildStartTime   time.Time // 重建开始时间
	rebuildLastItemID  string    // 最近处理的项 ID
	rebuildLastChunks  int       // 最近处理的项的分块数
}

// NewIndexer 创建新的索引器
func NewIndexer(db *sql.DB, embedder *Embedder, logger *zap.Logger, indexingCfg *config.IndexingConfig) *Indexer {
	chunkSize := 512
	overlap := 50
	maxChunks := 0
	if indexingCfg != nil {
		if indexingCfg.ChunkSize > 0 {
			chunkSize = indexingCfg.ChunkSize
		}
		if indexingCfg.ChunkOverlap >= 0 {
			overlap = indexingCfg.ChunkOverlap
		}
		if indexingCfg.MaxChunksPerItem > 0 {
			maxChunks = indexingCfg.MaxChunksPerItem
		}
	}
	return &Indexer{
		db:        db,
		embedder:  embedder,
		logger:    logger,
		chunkSize: chunkSize,
		overlap:   overlap,
		maxChunks: maxChunks,
	}
}

// ChunkText 将文本分块（支持重叠，保留标题上下文）
func (idx *Indexer) ChunkText(text string) []string {
	// 按 Markdown 标题分割，获取带标题的块
	sections := idx.splitByMarkdownHeadersWithContent(text)

	// 处理每个块
	result := make([]string, 0)
	for _, section := range sections {
		// 构建父级标题路径（不包含最后一级标题，因为内容中已经包含）
		// 例如：["# A", "## B", "### C"] -> "[# A > ## B]"
		var parentHeaderPath string
		if len(section.HeaderPath) > 1 {
			parentHeaderPath = strings.Join(section.HeaderPath[:len(section.HeaderPath)-1], " > ")
		}

		// 提取内容的第一行作为标题（如 "# Prompt Injection"）
		firstLine, remainingContent := extractFirstLine(section.Content)

		// 如果剩余内容为空或只有空白，说明这个块只有标题没有正文，跳过
		if strings.TrimSpace(remainingContent) == "" {
			continue
		}

		// 如果块太大，进一步分割
		if idx.estimateTokens(section.Content) <= idx.chunkSize {
			// 块大小合适，添加父级标题前缀
			if parentHeaderPath != "" {
				result = append(result, fmt.Sprintf("[%s] %s", parentHeaderPath, section.Content))
			} else {
				result = append(result, section.Content)
			}
		} else {
			// 块太大，按子标题或段落分割，保持标题上下文
			// 首先尝试按子标题分割（保留子标题结构）
			subSections := idx.splitBySubHeaders(section.Content, firstLine, parentHeaderPath)
			if len(subSections) > 1 {
				// 成功按子标题分割，递归处理每个子块
				for _, sub := range subSections {
					if idx.estimateTokens(sub) <= idx.chunkSize {
						result = append(result, sub)
					} else {
						// 子块仍然太大，按段落分割（保留标题前缀）
						paragraphs := idx.splitByParagraphsWithHeader(sub, parentHeaderPath)
						for _, para := range paragraphs {
							if idx.estimateTokens(para) <= idx.chunkSize {
								result = append(result, para)
							} else {
								// 段落仍太大，按句子分割
								sentenceChunks := idx.splitBySentencesWithOverlap(para)
								for _, chunk := range sentenceChunks {
									result = append(result, chunk)
								}
							}
						}
					}
				}
			} else {
				// 没有子标题，按段落分割（保留标题前缀）
				paragraphs := idx.splitByParagraphsWithHeader(section.Content, parentHeaderPath)
				for _, para := range paragraphs {
					if idx.estimateTokens(para) <= idx.chunkSize {
						result = append(result, para)
					} else {
						// 段落仍太大，按句子分割
						sentenceChunks := idx.splitBySentencesWithOverlap(para)
						for _, chunk := range sentenceChunks {
							result = append(result, chunk)
						}
					}
				}
			}
		}
	}

	return result
}

// extractFirstLine 提取第一行内容和剩余内容
func extractFirstLine(content string) (firstLine, remaining string) {
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) == 0 {
		return "", ""
	}
	if len(lines) == 1 {
		return lines[0], ""
	}
	return lines[0], lines[1]
}

// splitBySubHeaders 尝试按子标题分割内容（用于处理大块内容）
// headerPrefix 是父级标题路径，用于添加到每个子块
func (idx *Indexer) splitBySubHeaders(content, headerPrefix, parentPath string) []string {
	// 匹配 Markdown 子标题（## 及以上）
	subHeaderRegex := regexp.MustCompile(`(?m)^#{2,6}\s+.+$`)
	matches := subHeaderRegex.FindAllStringIndex(content, -1)

	if len(matches) == 0 {
		// 没有子标题，返回原始内容
		return []string{content}
	}

	result := make([]string, 0, len(matches))
	for i, match := range matches {
		start := match[0]
		nextStart := len(content)
		if i+1 < len(matches) {
			nextStart = matches[i+1][0]
		}

		subContent := strings.TrimSpace(content[start:nextStart])

		// 添加父级路径前缀
		if parentPath != "" {
			result = append(result, fmt.Sprintf("[%s] %s", parentPath, subContent))
		} else {
			result = append(result, subContent)
		}
	}

	return result
}

// splitByParagraphsWithHeader 按段落分割，每个段落添加标题前缀（用于保持上下文）
func (idx *Indexer) splitByParagraphsWithHeader(content, parentPath string) []string {
	// 提取第一行作为标题
	firstLine, _ := extractFirstLine(content)

	paragraphs := strings.Split(content, "\n\n")
	result := make([]string, 0)

	for i, p := range paragraphs {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}

		// 过滤掉只有标题的段落（没有实际内容）
		if strings.TrimSpace(trimmed) == strings.TrimSpace(firstLine) {
			continue
		}

		// 第一个段落已经包含标题，不需要重复添加
		if i == 0 && strings.Contains(trimmed, firstLine) {
			if parentPath != "" {
				result = append(result, fmt.Sprintf("[%s] %s", parentPath, trimmed))
			} else {
				result = append(result, trimmed)
			}
		} else {
			// 其他段落添加标题前缀以保持上下文
			if parentPath != "" {
				result = append(result, fmt.Sprintf("[%s] %s\n%s", parentPath, firstLine, trimmed))
			} else {
				result = append(result, fmt.Sprintf("%s\n%s", firstLine, trimmed))
			}
		}
	}

	return result
}

// Section 表示一个带标题路径的文本块
type Section struct {
	HeaderPath []string // 标题路径（如 ["# SQL 注入", "## 检测方法"]）
	Content    string   // 块内容
}

// splitByMarkdownHeadersWithContent 按 Markdown 标题分割，返回带标题路径的块
// 每个块的内容包含自己的标题，用于向量化检索
//
// 例如，对于以下 Markdown:
//   # Prompt Injection
//   引言内容
//   ## Summary
//   目录内容
//
// 返回：
//   [{HeaderPath: ["# Prompt Injection"], Content: "# Prompt Injection\n引言内容"},
//    {HeaderPath: ["# Prompt Injection", "## Summary"], Content: "## Summary\n目录内容"}]
func (idx *Indexer) splitByMarkdownHeadersWithContent(text string) []Section {
	// 匹配 Markdown 标题 (# ## ### 等)
	headerRegex := regexp.MustCompile(`(?m)^#{1,6}\s+.+$`)

	// 找到所有标题位置
	matches := headerRegex.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		// 没有标题，返回整个文本
		return []Section{{HeaderPath: []string{}, Content: text}}
	}

	sections := make([]Section, 0, len(matches))
	currentHeaderPath := []string{}

	for i, match := range matches {
		start := match[0]
		end := match[1]
		nextStart := len(text)

		// 找到下一个标题的位置
		if i+1 < len(matches) {
			nextStart = matches[i+1][0]
		}

		// 提取当前标题
		headerLine := strings.TrimSpace(text[start:end])

		// 计算标题层级（# 的数量）
		level := 0
		for _, ch := range headerLine {
			if ch == '#' {
				level++
			} else {
				break
			}
		}

		// 更新标题路径：移除比当前层级深或等于的子标题，然后添加当前标题
		newPath := make([]string, 0, len(currentHeaderPath)+1)
		for _, h := range currentHeaderPath {
			hLevel := 0
			for _, ch := range h {
				if ch == '#' {
					hLevel++
				} else {
					break
				}
			}
			if hLevel < level {
				newPath = append(newPath, h)
			}
		}
		newPath = append(newPath, headerLine)
		currentHeaderPath = newPath

		// 提取当前标题到下一个标题之间的内容（包含当前标题）
		content := strings.TrimSpace(text[start:nextStart])

		// 创建块，使用当前标题路径（包含当前标题）
		sections = append(sections, Section{
			HeaderPath: append([]string(nil), currentHeaderPath...),
			Content:    content,
		})
	}

	// 过滤空块
	result := make([]Section, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section.Content) != "" {
			result = append(result, section)
		}
	}

	if len(result) == 0 {
		return []Section{{HeaderPath: []string{}, Content: text}}
	}

	return result
}

// splitByParagraphs 按段落分割
func (idx *Indexer) splitByParagraphs(text string) []string {
	paragraphs := strings.Split(text, "\n\n")
	result := make([]string, 0)
	for _, p := range paragraphs {
		if strings.TrimSpace(p) != "" {
			result = append(result, strings.TrimSpace(p))
		}
	}
	return result
}

// splitBySentences 按句子分割（用于内部，不包含重叠逻辑）
func (idx *Indexer) splitBySentences(text string) []string {
	// 简单的句子分割（按句号、问号、感叹号，支持中英文）
	// . ! ? = 英文标点
	// \u3002 = 。(中文句号)
	// \uFF01 = ！(中文叹号)
	// \uFF1F = ？(中文问号)
	sentenceRegex := regexp.MustCompile(`[.!?\x{3002}\x{FF01}\x{FF1F}]+`)
	sentences := sentenceRegex.Split(text, -1)
	result := make([]string, 0)
	for _, s := range sentences {
		if strings.TrimSpace(s) != "" {
			result = append(result, strings.TrimSpace(s))
		}
	}
	return result
}

// splitBySentencesWithOverlap 按句子分割并应用重叠策略
func (idx *Indexer) splitBySentencesWithOverlap(text string) []string {
	if idx.overlap <= 0 {
		// 如果没有重叠，使用简单分割
		return idx.splitBySentencesSimple(text)
	}

	sentences := idx.splitBySentences(text)
	if len(sentences) == 0 {
		return []string{}
	}

	result := make([]string, 0)
	currentChunk := ""

	for _, sentence := range sentences {
		testChunk := currentChunk
		if testChunk != "" {
			testChunk += "\n"
		}
		testChunk += sentence

		testTokens := idx.estimateTokens(testChunk)

		if testTokens > idx.chunkSize && currentChunk != "" {
			// 当前块已达到大小限制，保存它
			result = append(result, currentChunk)

			// 从当前块的末尾提取重叠部分
			overlapText := idx.extractLastTokens(currentChunk, idx.overlap)
			if overlapText != "" {
				// 如果有重叠内容，作为下一个块的起始
				currentChunk = overlapText + "\n" + sentence
			} else {
				// 如果无法提取足够的重叠内容，直接使用当前句子
				currentChunk = sentence
			}
		} else {
			currentChunk = testChunk
		}
	}

	// 添加最后一个块
	if strings.TrimSpace(currentChunk) != "" {
		result = append(result, currentChunk)
	}

	// 过滤空块
	filtered := make([]string, 0)
	for _, chunk := range result {
		if strings.TrimSpace(chunk) != "" {
			filtered = append(filtered, chunk)
		}
	}

	return filtered
}

// splitBySentencesSimple 按句子分割（简单版本，无重叠）
func (idx *Indexer) splitBySentencesSimple(text string) []string {
	sentences := idx.splitBySentences(text)
	result := make([]string, 0)
	currentChunk := ""

	for _, sentence := range sentences {
		testChunk := currentChunk
		if testChunk != "" {
			testChunk += "\n"
		}
		testChunk += sentence

		if idx.estimateTokens(testChunk) > idx.chunkSize && currentChunk != "" {
			result = append(result, currentChunk)
			currentChunk = sentence
		} else {
			currentChunk = testChunk
		}
	}
	if currentChunk != "" {
		result = append(result, currentChunk)
	}

	return result
}

// extractLastTokens 从文本末尾提取指定 token 数量的内容
func (idx *Indexer) extractLastTokens(text string, tokenCount int) string {
	if tokenCount <= 0 || text == "" {
		return ""
	}

	// 估算字符数（1 token ≈ 4 字符）
	charCount := tokenCount * 4
	runes := []rune(text)

	if len(runes) <= charCount {
		return text
	}

	// 从末尾提取指定数量的字符
	startPos := len(runes) - charCount
	extracted := string(runes[startPos:])

	// 尝试找到第一个句子边界（支持中英文标点）
	sentenceBoundary := regexp.MustCompile(`[.!?\x{3002}\x{FF01}\x{FF1F}]+`)
	matches := sentenceBoundary.FindStringIndex(extracted)
	if len(matches) > 0 && matches[0] > 0 {
		// 在句子边界处截断，保留完整句子
		extracted = extracted[matches[0]:]
	}

	return strings.TrimSpace(extracted)
}

// estimateTokens 估算 token 数（简单估算：1 token ≈ 4 字符）
func (idx *Indexer) estimateTokens(text string) int {
	return len([]rune(text)) / 4
}

// IndexItem 索引知识项（分块并向量化）
func (idx *Indexer) IndexItem(ctx context.Context, itemID string) error {
	// 获取知识项（包含 category 和 title，用于向量化）
	var content, category, title string
	err := idx.db.QueryRow("SELECT content, category, title FROM knowledge_base_items WHERE id = ?", itemID).Scan(&content, &category, &title)
	if err != nil {
		return fmt.Errorf("获取知识项失败：%w", err)
	}

	// 删除旧的向量（在 RebuildIndex 中已经统一清空，这里保留是为了单独调用 IndexItem 时的兼容性）
	_, err = idx.db.Exec("DELETE FROM knowledge_embeddings WHERE item_id = ?", itemID)
	if err != nil {
		return fmt.Errorf("删除旧向量失败：%w", err)
	}

	// 分块
	chunks := idx.ChunkText(content)

	// 应用最大块数限制
	if idx.maxChunks > 0 && len(chunks) > idx.maxChunks {
		idx.logger.Info("知识项块数量超过限制，已截断",
			zap.String("itemId", itemID),
			zap.Int("originalChunks", len(chunks)),
			zap.Int("maxChunks", idx.maxChunks))
		chunks = chunks[:idx.maxChunks]
	}

	idx.logger.Info("知识项分块完成", zap.String("itemId", itemID), zap.Int("chunks", len(chunks)))

	// 跟踪该知识项的错误
	itemErrorCount := 0
	var firstError error
	firstErrorChunkIndex := -1

	// 向量化每个块（包含 category 和 title 信息，以便向量检索时能匹配到风险类型）
	for i, chunk := range chunks {
		// 将 category 和 title 信息包含到向量化的文本中
		// 格式："[风险类型：{category}] [标题：{title}]\n{chunk 内容}"
		// 这样向量嵌入就会包含风险类型信息，即使 SQL 过滤失败，向量相似度也能帮助匹配
		textForEmbedding := fmt.Sprintf("[风险类型：%s] [标题：%s]\n%s", category, title, chunk)

		embedding, err := idx.embedder.EmbedText(ctx, textForEmbedding)
		if err != nil {
			itemErrorCount++
			if firstError == nil {
				firstError = err
				firstErrorChunkIndex = i
				// 只在第一个块失败时记录详细日志
				chunkPreview := chunk
				if len(chunkPreview) > 200 {
					chunkPreview = chunkPreview[:200] + "..."
				}
				idx.logger.Warn("向量化失败",
					zap.String("itemId", itemID),
					zap.Int("chunkIndex", i),
					zap.Int("totalChunks", len(chunks)),
					zap.String("chunkPreview", chunkPreview),
					zap.Error(err),
				)

				// 更新全局错误跟踪
				errorMsg := fmt.Sprintf("向量化失败 (知识项：%s): %v", itemID, err)
				idx.mu.Lock()
				idx.lastError = errorMsg
				idx.lastErrorTime = time.Now()
				idx.mu.Unlock()
			}

			// 如果连续失败 5 个块，立即停止处理该知识项
			// 这样可以避免继续浪费 API 调用，同时也能更快地检测到配置问题
			// 对于大文档（超过 10 个块），允许失败比例不超过 50%
			maxConsecutiveFailures := 5
			if len(chunks) > 10 && itemErrorCount > len(chunks)/2 {
				idx.logger.Error("知识项向量化失败比例过高，停止处理",
					zap.String("itemId", itemID),
					zap.Int("totalChunks", len(chunks)),
					zap.Int("failedChunks", itemErrorCount),
					zap.Int("firstErrorChunkIndex", firstErrorChunkIndex),
					zap.Error(firstError),
				)
				return fmt.Errorf("知识项向量化失败比例过高 (%d/%d个块失败): %v", itemErrorCount, len(chunks), firstError)
			}
			if itemErrorCount >= maxConsecutiveFailures {
				idx.logger.Error("知识项连续向量化失败，停止处理",
					zap.String("itemId", itemID),
					zap.Int("totalChunks", len(chunks)),
					zap.Int("failedChunks", itemErrorCount),
					zap.Int("firstErrorChunkIndex", firstErrorChunkIndex),
					zap.Error(firstError),
				)
				return fmt.Errorf("知识项连续向量化失败 (%d个块失败): %v", itemErrorCount, firstError)
			}
			continue
		}

		// 保存向量
		chunkID := uuid.New().String()
		embeddingJSON, _ := json.Marshal(embedding)

		_, err = idx.db.Exec(
			"INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, created_at) VALUES (?, ?, ?, ?, ?, datetime('now'))",
			chunkID, itemID, i, chunk, string(embeddingJSON),
		)
		if err != nil {
			idx.logger.Warn("保存向量失败", zap.String("itemId", itemID), zap.Int("chunkIndex", i), zap.Error(err))
			continue
		}
	}

	idx.logger.Info("知识项索引完成", zap.String("itemId", itemID), zap.Int("chunks", len(chunks)))

	// 更新重建状态中的最近处理信息
	idx.rebuildMu.Lock()
	idx.rebuildLastItemID = itemID
	idx.rebuildLastChunks = len(chunks)
	idx.rebuildMu.Unlock()

	return nil
}

// HasIndex 检查是否存在索引
func (idx *Indexer) HasIndex() (bool, error) {
	var count int
	err := idx.db.QueryRow("SELECT COUNT(*) FROM knowledge_embeddings").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("检查索引失败：%w", err)
	}
	return count > 0, nil
}

// RebuildIndex 重建所有索引
func (idx *Indexer) RebuildIndex(ctx context.Context) error {
	// 设置重建状态
	idx.rebuildMu.Lock()
	idx.isRebuilding = true
	idx.rebuildTotalItems = 0
	idx.rebuildCurrent = 0
	idx.rebuildFailed = 0
	idx.rebuildStartTime = time.Now()
	idx.rebuildLastItemID = ""
	idx.rebuildLastChunks = 0
	idx.rebuildMu.Unlock()

	// 重置错误跟踪
	idx.mu.Lock()
	idx.lastError = ""
	idx.lastErrorTime = time.Time{}
	idx.errorCount = 0
	idx.mu.Unlock()

	rows, err := idx.db.Query("SELECT id FROM knowledge_base_items")
	if err != nil {
		// 重置重建状态
		idx.rebuildMu.Lock()
		idx.isRebuilding = false
		idx.rebuildMu.Unlock()
		return fmt.Errorf("查询知识项失败：%w", err)
	}
	defer rows.Close()

	var itemIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			// 重置重建状态
			idx.rebuildMu.Lock()
			idx.isRebuilding = false
			idx.rebuildMu.Unlock()
			return fmt.Errorf("扫描知识项 ID 失败：%w", err)
		}
		itemIDs = append(itemIDs, id)
	}

	idx.rebuildMu.Lock()
	idx.rebuildTotalItems = len(itemIDs)
	idx.rebuildMu.Unlock()

	idx.logger.Info("开始重建索引", zap.Int("totalItems", len(itemIDs)))

	// 注意：不再清空所有旧索引，而是按增量方式更新
	// 每个知识项在 IndexItem 中会先删除自己的旧向量，然后插入新向量
	// 这样配置更新后只重新索引变化的知识项，保留其他知识项的索引

	failedCount := 0
	consecutiveFailures := 0
	maxConsecutiveFailures := 5 // 连续失败 5 次后立即停止（允许偶尔的临时错误）
	firstFailureItemID := ""
	var firstFailureError error

	for i, itemID := range itemIDs {
		if err := idx.IndexItem(ctx, itemID); err != nil {
			failedCount++
			consecutiveFailures++

			// 只在第一个失败时记录详细日志
			if consecutiveFailures == 1 {
				firstFailureItemID = itemID
				firstFailureError = err
				idx.logger.Warn("索引知识项失败",
					zap.String("itemId", itemID),
					zap.Int("totalItems", len(itemIDs)),
					zap.Error(err),
				)
			}

			// 如果连续失败过多，可能是配置问题，立即停止索引
			if consecutiveFailures >= maxConsecutiveFailures {
				errorMsg := fmt.Sprintf("连续 %d 个知识项索引失败，可能存在配置问题（如嵌入模型配置错误、API 密钥无效、余额不足等）。第一个失败项：%s, 错误：%v", consecutiveFailures, firstFailureItemID, firstFailureError)
				idx.mu.Lock()
				idx.lastError = errorMsg
				idx.lastErrorTime = time.Now()
				idx.mu.Unlock()

				idx.logger.Error("连续索引失败次数过多，立即停止索引",
					zap.Int("consecutiveFailures", consecutiveFailures),
					zap.Int("totalItems", len(itemIDs)),
					zap.Int("processedItems", i+1),
					zap.String("firstFailureItemId", firstFailureItemID),
					zap.Error(firstFailureError),
				)
				return fmt.Errorf("连续索引失败次数过多：%v", firstFailureError)
			}

			// 如果失败的知识项过多，记录警告但继续处理（降低阈值到 30%）
			if failedCount > len(itemIDs)*3/10 && failedCount == len(itemIDs)*3/10+1 {
				errorMsg := fmt.Sprintf("索引失败的知识项过多 (%d/%d)，可能存在配置问题。第一个失败项：%s, 错误：%v", failedCount, len(itemIDs), firstFailureItemID, firstFailureError)
				idx.mu.Lock()
				idx.lastError = errorMsg
				idx.lastErrorTime = time.Now()
				idx.mu.Unlock()

				idx.logger.Error("索引失败的知识项过多，可能存在配置问题",
					zap.Int("failedCount", failedCount),
					zap.Int("totalItems", len(itemIDs)),
					zap.String("firstFailureItemId", firstFailureItemID),
					zap.Error(firstFailureError),
				)
			}
			continue
		}

		// 成功时重置连续失败计数和第一个失败信息
		if consecutiveFailures > 0 {
			consecutiveFailures = 0
			firstFailureItemID = ""
			firstFailureError = nil
		}

		// 更新重建进度
		idx.rebuildMu.Lock()
		idx.rebuildCurrent = i + 1
		idx.rebuildFailed = failedCount
		idx.rebuildMu.Unlock()

		// 减少进度日志频率（每 10 个或每 10% 记录一次）
		if (i+1)%10 == 0 || (len(itemIDs) > 0 && (i+1)*100/len(itemIDs)%10 == 0 && (i+1)*100/len(itemIDs) > 0) {
			idx.logger.Info("索引进度", zap.Int("current", i+1), zap.Int("total", len(itemIDs)), zap.Int("failed", failedCount))
		}
	}

	// 重置重建状态
	idx.rebuildMu.Lock()
	idx.isRebuilding = false
	idx.rebuildMu.Unlock()

	idx.logger.Info("索引重建完成", zap.Int("totalItems", len(itemIDs)), zap.Int("failedCount", failedCount))
	return nil
}

// GetLastError 获取最近一次错误信息
func (idx *Indexer) GetLastError() (string, time.Time) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.lastError, idx.lastErrorTime
}

// GetRebuildStatus 获取重建索引状态
func (idx *Indexer) GetRebuildStatus() (isRebuilding bool, totalItems int, current int, failed int, lastItemID string, lastChunks int, startTime time.Time) {
	idx.rebuildMu.RLock()
	defer idx.rebuildMu.RUnlock()
	return idx.isRebuilding, idx.rebuildTotalItems, idx.rebuildCurrent, idx.rebuildFailed, idx.rebuildLastItemID, idx.rebuildLastChunks, idx.rebuildStartTime
}
