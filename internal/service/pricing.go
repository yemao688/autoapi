package service

import (
	"math"
	"time"

	"autoapi/internal/model"
)

// EstimateEffectiveCost returns a routing-time cost estimate for a single
// upstream attempt using the resolved price. It never treats an unknown price as
// free; when the price is nil, invalid, expired, or not estimable, the returned
// EffectiveCost has Available=false and Cost=0.
//
// Retry cost is an estimate only: for token billing the base token cost is
// multiplied by (1 + additionalRetryCount), and for request billing each retry
// is counted as an additional request. Total attempts = baseRequestCount +
// additionalRetryCount. The result is not written back into any log.
func EstimateEffectiveCost(price *model.Price, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, baseRequestCount, additionalRetryCount int) model.EffectiveCost {
	return EstimateEffectiveCostAt(price, time.Now().UnixMilli(), inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, baseRequestCount, additionalRetryCount)
}

// EstimateEffectiveCostAt is the deterministic form used for historical
// replay. All validity/expiry checks use timestampMs; callers must supply the
// event's timestamp rather than silently substituting current time.
func EstimateEffectiveCostAt(price *model.Price, timestampMs int64, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, baseRequestCount, additionalRetryCount int) model.EffectiveCost {
	if inputTokens < 0 || outputTokens < 0 || cacheReadTokens < 0 || cacheWriteTokens < 0 ||
		baseRequestCount < 0 || additionalRetryCount < 0 {
		return unavailableCost(inputTokens, outputTokens, baseRequestCount, additionalRetryCount)
	}
	out := unavailableCost(inputTokens, outputTokens, baseRequestCount, additionalRetryCount)
	if price == nil || !price.IsValid() || !price.IsEffectiveAt(timestampMs) {
		return out
	}
	out.Currency = price.Currency
	out.PriceVersion = price.Version

	var cost float64
	confidence := price.Confidence
	switch price.BillingMode {
	case model.BillingModeToken:
		cost = tokenCost(price, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
		if !validCost(cost) {
			return out
		}
		cost *= float64(1 + additionalRetryCount)
	case model.BillingModeRequest:
		if !validCost(price.RequestPricePerRequest) {
			return out
		}
		cost = price.RequestPricePerRequest * float64(baseRequestCount+additionalRetryCount)
	case model.BillingModeQuota, model.BillingModeCustom:
		return out
	default:
		return out
	}
	if !validCost(cost) {
		return out
	}
	out.Cost = cost
	if additionalRetryCount > 0 && confidence == model.CostConfidenceExact {
		confidence = model.CostConfidenceEstimated
	}
	out.Confidence = confidence
	out.Available = true
	return out
}

func unavailableCost(inputTokens, outputTokens, baseRequestCount, additionalRetryCount int) model.EffectiveCost {
	return model.EffectiveCost{
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		BaseRequestCount:     baseRequestCount,
		AdditionalRetryCount: additionalRetryCount,
		Cost:                 0,
		Currency:             "USD",
		Confidence:           model.CostConfidenceUnavailable,
		Available:            false,
	}
}

func tokenCost(price *model.Price, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	if !validCost(price.InputPricePerMillion) || !validCost(price.OutputPricePerMillion) ||
		!validCost(price.CacheReadPricePerMillion) || !validCost(price.CacheWritePricePerMillion) {
		return math.NaN()
	}
	return float64(inputTokens)*price.InputPricePerMillion/1e6 +
		float64(outputTokens)*price.OutputPricePerMillion/1e6 +
		float64(cacheReadTokens)*price.CacheReadPricePerMillion/1e6 +
		float64(cacheWriteTokens)*price.CacheWritePricePerMillion/1e6
}

func validCost(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}
