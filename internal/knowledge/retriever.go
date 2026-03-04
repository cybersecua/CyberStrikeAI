package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"go.uber.org/zap"
)

// Retriever knowledge retriever
type Retriever struct {
	db        *sql.DB
	embedder  *Embedder
	config    *RetrievalConfig
	logger    *zap.Logger
	bm25Index *BM25CorpusIndexer // corpus-level BM25 index rebuilt from all chunks
}

// RetrievalConfig retrieval configuration
type RetrievalConfig struct {
	TopK                int
	SimilarityThreshold float64
	HybridWeight        float64
}

// NewRetriever creates a new retriever
func NewRetriever(db *sql.DB, embedder *Embedder, config *RetrievalConfig, logger *zap.Logger) *Retriever {
	r := &Retriever{
		db:        db,
		embedder:  embedder,
		config:    config,
		logger:    logger,
		bm25Index: NewBM25CorpusIndexer(),
	}
	// Build corpus BM25 index from existing chunks asynchronously.
	go r.RebuildBM25Index()
	return r
}

// RebuildBM25Index fetches all chunk texts from the database and rebuilds the
// corpus-level BM25 index.  Safe to call concurrently and at any time.
func (r *Retriever) RebuildBM25Index() {
	rows, err := r.db.Query(`SELECT id, chunk_text FROM knowledge_embeddings`)
	if err != nil {
		r.logger.Warn("BM25 rebuild: query failed", zap.Error(err))
		return
	}
	defer rows.Close()

	chunks := make(map[string]string)
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			continue
		}
		chunks[id] = text
	}
	r.bm25Index.Rebuild(chunks)
	r.logger.Info("BM25 corpus index rebuilt", zap.Int("chunks", len(chunks)))
}

// UpdateConfig updates the retrieval configuration
func (r *Retriever) UpdateConfig(config *RetrievalConfig) {
	if config != nil {
		r.config = config
		r.logger.Info("retriever configuration updated",
			zap.Int("top_k", config.TopK),
			zap.Float64("similarity_threshold", config.SimilarityThreshold),
			zap.Float64("hybrid_weight", config.HybridWeight),
		)
	}
}

// cosineSimilarity calculates cosine similarity
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0.0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// bm25Score computes the BM25 Okapi score for an arbitrary text against a query.
// It delegates to the corpus-level BM25CorpusIndexer which holds real IDF weights
// built from all indexed knowledge chunks.  When the corpus index is empty (e.g.
// no knowledge base loaded) it automatically falls back to the single-document
// approximation so the retriever still works correctly.
func (r *Retriever) bm25Score(query, text string) float64 {
	score := r.bm25Index.ScoreText(query, text)
	// Normalise to 0–1 for compatibility with the hybrid scoring logic below.
	// BM25 scores have no upper bound so we apply a soft-max via tanh.
	return math.Tanh(score)
}

