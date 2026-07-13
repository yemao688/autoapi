package model

// ReplayResult is a deterministic, shadow-only reconstruction of one request.
type ReplayResult struct {
	LogID           string               `json:"log_id"`
	Timestamp       int64                `json:"timestamp"`
	RuleID          string               `json:"rule_id"`
	RuleName        string               `json:"rule_name"`
	RequestOutcome  string               `json:"request_outcome"`
	SelectedTarget  string               `json:"selected_target"`
	Endpoint        string               `json:"endpoint"`
	EndpointAssumed bool                 `json:"endpoint_assumed"`
	Attempts        []ReplayAttemptScore `json:"attempts"`
	Warnings        []string             `json:"warnings,omitempty"`
}

type ReplayAttemptScore struct {
	Attempt          RequestLogChainEntry `json:"attempt"`
	TargetID         string               `json:"target_id"`
	ProviderID       string               `json:"provider_id"`
	ModelName        string               `json:"model_name"`
	TargetMissing    bool                 `json:"target_missing"`
	ProviderMissing  bool                 `json:"provider_missing"`
	Score            TargetShadowScore    `json:"score"`
	ReplayLimitation string               `json:"replay_limitation,omitempty"`
}
