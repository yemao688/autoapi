package proxy

import "testing"

func TestHasConversionCandidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		cs   []candidate
		want bool
	}{
		{"empty", nil, false},
		{"native only", []candidate{{}}, false},
		{"conversion only", []candidate{{convertTo: "responses"}}, true},
		{"mixed", []candidate{{}, {convertTo: "messages"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasConversionCandidate(tc.cs); got != tc.want {
				t.Fatalf("hasConversionCandidate() = %v, want %v", got, tc.want)
			}
		})
	}
}
