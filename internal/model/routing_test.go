package model

import (
	"math"
	"testing"
)

func TestTargetIdentity(t *testing.T) {
	cases := []struct {
		id         TargetIdentity
		valid      bool
		normalized string
	}{
		{TargetIdentity{TargetID: "t1"}, true, "t1"},
		{TargetIdentity{TargetID: "  t2  "}, true, "t2"},
		{TargetIdentity{TargetID: ""}, false, ""},
		{TargetIdentity{TargetID: "   "}, false, ""},
	}
	for _, tc := range cases {
		if got := tc.id.Valid(); got != tc.valid {
			t.Errorf("Valid(%q) = %v, want %v", tc.id.TargetID, got, tc.valid)
		}
		if got := tc.id.Normalized().TargetID; got != tc.normalized {
			t.Errorf("Normalized(%q) = %q, want %q", tc.id.TargetID, got, tc.normalized)
		}
	}
}

func TestTargetMetricKey(t *testing.T) {
	cases := []struct {
		key        TargetMetricKey
		valid      bool
		normalized TargetMetricKey
	}{
		{
			key:        TargetMetricKey{ProviderID: "p", ModelName: "m", Endpoint: "/v1/chat/completions"},
			valid:      true,
			normalized: TargetMetricKey{ProviderID: "p", ModelName: "m", Endpoint: "/v1/chat/completions"},
		},
		{
			key:        TargetMetricKey{ProviderID: "  p  ", ModelName: "  m  ", Endpoint: "  /ep  "},
			valid:      true,
			normalized: TargetMetricKey{ProviderID: "p", ModelName: "m", Endpoint: "/ep"},
		},
		{
			key:        TargetMetricKey{ProviderID: "", ModelName: "m"},
			valid:      false,
			normalized: TargetMetricKey{ProviderID: "", ModelName: "m"},
		},
		{
			key:        TargetMetricKey{ProviderID: "p", ModelName: ""},
			valid:      false,
			normalized: TargetMetricKey{ProviderID: "p", ModelName: ""},
		},
	}
	for _, tc := range cases {
		if got := tc.key.Valid(); got != tc.valid {
			t.Errorf("Valid(%+v) = %v, want %v", tc.key, got, tc.valid)
		}
		if got := tc.key.Normalized(); got != tc.normalized {
			t.Errorf("Normalized(%+v) = %+v, want %+v", tc.key, got, tc.normalized)
		}
	}
}

func TestRequestMetricKeyAllowsPreflightButAttemptRequiresProvider(t *testing.T) {
	request := TargetMetricEvent{Key: TargetMetricKey{ProviderID: MetricProviderPreflight, ModelName: MetricProviderClient, Endpoint: "/v1/chat/completions"}, Kind: MetricEventRequest, RequestOutcome: RequestOutcomeFailure}
	if !request.Valid() {
		t.Fatal("preflight request event should be valid")
	}
	attempt := TargetMetricEvent{Key: TargetMetricKey{ProviderID: MetricProviderPreflight, ModelName: MetricProviderClient, Endpoint: "/v1/chat/completions"}, Kind: MetricEventAttempt, AttemptOutcome: AttemptOutcomeUnknown}
	if attempt.Valid() {
		t.Fatal("preflight key must not be valid for an upstream attempt")
	}
}

func TestAttemptOutcome(t *testing.T) {
	for _, o := range []AttemptOutcome{
		AttemptOutcomeSuccess, AttemptOutcomeRetryable, AttemptOutcomeNonRetryable,
		AttemptOutcomeCircuitOpen, AttemptOutcomePreflightError, AttemptOutcomeClientAbort,
		AttemptOutcomeTruncated, AttemptOutcomeDownstreamError, AttemptOutcomeUnknown,
		OutcomeSuccess, OutcomeTruncated, OutcomeDownstreamError, OutcomeClientAbort,
	} {
		if !o.Valid() {
			t.Errorf("%q should be valid", o)
		}
	}
	if AttemptOutcome("bogus").Valid() {
		t.Error("bogus attempt outcome should be invalid")
	}
	if got := AttemptOutcome("bogus").Normalized(); got != AttemptOutcomeUnknown {
		t.Errorf("Normalized(bogus) = %q, want %q", got, AttemptOutcomeUnknown)
	}
}

