package proxy

import "testing"

func TestEndpointForCandidateReturnsUpstreamPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "chat", path: "/v1/chat/completions", want: "/v1/chat/completions"},
		{name: "responses", path: "/v1/responses", want: "/v1/responses"},
		{name: "default", want: "/v1/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endpointForCandidate(candidate{upstreamPath: tt.path})
			if got != tt.want {
				t.Fatalf("endpointForCandidate() = %q, want %q", got, tt.want)
			}
		})
	}
}
