package model

type ModelRuleShadowComparison struct {
	RuleID        string                `json:"rule_id"`
	RuleName      string                `json:"rule_name"`
	Strategy      string                `json:"strategy"`
	OriginalOrder []string              `json:"original_order"`
	PlannedOrder  []string              `json:"planned_order"`
	Changed       bool                  `json:"changed"`
	Candidates    []ShadowPlanCandidate `json:"candidates"`
	Rejected      []ShadowPlanCandidate `json:"rejected"`
	Assumptions   []string              `json:"assumptions"`
}

type ShadowPlanCandidate struct {
	TargetID     string `json:"target_id"`
	Tier         int    `json:"tier"`
	Available    bool   `json:"available"`
	Reason       string `json:"reason"`
	Changed      bool   `json:"changed"`
	CircuitState string `json:"circuit_state,omitempty"`
}

// TargetShadowScore is a read-only explanation of the Phase 3A shadow score.
// EndpointAssumed makes the synthetic endpoint assumption explicit.
type TargetShadowScore struct {
	TargetID     string               `json:"target_id"`
	RuleID       string               `json:"rule_id"`
	RuleName     string               `json:"rule_name"`
	ProviderID   string               `json:"provider_id"`
	ProviderName string               `json:"provider_name"`
	ModelName    string               `json:"model_name"`
	Tier         int                  `json:"tier"`
	Metrics      TargetRuntimeSummary `json:"metrics"`
	MetricsFresh bool                 `json:"metrics_fresh"`
	// SampleBasis describes whether the score used a live route window or a
	// legacy target snapshot. A runtime sample is one real upstream attempt in
	// the exact in-memory route window (10 minutes, at most 64 samples per
	// route); it is neither a request-log row nor restored cumulative history.
	SampleBasis      string        `json:"sample_basis,omitempty"`
	RouteModes       []string      `json:"route_modes,omitempty"`
	Endpoint         string        `json:"endpoint"`
	EndpointAssumed  bool          `json:"endpoint_assumed"`
	Reliability      float64       `json:"reliability"`
	Latency          float64       `json:"latency"`
	TTFT             float64       `json:"ttft"`
	Capacity         float64       `json:"capacity"`
	CostEfficiency   float64       `json:"cost_efficiency"`
	Confidence       float64       `json:"confidence"`
	SampleCount      int64         `json:"sample_count"`
	Overall          float64       `json:"overall"`
	ExplorationBonus float64       `json:"exploration_bonus"`
	EstimatedCost    float64       `json:"estimated_cost"`
	Cost             EffectiveCost `json:"cost"`
	Availability     string        `json:"availability"`
	Reason           string        `json:"reason"`
	CircuitState     string        `json:"circuit_state,omitempty"`
}