// Search searches the knowledge base
func (r *Retriever) Search(ctx context.Context, req *SearchRequest) ([]*RetrievalResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = r.config.TopK
	}
	if topK == 0 {
		topK = 5
	}

	threshold := req.Threshold
	if threshold <= 0 {
		threshold = r.config.SimilarityThreshold
	}
	if threshold == 0 {
		threshold = 0.7
	}

	// vectorize query (if risk_type is provided, include it in the query text for better matching)
	queryText := req.Query
	if req.RiskType != "" {
		// include risk_type information in the query, keeping the format consistent with indexing
		queryText = fmt.Sprintf("[Risk Type: %s] %s", req.RiskType, req.Query)
	}
	queryEmbedding, err := r.embedder.EmbedText(ctx, queryText)
	if err != nil {
		return nil, fmt.Errorf("failed to vectorize query: %w", err)
	}

	// query all vectors (or filter by risk type)
	// use exact match (=) for better performance and accuracy
	// since the system provides a built-in tool to get the risk type list, users should use accurate category names
		// also, the category information is already embedded in the vector, so even if SQL filtering doesn't fully match,
		// vector similarity can help with matching
		var rows *sql.Rows
		if req.RiskType != "" {
			// use exact match (=) for better performance and accuracy
			// use COLLATE NOCASE for case-insensitive matching to improve fault tolerance
			// note: if the user's risk_type doesn't exactly match the category, it may not match
			// it is recommended to first call the corresponding built-in tool to get accurate category names
		rows, err = r.db.Query(`
			SELECT e.id, e.item_id, e.chunk_index, e.chunk_text, e.embedding, i.category, i.title
			FROM knowledge_embeddings e
			JOIN knowledge_base_items i ON e.item_id = i.id
			WHERE i.category = ? COLLATE NOCASE
		`, req.RiskType)
	} else {
		rows, err = r.db.Query(`
			SELECT e.id, e.item_id, e.chunk_index, e.chunk_text, e.embedding, i.category, i.title
			FROM knowledge_embeddings e
			JOIN knowledge_base_items i ON e.item_id = i.id
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query vectors: %w", err)
	}
	defer rows.Close()

	// calculate similarity
	type candidate struct {
		chunk                 *KnowledgeChunk
		item                  *KnowledgeItem
		similarity            float64
		bm25Score             float64
		hasStrongKeywordMatch bool
		hybridScore           float64 // hybrid score for final sorting
	}

	candidates := make([]candidate, 0)

	for rows.Next() {
		var chunkID, itemID, chunkText, embeddingJSON, category, title string
		var chunkIndex int

		if err := rows.Scan(&chunkID, &itemID, &chunkIndex, &chunkText, &embeddingJSON, &category, &title); err != nil {
			r.logger.Warn("failed to scan vector", zap.Error(err))
			continue
		}

		// parse vector
		var embedding []float32
		if err := json.Unmarshal([]byte(embeddingJSON), &embedding); err != nil {
			r.logger.Warn("failed to parse vector", zap.Error(err))
			continue
		}

		// calculate cosine similarity
		similarity := cosineSimilarity(queryEmbedding, embedding)

		// calculate BM25 score (considering chunk text, category, and title)
		// category and title are structured fields and should be prioritized when fully matched
		chunkBM25 := r.bm25Score(req.Query, chunkText)
		categoryBM25 := r.bm25Score(req.Query, category)
		titleBM25 := r.bm25Score(req.Query, title)

		// check if category or title has a significant match (important for structured fields)
		hasStrongKeywordMatch := categoryBM25 > 0.3 || titleBM25 > 0.3

		// combined BM25 score (for subsequent sorting)
		bm25Score := math.Max(math.Max(chunkBM25, categoryBM25), titleBM25)

		// collect all candidates (no strict filtering yet, to handle cross-language cases later)
		// only filter out very low similarity results (< 0.1) to avoid noise
		if similarity < 0.1 {
			continue
		}

		chunk := &KnowledgeChunk{
			ID:         chunkID,
			ItemID:     itemID,
			ChunkIndex: chunkIndex,
			ChunkText:  chunkText,
			Embedding:  embedding,
		}

		item := &KnowledgeItem{
			ID:       itemID,
			Category: category,
			Title:    title,
		}

		candidates = append(candidates, candidate{
			chunk:                 chunk,
			item:                  item,
			similarity:            similarity,
			bm25Score:             bm25Score,
			hasStrongKeywordMatch: hasStrongKeywordMatch,
		})
	}

	// sort by similarity first (using more efficient sorting)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].similarity > candidates[j].similarity
	})

	// smart filtering strategy: prioritize keyword-matched results; use a more relaxed threshold for cross-language queries
	filteredCandidates := make([]candidate, 0)

	// check if any keyword matches (to determine if this is a cross-language query)
	hasAnyKeywordMatch := false
	for _, cand := range candidates {
		if cand.hasStrongKeywordMatch {
			hasAnyKeywordMatch = true
			break
		}
	}

	// check the highest similarity, to determine if there is actually relevant content
	maxSimilarity := 0.0
	if len(candidates) > 0 {
		maxSimilarity = candidates[0].similarity
	}

	// apply smart filtering
	// if the user set a high threshold (>=0.8), strictly respect the threshold and reduce automatic relaxation
	strictMode := threshold >= 0.8

	// use different threshold strategies based on whether there are keyword matches
	// in strict mode, disable cross-language relaxation and strictly follow the user-set threshold
	effectiveThreshold := threshold
	if !strictMode && !hasAnyKeywordMatch {
		// in non-strict mode, no keyword matches may indicate a cross-language query, moderately relax the threshold
		// but even for cross-language queries, don't blindly lower the threshold; ensure minimum relevance
		// cross-language threshold set to 0.6 to ensure some relevance in returned results
		effectiveThreshold = math.Max(threshold*0.85, 0.6)
		r.logger.Debug("possible cross-language query detected, using relaxed threshold",
			zap.Float64("originalThreshold", threshold),
			zap.Float64("effectiveThreshold", effectiveThreshold),
		)
	} else if strictMode {
		// in strict mode, respect the threshold even without keyword matches
		r.logger.Debug("strict mode: strictly respecting user-set threshold",
			zap.Float64("threshold", threshold),
			zap.Bool("hasKeywordMatch", hasAnyKeywordMatch),
		)
	}
	for _, cand := range candidates {
		if cand.similarity >= effectiveThreshold {
			// meets threshold, pass directly
			filteredCandidates = append(filteredCandidates, cand)
		} else if !strictMode && cand.hasStrongKeywordMatch {
			// in non-strict mode, has keyword match but similarity slightly below threshold, relax slightly
			// in strict mode, respect the threshold even with keyword matches
			relaxedThreshold := math.Max(effectiveThreshold*0.85, 0.55)
			if cand.similarity >= relaxedThreshold {
				filteredCandidates = append(filteredCandidates, cand)
			}
		}
		// if neither keyword match nor above threshold, filter out
	}

	// smart fallback strategy: only consider returning results if the highest similarity is at a reasonable level
	// if the highest similarity is very low (<0.55), there is truly no relevant content and an empty result should be returned
	// in strict mode (threshold>=0.8), disable fallback strategy and strictly respect the user-set threshold
	if len(filteredCandidates) == 0 && len(candidates) > 0 && !strictMode {
		// even if no candidates passed the threshold filter, if the highest similarity is acceptable (>=0.55), consider returning Top-K
		// but this is a last resort, only used when there is some relevance
		// do not use fallback in strict mode
		minAcceptableSimilarity := 0.55
		if maxSimilarity >= minAcceptableSimilarity {
			r.logger.Debug("no results after filtering, but highest similarity is acceptable, returning Top-K results",
				zap.Int("totalCandidates", len(candidates)),
				zap.Float64("maxSimilarity", maxSimilarity),
				zap.Float64("effectiveThreshold", effectiveThreshold),
			)
			maxResults := topK
			if len(candidates) < maxResults {
				maxResults = len(candidates)
			}
			// only return results with similarity >= 0.55
			for _, cand := range candidates {
				if cand.similarity >= minAcceptableSimilarity && len(filteredCandidates) < maxResults {
					filteredCandidates = append(filteredCandidates, cand)
				}
			}
		} else {
			r.logger.Debug("no results after filtering, and highest similarity is too low, returning empty result",
				zap.Int("totalCandidates", len(candidates)),
				zap.Float64("maxSimilarity", maxSimilarity),
				zap.Float64("minAcceptableSimilarity", minAcceptableSimilarity),
			)
		}
	} else if len(filteredCandidates) == 0 && strictMode {
		// in strict mode, if no results after filtering, return empty directly without fallback
		r.logger.Debug("strict mode: no results after filtering, strictly respecting threshold, returning empty result",
			zap.Float64("threshold", threshold),
			zap.Float64("maxSimilarity", maxSimilarity),
		)
	} else if len(filteredCandidates) > topK {
		// if too many results after filtering, only keep Top-K
		filteredCandidates = filteredCandidates[:topK]
	}

	candidates = filteredCandidates

	// hybrid sorting (vector similarity + BM25)
	// note: hybridWeight can be 0.0 (pure keyword retrieval), so no default value is set here
	// if not set in the config, a default value should be used when loading the config
	hybridWeight := r.config.HybridWeight
	// if not set, use default value 0.7 (weighted towards vector retrieval)
	if hybridWeight < 0 || hybridWeight > 1 {
		r.logger.Warn("hybrid weight out of range, using default value 0.7",
			zap.Float64("provided", hybridWeight))
		hybridWeight = 0.7
	}

	// calculate hybrid scores and store in candidates for sorting
	for i := range candidates {
		normalizedBM25 := math.Min(candidates[i].bm25Score, 1.0)
		candidates[i].hybridScore = hybridWeight*candidates[i].similarity + (1-hybridWeight)*normalizedBM25

		// debug log: record score calculation for top few candidates (debug level only)
		if i < 3 {
			r.logger.Debug("hybrid score calculation",
				zap.Int("index", i),
				zap.Float64("similarity", candidates[i].similarity),
				zap.Float64("bm25Score", candidates[i].bm25Score),
				zap.Float64("normalizedBM25", normalizedBM25),
				zap.Float64("hybridWeight", hybridWeight),
				zap.Float64("hybridScore", candidates[i].hybridScore))
		}
	}

	// re-sort by hybrid score (this is the real hybrid retrieval)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].hybridScore > candidates[j].hybridScore
	})

	// convert to results
	results := make([]*RetrievalResult, len(candidates))
	for i, cand := range candidates {
		results[i] = &RetrievalResult{
			Chunk:      cand.chunk,
			Item:       cand.item,
			Similarity: cand.similarity,
			Score:      cand.hybridScore,
		}
	}

	// context expansion: add related chunks from the same document for each matched chunk
	// this prevents cases where a description and payload are split into different chunks,
	// returning only the description while losing the payload
	results = r.expandContext(ctx, results)

	return results, nil
}

// expandContext expands the context of retrieval results.
// For each matched chunk, automatically includes related chunks from the same document
// (especially chunks containing code blocks or payloads).
func (r *Retriever) expandContext(ctx context.Context, results []*RetrievalResult) []*RetrievalResult {
	if len(results) == 0 {
		return results
	}

	// collect all matched document IDs
	itemIDs := make(map[string]bool)
	for _, result := range results {
		itemIDs[result.Item.ID] = true
	}

	// load all chunks for each document
	itemChunksMap := make(map[string][]*KnowledgeChunk)
	for itemID := range itemIDs {
		chunks, err := r.loadAllChunksForItem(itemID)
		if err != nil {
			r.logger.Warn("failed to load document chunks", zap.String("itemId", itemID), zap.Error(err))
			continue
		}
		itemChunksMap[itemID] = chunks
	}

	// group results by document, expand each document only once
	resultsByItem := make(map[string][]*RetrievalResult)
	for _, result := range results {
		itemID := result.Item.ID
		resultsByItem[itemID] = append(resultsByItem[itemID], result)
	}

	// expand results for each document
	expandedResults := make([]*RetrievalResult, 0, len(results))
	processedChunkIDs := make(map[string]bool) // avoid adding duplicates

	for itemID, itemResults := range resultsByItem {
		// get all chunks for this document
		allChunks, exists := itemChunksMap[itemID]
		if !exists {
			// if chunks can't be loaded, add original results directly
			for _, result := range itemResults {
				if !processedChunkIDs[result.Chunk.ID] {
					expandedResults = append(expandedResults, result)
					processedChunkIDs[result.Chunk.ID] = true
				}
			}
			continue
		}

		// add original results
		for _, result := range itemResults {
			if !processedChunkIDs[result.Chunk.ID] {
				expandedResults = append(expandedResults, result)
				processedChunkIDs[result.Chunk.ID] = true
			}
		}

		// collect adjacent chunks to expand for the matched chunks of this document
		// strategy: only expand the top 3 matched chunks by hybrid score to avoid expanding too many
		// sort by hybrid score first, expand only the top 3 (use hybrid score not similarity)
		sortedItemResults := make([]*RetrievalResult, len(itemResults))
		copy(sortedItemResults, itemResults)
		sort.Slice(sortedItemResults, func(i, j int) bool {
			return sortedItemResults[i].Score > sortedItemResults[j].Score
		})

		// expand only the top 3 (or all, if fewer than 3)
		maxExpandFrom := 3
		if len(sortedItemResults) < maxExpandFrom {
			maxExpandFrom = len(sortedItemResults)
		}

		// use a map to deduplicate, avoid adding the same chunk multiple times
		relatedChunksMap := make(map[string]*KnowledgeChunk)

		for i := 0; i < maxExpandFrom; i++ {
			result := sortedItemResults[i]
			// find related chunks (2 above and below, excluding already-processed chunks)
			relatedChunks := r.findRelatedChunks(result.Chunk, allChunks, processedChunkIDs)
			for _, relatedChunk := range relatedChunks {
				// use chunk ID as key for deduplication
				if !processedChunkIDs[relatedChunk.ID] {
					relatedChunksMap[relatedChunk.ID] = relatedChunk
				}
			}
		}

		// limit the maximum number of chunks expanded per document (avoid expanding too many)
		// strategy: expand at most 8 chunks regardless of how many matched chunks there are
		// this avoids expanding too many chunks when multiple matched chunks are scattered across the document
		maxExpandPerItem := 8

		// convert related chunks to a slice and sort by index, preferring chunks closest to matched chunks
		relatedChunksList := make([]*KnowledgeChunk, 0, len(relatedChunksMap))
		for _, chunk := range relatedChunksMap {
			relatedChunksList = append(relatedChunksList, chunk)
		}

		// calculate the distance from each related chunk to the nearest matched chunk, sort by distance
		sort.Slice(relatedChunksList, func(i, j int) bool {
			// calculate distance to the nearest matched chunk
			minDistI := len(allChunks)
			minDistJ := len(allChunks)
			for _, result := range itemResults {
				distI := abs(relatedChunksList[i].ChunkIndex - result.Chunk.ChunkIndex)
				distJ := abs(relatedChunksList[j].ChunkIndex - result.Chunk.ChunkIndex)
				if distI < minDistI {
					minDistI = distI
				}
				if distJ < minDistJ {
					minDistJ = distJ
				}
			}
			return minDistI < minDistJ
		})

		// limit count
		if len(relatedChunksList) > maxExpandPerItem {
			relatedChunksList = relatedChunksList[:maxExpandPerItem]
		}

		// add deduplicated related chunks
		// use the result with the highest hybrid score in this document as reference
		maxScore := 0.0
		maxSimilarity := 0.0
		for _, result := range itemResults {
			if result.Score > maxScore {
				maxScore = result.Score
			}
			if result.Similarity > maxSimilarity {
				maxSimilarity = result.Similarity
			}
		}

		// calculate hybrid score for expanded chunks (using the same hybrid weight)
		hybridWeight := r.config.HybridWeight
		expandedSimilarity := maxSimilarity * 0.8 // related chunks have slightly lower similarity
		// for expanded chunks, BM25 score is 0 (they are context expansions, not direct matches)
		expandedBM25 := 0.0
		expandedScore := hybridWeight*expandedSimilarity + (1-hybridWeight)*expandedBM25

		for _, relatedChunk := range relatedChunksList {
			expandedResult := &RetrievalResult{
				Chunk:      relatedChunk,
				Item:       itemResults[0].Item, // use the Item info from the first result
				Similarity: expandedSimilarity,
				Score:      expandedScore, // use the correct hybrid score
			}
			expandedResults = append(expandedResults, expandedResult)
			processedChunkIDs[relatedChunk.ID] = true
		}
	}

	return expandedResults
}

// loadAllChunksForItem loads all chunks for a document
func (r *Retriever) loadAllChunksForItem(itemID string) ([]*KnowledgeChunk, error) {
	rows, err := r.db.Query(`
		SELECT id, item_id, chunk_index, chunk_text, embedding
		FROM knowledge_embeddings
		WHERE item_id = ?
		ORDER BY chunk_index
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to query chunks: %w", err)
	}
	defer rows.Close()

	var chunks []*KnowledgeChunk
	for rows.Next() {
		var chunkID, itemID, chunkText, embeddingJSON string
		var chunkIndex int

		if err := rows.Scan(&chunkID, &itemID, &chunkIndex, &chunkText, &embeddingJSON); err != nil {
			r.logger.Warn("failed to scan chunk", zap.Error(err))
			continue
		}

		// parse vector (optional, not needed here)
		var embedding []float32
		if embeddingJSON != "" {
			json.Unmarshal([]byte(embeddingJSON), &embedding)
		}

		chunk := &KnowledgeChunk{
			ID:         chunkID,
			ItemID:     itemID,
			ChunkIndex: chunkIndex,
			ChunkText:  chunkText,
			Embedding:  embedding,
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// findRelatedChunks finds other chunks related to the given chunk.
// Strategy: only return 2 adjacent chunks above and below (at most 4 total).
// Excludes already-processed chunks to avoid duplicates.
func (r *Retriever) findRelatedChunks(targetChunk *KnowledgeChunk, allChunks []*KnowledgeChunk, processedChunkIDs map[string]bool) []*KnowledgeChunk {
	related := make([]*KnowledgeChunk, 0)

	// find 2 adjacent chunks above and below
	for _, chunk := range allChunks {
		if chunk.ID == targetChunk.ID {
			continue
		}

		// check if it has already been processed (may already be in retrieval results)
		if processedChunkIDs[chunk.ID] {
			continue
		}

		// check if it is an adjacent chunk (index differs by no more than 2, and not 0)
		indexDiff := chunk.ChunkIndex - targetChunk.ChunkIndex
		if indexDiff >= -2 && indexDiff <= 2 && indexDiff != 0 {
			related = append(related, chunk)
		}
	}

	// sort by index distance, prefer the nearest
	sort.Slice(related, func(i, j int) bool {
		diffI := abs(related[i].ChunkIndex - targetChunk.ChunkIndex)
		diffJ := abs(related[j].ChunkIndex - targetChunk.ChunkIndex)
		return diffI < diffJ
	})

	// limit to at most 4 (2 above and 2 below)
	if len(related) > 4 {
		related = related[:4]
	}

	return related
}

// abs returns the absolute value of an integer
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