// TestAttemptOutcomeJSONValues ensures the string values used by the proxy and
// stored in request_log chain entries stay unchanged. The values are the
// contract with the frontend and any downstream log consumers.
func TestAttemptOutcomeJSONValues(t *testing.T) {
	want := map[AttemptOutcome]string{
		AttemptOutcomeSuccess:         "success",
		AttemptOutcomeRetryable:       "retryable",
		AttemptOutcomeNonRetryable:    "non_retryable",
		AttemptOutcomeCircuitOpen:     "circuit_open",
		AttemptOutcomePreflightError:  "preflight_error",
		AttemptOutcomeClientAbort:     "client_abort",
		AttemptOutcomeTruncated:       "truncated",
		AttemptOutcomeDownstreamError: "downstream_error",
		AttemptOutcomeUnknown:         "unknown",
	}
	for o, expected := range want {
		if string(o) != expected {
			t.Errorf("%q string value = %q, want %q", o, string(o), expected)
		}
	}
}

func TestRequestOutcome(t *testing.T) {
	for _, o := range []RequestOutcome{
		RequestOutcomeSuccess, RequestOutcomePartial, RequestOutcomeFailure,
		RequestOutcomeAborted, RequestOutcomeUnknown,
	} {
		if !o.Valid() {
			t.Errorf("%q should be valid", o)
		}
	}
	if RequestOutcome("bogus").Valid() {
		t.Error("bogus request outcome should be invalid")
	}
	if got := RequestOutcome("bogus").Normalized(); got != RequestOutcomeUnknown {
		t.Errorf("Normalized(bogus) = %q, want %q", got, RequestOutcomeUnknown)
	}
}

func TestBillingMode(t *testing.T) {
	for _, m := range []BillingMode{
		BillingModeToken, BillingModeRequest, BillingModeQuota, BillingModeCustom, BillingModeUnknown,
	} {
		if !m.Valid() {
			t.Errorf("%q should be valid", m)
		}
	}
	if BillingMode("bogus").Valid() {
		t.Error("bogus billing mode should be invalid")
	}
	if got := BillingMode("bogus").Normalized(); got != BillingModeUnknown {
		t.Errorf("Normalized(bogus) = %q, want %q", got, BillingModeUnknown)
	}
}

func TestCostConfidence(t *testing.T) {
	for _, c := range []CostConfidence{
		CostConfidenceExact, CostConfidenceEstimated, CostConfidencePartial,
		CostConfidenceUnknown, CostConfidenceUnavailable,
	} {
		if !c.Valid() {
			t.Errorf("%q should be valid", c)
		}
	}
	if CostConfidence("bogus").Valid() {
		t.Error("bogus cost confidence should be invalid")
	}
	if got := CostConfidence("bogus").Normalized(); got != CostConfidenceUnknown {
		t.Errorf("Normalized(bogus) = %q, want %q", got, CostConfidenceUnknown)
	}
}

func TestEffectiveCost(t *testing.T) {
	defaultCost := DefaultEffectiveCost()
	if defaultCost.IsAvailable() {
		t.Error("default effective cost should not be available")
	}
	if defaultCost.Cost != 0 {
		t.Errorf("default cost should be zero, got %v", defaultCost.Cost)
	}
	if defaultCost.Confidence != CostConfidenceUnavailable {
		t.Errorf("default confidence = %q, want %q", defaultCost.Confidence, CostConfidenceUnavailable)
	}

	available := EffectiveCost{Cost: 0.001, Currency: "USD", Confidence: CostConfidenceEstimated, Available: true}
	if !available.IsAvailable() {
		t.Error("expected available cost")
	}

	for _, bad := range []EffectiveCost{
		{Cost: math.NaN(), Available: true},
		{Cost: math.Inf(1), Available: true},
		{Cost: math.Inf(-1), Available: true},
		{Cost: -0.001, Available: true},
		{Cost: 0.001, Available: false},
	} {
		if bad.IsAvailable() {
			t.Errorf("expected unavailable for %+v", bad)
		}
	}
}
