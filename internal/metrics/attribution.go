package metrics

import "autoapi/internal/model"

// AttributeAttempt converts existing proxy semantics into a metrics event.
// Client aborts never become provider failures; 429 is an independent signal.
// Truncation is preferred over the HTTP status because a committed response
// whose body was cut short is an upstream-truncated attempt even when the
// recorded response status is 5xx. Unknown outcomes intentionally receive no
// inferred failure class and are still accepted as diagnostic attempts.
func AttributeAttempt(key model.TargetMetricKey, routeMode model.RouteModeKey, outcome model.AttemptOutcome, status int, committed bool, firstByteMs, ttftMs int64) model.TargetMetricEvent {
	e := model.TargetMetricEvent{Key: key, RouteMode: routeMode, Kind: model.MetricEventAttempt, AttemptOutcome: outcome, StatusCode: status, StreamCommitted: committed, FirstByteMs: firstByteMs, TTFTMs: ttftMs}
	switch {
	case outcome == model.AttemptOutcomeClientAbort:
		e.FailureClass = model.MetricFailureClientAbort
	case outcome == model.AttemptOutcomeTruncated:
		e.FailureClass = model.MetricFailureUpstreamTrunc
	case status == 429:
		e.FailureClass = model.MetricFailure429
	case status >= 500 && status <= 599:
		e.FailureClass = model.MetricFailure5xx
	case outcome == model.AttemptOutcomeDownstreamError:
		e.FailureClass = model.MetricFailureDownstream
	case outcome == model.AttemptOutcomeConversionError:
		e.FailureClass = model.MetricFailureConversionLocal
	case outcome == model.AttemptOutcomeNonRetryable && status >= 400 && status < 500:
		e.FailureClass = model.MetricFailureHTTPNonRetryable
	case outcome == model.AttemptOutcomeRetryable:
		e.FailureClass = model.MetricFailureTransport
	}
	return e
}
