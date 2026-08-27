package main

import (
	"database/sql"
	"encoding/json"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
)

type retrievalPolicy struct {
	Enabled           bool    `json:"enabled"`
	Mode              string  `json:"mode"`
	VectorAlgorithm   string  `json:"vectorAlgorithm"`
	Dimensions        int     `json:"dimensions"`
	KeywordWeight     float64 `json:"keywordWeight"`
	VectorWeight      float64 `json:"vectorWeight"`
	MinimumSimilarity float64 `json:"minimumSimilarity"`
	TopK              int     `json:"topK"`
	CandidateK        int     `json:"candidateK"`
	ChunkSize         int     `json:"chunkSize"`
	ChunkOverlap      int     `json:"chunkOverlap"`
	EmbeddingEndpoint string  `json:"embeddingEndpointId"`
	RerankEndpoint    string  `json:"rerankEndpointId"`
}

func defaultRetrievalPolicy() retrievalPolicy {
	return retrievalPolicy{
		Enabled: true, Mode: "hybrid", VectorAlgorithm: "remote_embedding", Dimensions: 256,
		KeywordWeight: 0.45, VectorWeight: 0.55, MinimumSimilarity: 0.08, TopK: 5,
		CandidateK: 24, ChunkSize: 900, ChunkOverlap: 140,
	}
}

func (s *coreConfigStore) retrievalPolicy() retrievalPolicy {
	policy := defaultRetrievalPolicy()
	raw, err := s.integrationRaw("retrieval_policy")
	if err == nil {
		_ = json.Unmarshal(raw, &policy)
	}
	if policy.Mode != "keyword" && policy.Mode != "vector" && policy.Mode != "hybrid" {
		policy.Mode = "hybrid"
	}
	if policy.VectorAlgorithm != "local_hash" && policy.VectorAlgorithm != "remote_embedding" {
		policy.VectorAlgorithm = "remote_embedding"
	}
	if policy.Dimensions < 64 || policy.Dimensions > 2048 {
		policy.Dimensions = 256
	}
	if policy.TopK < 1 || policy.TopK > 20 {
		policy.TopK = 5
	}
	if policy.CandidateK < policy.TopK || policy.CandidateK > 100 {
		policy.CandidateK = 24
	}
	if policy.ChunkSize < 200 || policy.ChunkSize > 4000 {
		policy.ChunkSize = 900
	}
	if policy.ChunkOverlap < 0 || policy.ChunkOverlap >= policy.ChunkSize/2 {
		policy.ChunkOverlap = 140
	}
	policy.EmbeddingEndpoint = strings.TrimSpace(policy.EmbeddingEndpoint)
	policy.RerankEndpoint = strings.TrimSpace(policy.RerankEndpoint)
	if policy.KeywordWeight < 0 || policy.KeywordWeight > 1 {
		policy.KeywordWeight = 0.45
	}
	if policy.VectorWeight < 0 || policy.VectorWeight > 1 {
		policy.VectorWeight = 0.55
	}
	if policy.KeywordWeight+policy.VectorWeight == 0 {
		policy.KeywordWeight, policy.VectorWeight = 0.45, 0.55
	}
	if policy.MinimumSimilarity < 0 || policy.MinimumSimilarity > 1 {
		policy.MinimumSimilarity = 0.08
	}
	return policy
}

func localHashVector(value string, dimensions int) []float64 {
	vector := make([]float64, dimensions)
	flush := func(segment []rune) {
		if len(segment) == 0 {
			return
		}
		for size := 1; size <= 3; size++ {
			if size > len(segment) {
				break
			}
			for index := 0; index+size <= len(segment); index++ {
				token := string(segment[index : index+size])
				hash := fnv.New64a()
				_, _ = hash.Write([]byte(token))
				sum := hash.Sum64()
				weight := 1.0
				if size == 2 {
					weight = 1.4
				} else if size == 3 {
					weight = 1.8
				}
				if sum&(1<<63) != 0 {
					weight = -weight
				}
				vector[int(sum%uint64(dimensions))] += weight
			}
		}
	}
	segment := []rune{}
	for _, char := range []rune(strings.ToLower(value)) {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			segment = append(segment, char)
			continue
		}
		flush(segment)
		segment = segment[:0]
	}
	flush(segment)
	norm := 0.0
	for _, item := range vector {
		norm += item * item
	}
	if norm == 0 {
		return vector
	}
	norm = math.Sqrt(norm)
	for index := range vector {
		vector[index] /= norm
	}
	return vector
}

