package store

import "testing"

func TestJoinProviderURL(t *testing.T) {
	cases := []struct {
		baseURL string
		path    string
		want    string
	}{
		{"https://api.openai.com", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://custom.com/api/v1", "/v1/models", "https://custom.com/api/v1/models"},
		{"http://localhost:8080", "/v1/embeddings", "http://localhost:8080/v1/embeddings"},
	}

	for _, tc := range cases {
		got := JoinProviderURL(tc.baseURL, tc.path)
		if got != tc.want {
			t.Errorf("JoinProviderURL(%q, %q) = %q, want %q", tc.baseURL, tc.path, got, tc.want)
		}
	}
}

func TestRequiresOpenCodeSession(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"console go base", "https://opencode.ai/inference/go/openai/v1", true},
		{"console go root", "https://opencode.ai/inference/go", true},
		{"zen go", "https://opencode.ai/zen/go", true},
		{"console pay as you go", "https://opencode.ai/inference/openai/v1", false},
		{"path prefix lookalike", "https://opencode.ai/inference/go-other", false},
		{"opencode root", "https://opencode.ai", false},
		{"lookalike host", "https://opencode.ai.evil.example/inference/go", false},
		{"unrelated provider", "https://api.openai.com/v1", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiresOpenCodeSession(tc.baseURL); got != tc.want {
				t.Fatalf("RequiresOpenCodeSession(%q) = %v, want %v", tc.baseURL, got, tc.want)
			}
		})
	}
}
