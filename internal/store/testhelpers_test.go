package store

import "autoapi/internal/model"

// targetToInput converts an output ModelRuleTarget back into an input payload.
// Tests use it when they need to round-trip an existing rule through
// UpdateModelRule.
func targetToInput(t model.ModelRuleTarget) model.ModelRuleTargetInput {
	tier := t.Tier
	return model.ModelRuleTargetInput{
		ID:                       t.ID,
		ProviderID:               t.ProviderID,
		ModelName:                t.ModelName,
		MaxRetries:               t.MaxRetries,
		Tier:                     &tier,
		FirstTokenTimeoutSeconds: t.FirstTokenTimeoutSeconds,
		Enabled:                  t.Enabled,
	}
}

func targetsToInput(targets []model.ModelRuleTarget) []model.ModelRuleTargetInput {
	out := make([]model.ModelRuleTargetInput, len(targets))
	for i, t := range targets {
		out[i] = targetToInput(t)
	}
	return out
}

// intPtr is a test helper for explicit tier values.
func intPtr(i int) *int { return &i }
