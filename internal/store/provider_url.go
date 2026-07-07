package store

import "strings"

// JoinProviderURL combines a provider base URL with an OpenAI-compatible
// request path. It strips a trailing "/v1" from the base URL so that both
// "https://api.openai.com" and "https://api.openai.com/v1" work.
func JoinProviderURL(baseURL, path string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return baseURL + path
}
