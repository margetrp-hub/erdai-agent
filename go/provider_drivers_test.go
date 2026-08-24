package main

import "testing"

func TestProviderDriverCatalogCoversConfiguredProtocols(t *testing.T) {
	values := providerDriverCatalog()
	seen := map[string]bool{}
	for _, value := range values {
		if value.ID == "" || value.Label == "" || value.ProbePath == "" {
			t.Fatalf("invalid provider driver: %+v", value)
		}
		if seen[value.ID] {
			t.Fatalf("duplicate provider driver: %s", value.ID)
		}
		seen[value.ID] = true
	}
	for _, id := range []string{"openai_chat_completion", "openai_chat_rerank", "openai_compatible", "openai_embeddings", "cohere_rerank", "xai_responses"} {
		if !seen[id] {
			t.Fatalf("missing provider driver: %s", id)
		}
	}
}
