package model

import "testing"

func TestNormalizeTargetBreakerSettingsDefaultsAndRanges(t *testing.T) {
	s := AdvancedSettings{}
	NormalizeTargetBreakerSettings(&s)
	if s.TargetBreakerThreshold != DefaultTargetBreakerThreshold || s.TargetBreakerWindowSeconds != DefaultTargetBreakerWindowSeconds || s.StreamStallTimeoutSeconds != DefaultStreamStallTimeoutSeconds {
		t.Fatalf("defaults=%+v", s)
	}
	s.TargetBreakerThreshold, s.TargetBreakerWindowSeconds = 50, 3600
	NormalizeTargetBreakerSettings(&s)
	if s.TargetBreakerThreshold != 50 || s.TargetBreakerWindowSeconds != 3600 {
		t.Fatalf("valid limits=%+v", s)
	}
	s.TargetBreakerThreshold, s.TargetBreakerWindowSeconds = 51, 29
	NormalizeTargetBreakerSettings(&s)
	if s.TargetBreakerThreshold != DefaultTargetBreakerThreshold || s.TargetBreakerWindowSeconds != DefaultTargetBreakerWindowSeconds {
		t.Fatalf("invalid limits=%+v", s)
	}
	for _, tt := range []struct {
		name string
		in   int
		want int
	}{
		{name: "disabled", in: -1, want: -1},
		{name: "minimum clamp", in: 1, want: 30},
		{name: "valid", in: 60, want: 60},
		{name: "zero default", in: 0, want: DefaultStreamStallTimeoutSeconds},
		{name: "too large default", in: 3601, want: DefaultStreamStallTimeoutSeconds},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeStreamStallTimeoutSeconds(tt.in); got != tt.want {
				t.Fatalf("NormalizeStreamStallTimeoutSeconds(%d)=%d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
