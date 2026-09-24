package main

import (
	"strings"
	"testing"
)

func TestDefaultModelSources(t *testing.T) {
	sources := defaultModelSources()
	if len(sources) != 9 {
		t.Fatalf("expected three models for three providers, got %d", len(sources))
	}
	models := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra"}
	suffixes := []string{"-astra", "", "-terra"}
	providers := []string{"provider-ai-input", "provider-pipio", "provider-krill"}
	for i, source := range sources {
		if source.ID != providers[i/3]+suffixes[i%3] || source.Model != models[i%3] {
			t.Fatalf("unexpected source/order at %d: %+v", i, source)
		}
		if !strings.Contains(source.Name, source.Model) {
			t.Fatalf("incident name must distinguish the model: %+v", source)
		}
		if i/3 == 2 && source.URL != "https://www.krill-code.com/api/public/channel-status?hours=24" {
			t.Fatalf("KRILL must use the new public source: %+v", source)
		}
	}
}
