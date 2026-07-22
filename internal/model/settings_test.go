package model

import "testing"

func TestNormalizeTargetBreakerSettingsDefaultsAndRanges(t *testing.T) {
	s := AdvancedSettings{}
	NormalizeTargetBreakerSettings(&s)
	if s.TargetBreakerThreshold != DefaultTargetBreakerThreshold || s.TargetBreakerWindowSeconds != DefaultTargetBreakerWindowSeconds {
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
}
