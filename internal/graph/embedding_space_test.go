package graph

import "testing"

func TestEmbeddingSpace_Compatible(t *testing.T) {
	tests := []struct {
		name string
		a, b EmbeddingSpace
		want bool
	}{
		{
			name: "identical spaces match",
			a:    EmbeddingSpace{Provider: "openai", Model: "text-embedding-3-small", Dims: 1536},
			b:    EmbeddingSpace{Provider: "openai", Model: "text-embedding-3-small", Dims: 1536},
			want: true,
		},
		{
			name: "different dims never match (the incident: 50-col vs 1536-provider)",
			a:    EmbeddingSpace{Provider: "static", Dims: 50},
			b:    EmbeddingSpace{Provider: "openai", Model: "text-embedding-3-small", Dims: 1536},
			want: false,
		},
		{
			name: "same dims, different provider refuses",
			a:    EmbeddingSpace{Provider: "openai", Model: "text-embedding-3-small", Dims: 1536},
			b:    EmbeddingSpace{Provider: "ollama", Model: "some-1536-model", Dims: 1536},
			want: false,
		},
		{
			name: "same dims, different model refuses",
			a:    EmbeddingSpace{Provider: "openai", Model: "text-embedding-3-small", Dims: 1536},
			b:    EmbeddingSpace{Provider: "openai", Model: "text-embedding-3-large", Dims: 1536},
			want: false,
		},
		{
			name: "empty identity on one side does not trip (legacy synthesized record)",
			a:    EmbeddingSpace{Dims: 50},
			b:    EmbeddingSpace{Provider: "static", Dims: 50},
			want: true,
		},
		{
			name: "both empty identity, same dims match",
			a:    EmbeddingSpace{Dims: 384},
			b:    EmbeddingSpace{Dims: 384},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Compatible(tt.b); got != tt.want {
				t.Errorf("Compatible() = %v, want %v", got, tt.want)
			}
			// Compatibility is symmetric.
			if got := tt.b.Compatible(tt.a); got != tt.want {
				t.Errorf("Compatible() reversed = %v, want %v", got, tt.want)
			}
		})
	}
}