// vectorCosine is true cosine similarity. Remote embedding providers do not
// guarantee unit vectors; a plain dot product would clamp every un-normalized
// pair to 1.0 and silently collapse the ranking to insertion order.
func vectorCosine(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	score, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		score += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	score /= math.Sqrt(leftNorm) * math.Sqrt(rightNorm)
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func (s *coreConfigStore) ensureKnowledgeVectors(namespace string, policy retrievalPolicy) error {
	rows, err := s.db.Query(`
		SELECT d.id, d.title, d.content, d.content_hash,
			v.content_hash, v.dimensions
		FROM knowledge_documents d
		LEFT JOIN knowledge_vectors v ON v.document_id = d.id
		WHERE d.namespace = ?
	`, namespace)
	if err != nil {
		return err
	}
	type pendingVector struct{ id, title, content, hash string }
	pending := []pendingVector{}
	for rows.Next() {
		var item pendingVector
		var storedHash sql.NullString
		var storedDimensions sql.NullInt64
		if err = rows.Scan(&item.id, &item.title, &item.content, &item.hash, &storedHash, &storedDimensions); err != nil {
			rows.Close()
			return err
		}
		if !storedHash.Valid || storedHash.String != item.hash || !storedDimensions.Valid || int(storedDimensions.Int64) != policy.Dimensions {
			pending = append(pending, item)
		}
	}
	if err = rows.Close(); err != nil || len(pending) == 0 {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range pending {
		encoded, marshalErr := json.Marshal(localHashVector(item.title+"\n"+item.content, policy.Dimensions))
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec(`
			INSERT INTO knowledge_vectors (document_id, content_hash, dimensions, vector_json, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(document_id) DO UPDATE SET content_hash = excluded.content_hash,
				dimensions = excluded.dimensions, vector_json = excluded.vector_json,
				updated_at = excluded.updated_at
		`, item.id, item.hash, policy.Dimensions, string(encoded), mgmtNow()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *coreConfigStore) vectorKnowledge(namespace, query string, limit int, policy retrievalPolicy) ([]nativeRAGItem, error) {
	if err := s.ensureKnowledgeVectors(namespace, policy); err != nil {
		return nil, err
	}
	queryVector := localHashVector(query, policy.Dimensions)
	rows, err := s.db.Query(`
		SELECT d.id, d.namespace, d.title, d.source_uri, substr(d.content, 1, 240),
			v.vector_json, d.metadata_json
		FROM knowledge_vectors v JOIN knowledge_documents d ON d.id = v.document_id
		WHERE d.namespace = ? AND v.dimensions = ?
	`, namespace, policy.Dimensions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		item  nativeRAGItem
		score float64
	}
	values := []scored{}
	for rows.Next() {
		var item nativeRAGItem
		var vectorJSON, metadataJSON string
		if err = rows.Scan(&item.ID, &item.Namespace, &item.Title, &item.SourceURI, &item.Snippet, &vectorJSON, &metadataJSON); err != nil {
			return nil, err
		}
		var vector []float64
		if json.Unmarshal([]byte(vectorJSON), &vector) != nil {
			continue
		}
		score := vectorCosine(queryVector, vector)
		if score < policy.MinimumSimilarity {
			continue
		}
		item.Metadata = mgmtJSONObject(metadataJSON)
		values = append(values, scored{item: item, score: score})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(values, func(left, right int) bool { return values[left].score > values[right].score })
	if len(values) > limit {
		values = values[:limit]
	}
	items := make([]nativeRAGItem, 0, len(values))
	for _, value := range values {
		score := value.score
		value.item.Rank = &score
		items = append(items, value.item)
	}
	return items, nil
}

func (s *coreConfigStore) searchHybridKnowledge(namespace, message string, requestedLimit int) ([]nativeRAGItem, error) {
	policy := s.retrievalPolicy()
	if requestedLimit > 0 && requestedLimit < policy.TopK {
		policy.TopK = requestedLimit
	}
	if !policy.Enabled {
		policy.Mode = "keyword"
	}
	type combined struct {
		item  nativeRAGItem
		score float64
	}
	values := map[string]combined{}
	if policy.Mode != "vector" {
		queries := []string{message}
		for _, clause := range nativeKnowledgeSplitPattern.Split(message, -1) {
			clause = strings.TrimSpace(clause)
			if clause != "" {
				queries = append(queries, clause, simplifyNativeKnowledgeQuery(clause))
			}
		}
		seenQueries := map[string]struct{}{}
		for _, query := range queries {
			query = strings.TrimSpace(query)
			if query == "" {
				continue
			}
			if _, found := seenQueries[query]; found {
				continue
			}
			seenQueries[query] = struct{}{}
			hits, err := s.searchNativeKnowledgeOnce(namespace, query, policy.TopK*2)
			if err != nil {
				return nil, err
			}
			for index, hit := range hits {
				score := policy.KeywordWeight / float64(index+1)
				current := values[hit.ID]
				if current.item.ID == "" {
					current.item = hit
				}
				if score > current.score {
					current.score = score
				}
				values[hit.ID] = current
			}
		}
	}
	if policy.Mode != "keyword" {
		hits, err := s.vectorKnowledge(namespace, message, policy.TopK*2, policy)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			similarity := 0.0
			if hit.Rank != nil {
				similarity = *hit.Rank
			}
			current := values[hit.ID]
			if current.item.ID == "" {
				current.item = hit
			}
			current.score += policy.VectorWeight * similarity
			values[hit.ID] = current
		}
	}
	ranked := make([]combined, 0, len(values))
	for _, value := range values {
		ranked = append(ranked, value)
	}
	sort.SliceStable(ranked, func(left, right int) bool { return ranked[left].score > ranked[right].score })
	if len(ranked) > policy.TopK {
		ranked = ranked[:policy.TopK]
	}
	items := make([]nativeRAGItem, 0, len(ranked))
	for _, value := range ranked {
		score := value.score
		value.item.Rank = &score
		items = append(items, value.item)
	}
	return items, nil
}
