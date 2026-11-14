package store

import (
	"net/url"
	"strings"
)

// JoinProviderURL combines a provider base URL with an OpenAI-compatible
// request path. It strips a trailing "/v1" from the base URL so that both
// "https://api.openai.com" and "https://api.openai.com/v1" work.
func JoinProviderURL(baseURL, path string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return baseURL + path
}

// RequiresOpenCodeSession reports whether baseURL points at an OpenCode
// gateway that requires the x-opencode-session header on inference requests.
// The Console Go plan uses /inference/go; /zen/go is the equivalent gateway
// used by OpenCode itself.
func RequiresOpenCodeSession(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Hostname(), "opencode.ai") {
		return false
	}
	path := strings.ToLower(u.Path)
	return hasPathPrefix(path, "/inference/go") || hasPathPrefix(path, "/zen/go")
}

func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
