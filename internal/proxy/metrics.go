package proxy

import (
	"strings"
	"time"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/store"
)

// metricSink is deliberately small so proxy tests can inject a synchronous
// spy. Production uses the bounded, process-local Registry. Submission is
// always best effort.
type metricSink interface {
	Submit(model.TargetMetricEvent) bool
}

func (p *Proxy) submitMetric(e model.TargetMetricEvent) {
	if p.metricSink == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = p.metricSink.Submit(e)
}

func metricKey(c candidate, endpoint string) model.TargetMetricKey {
	return model.TargetMetricKey{TargetID: c.targetID, ProviderID: c.provider.ID, ModelName: c.modelName, Endpoint: endpoint}
}

func routeModeKey(c candidate) model.RouteModeKey {
	targetID := c.targetID
	if strings.TrimSpace(targetID) == "" && c.provider != nil {
		// Production model-rule targets always have a persisted ID. This
		// fallback only keeps legacy hand-built test fixtures attributable.
		targetID = c.provider.ID
	}
	upstream := string(c.protocol)
	if c.convertTo != "" {
		upstream = string(c.convertTo)
	}
	return model.RouteModeKey{TargetID: targetID, InboundProtocol: string(c.protocol), UpstreamProtocol: upstream}
}

func (p *Proxy) emitAttempt(c candidate, endpoint, requestID string, outcome model.AttemptOutcome, status int, committed bool, firstByte, ttft int64) {
	e := metrics.AttributeAttempt(metricKey(c, endpoint), routeModeKey(c), outcome, status, committed, firstByte, ttft)
	e.RequestID, e.AttemptID, e.At = requestID, store.NewUUID(), time.Now().UTC()
	p.submitMetric(e)
}

func (p *Proxy) emitRequest(logEntry *model.RequestLog, endpoint string) {
	if logEntry == nil {
		return
	}
	providerID, modelName := logEntry.ProviderID, logEntry.Model
	targetID := ""
	if len(logEntry.Chain) > 0 {
		last := logEntry.Chain[len(logEntry.Chain)-1]
		if providerID == "" {
			providerID = last.ProviderID
		}
		if modelName == "" {
			modelName = last.ModelName
		}
		targetID = last.TargetID
	}
	if providerID == "" {
		providerID = model.MetricProviderPreflight
	}
	if modelName == "" {
		modelName = model.MetricProviderClient
	}
	if logEntry.ID == "" {
		logEntry.ID = store.NewUUID()
	}
	outcome := requestOutcome(logEntry)
	e := model.TargetMetricEvent{Key: model.TargetMetricKey{TargetID: targetID, ProviderID: providerID, ModelName: modelName, Endpoint: endpoint}, Kind: model.MetricEventRequest, RequestID: logEntry.ID, RequestOutcome: outcome, StatusCode: logEntry.StatusCode, StreamCommitted: logEntry.IsStream && logEntry.StatusCode >= 200 && logEntry.StatusCode < 300, TTFTMs: int64(logEntry.FirstTokenMs), At: time.Now().UTC()}
	p.submitMetric(e)
}

func requestOutcome(log *model.RequestLog) model.RequestOutcome {
	if log == nil {
		return model.RequestOutcomeUnknown
	}
	err := strings.ToLower(log.Error)
	if log.StatusCode == statusClientClosed || strings.Contains(err, "client_abort") || strings.Contains(err, "context canceled") || strings.Contains(err, "client disconnected") {
		return model.RequestOutcomeAborted
	}
	if log.IsStream && log.StatusCode >= 200 && log.StatusCode < 300 {
		if log.Error != "" {
			return model.RequestOutcomePartial
		}
		for _, c := range log.Chain {
			if c.Status == "truncated" || c.Status == "downstream_error" {
				return model.RequestOutcomePartial
			}
		}
	}
	if log.StatusCode >= 200 && log.StatusCode < 300 {
		return model.RequestOutcomeSuccess
	}
	return model.RequestOutcomeFailure
}
