package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

type memorySearchCandidate struct {
	memory  RecalledMemory
	score   float64
	matched bool
}

func validMemoryVector(vector []float64) bool {
	if len(vector) == 0 || len(vector) > 8192 {
		return false
	}
	norm := 0.0
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		norm += value * value
	}
	return norm > 0 && !math.IsInf(norm, 0)
}

// Personal memory is opt-in separately from knowledge retrieval. Reuse only
// its explicitly configured endpoint, with one bounded embedding request.
func (s *MemoryGroupStore) semanticMemoryScores(ctx context.Context, scope, query string, candidates []memorySearchCandidate) map[string]float64 {
	scores := map[string]float64{}
	a := s.runtime
	if query == "" || len(candidates) == 0 || a.configStore == nil {
		return scores
	}
	memoryPolicy := a.memoryPolicy(ctx)
	if !memoryPolicy.Enabled || !memoryPolicy.SemanticRecallEnabled {
		return scores
	}
	policy := a.configStore.retrievalPolicy()
	if !policy.Enabled || policy.Mode == "keyword" || policy.VectorAlgorithm != "remote_embedding" || policy.EmbeddingEndpoint == "" {
		return scores
	}
	endpoint, err := a.semanticEndpoint(policy.EmbeddingEndpoint, "embedding")
	if err != nil {
		return scores
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	// A bounded candidate window keeps a cold cache from embedding the user's
	// entire history on one message. Exact keyword matches remain independent.
	selected := append([]memorySearchCandidate(nil), candidates...)
	sort.SliceStable(selected, func(i, j int) bool {
		left, _ := memoryRelevanceScore(selected[i].memory, "", s.now().UTC())
		right, _ := memoryRelevanceScore(selected[j].memory, "", s.now().UTC())
		return math.Max(left, selected[i].score) > math.Max(right, selected[j].score)
	})
	if len(selected) > 32 {
		selected = selected[:32]
	}
	scopeDigest := s.digest("scope", scope)
	endpointDigest := s.digest("memory-embedding-endpoint", endpoint.ID+"\x00"+endpoint.Model+"\x00"+endpoint.APIBase)
	vectors := map[string][]float64{}
	missing := []RecalledMemory{}
	inputs := []string{truncateRunes(query, 1000)}
	for _, candidate := range selected {
		memory := candidate.memory
		if containsSensitiveMemory(memory.UntrustedContent) {
			continue
		}
		contentDigest := s.digestBytes("memory", scopeDigest, []byte(memory.UntrustedContent))
		var storedDigest, ciphertext []byte
		err := a.db.QueryRowContext(ctx, `SELECT content_digest,vector_cipher FROM agent_memory_embeddings
			WHERE memory_id=? AND endpoint_digest=?`, memory.ID, endpointDigest).Scan(&storedDigest, &ciphertext)
		if err == nil && subtle.ConstantTimeCompare(contentDigest, storedDigest) == 1 {
			plain, decryptErr := a.decrypt(ciphertext)
			var vector []float64
			if decryptErr == nil && json.Unmarshal(plain, &vector) == nil && validMemoryVector(vector) {
				vectors[memory.ID] = vector
				continue
			}
		}
		missing = append(missing, memory)
		inputs = append(inputs, truncateRunes(memory.UntrustedContent, 1000))
	}
	if len(vectors) == 0 && len(missing) == 0 || containsSensitiveMemory(query) {
		return scores
	}
	batch, err := a.remoteEmbeddings(ctx, endpoint, inputs)
	if err != nil || len(batch) != len(inputs) {
		return scores
	}
	for _, vector := range batch {
		if !validMemoryVector(vector) {
			return scores
		}
	}
	for index, memory := range missing {
		vector := batch[index+1]
		encoded, marshalErr := json.Marshal(vector)
		if marshalErr != nil {
			continue
		}
		cipher, encryptErr := a.encrypt(encoded)
		if encryptErr != nil {
			continue
		}
		contentDigest := s.digestBytes("memory", scopeDigest, []byte(memory.UntrustedContent))
		// INSERT SELECT prevents a concurrent forget/update from repopulating
		// cached personal data after its source record is gone or changed.
		_, _ = a.db.ExecContext(ctx, `INSERT INTO agent_memory_embeddings
			(memory_id,endpoint_digest,content_digest,vector_cipher,updated_at)
			SELECT id,?,?,?,? FROM agent_memories WHERE id=? AND scope_digest=? AND content_digest=?
			AND (expires_at IS NULL OR expires_at>?)
			ON CONFLICT(memory_id,endpoint_digest) DO UPDATE SET content_digest=excluded.content_digest,
			vector_cipher=excluded.vector_cipher,updated_at=excluded.updated_at`, endpointDigest, contentDigest, cipher,
			formatStoreTime(s.now().UTC()), memory.ID, scopeDigest, contentDigest, formatStoreTime(s.now().UTC()))
		vectors[memory.ID] = vector
	}
	for _, candidate := range selected {
		vector := vectors[candidate.memory.ID]
		if len(vector) != len(batch[0]) {
			continue
		}
		similarity := vectorCosine(batch[0], vector)
		if similarity < math.Max(0.65, policy.MinimumSimilarity) {
			continue
		}
		base, _ := memoryRelevanceScore(candidate.memory, "", s.now().UTC())
		scores[candidate.memory.ID] = base + similarity*0.75
	}
	return scores
}

func (s *MemoryGroupStore) memoryStillCurrent(ctx context.Context, scope string, memory RecalledMemory) bool {
	var exists int
	scopeDigest := s.digest("scope", scope)
	digest := s.digestBytes("memory", scopeDigest, []byte(memory.UntrustedContent))
	err := s.runtime.db.QueryRowContext(ctx, `SELECT 1 FROM agent_memories
		WHERE id=? AND scope_digest=? AND content_digest=? AND (expires_at IS NULL OR expires_at>?)`,
		strings.TrimSpace(memory.ID), scopeDigest, digest, formatStoreTime(s.now().UTC())).Scan(&exists)
	return err == nil && exists == 1
}
