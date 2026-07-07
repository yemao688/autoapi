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
