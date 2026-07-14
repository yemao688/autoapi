package proxy

import "autoapi/internal/model"

// featureValue is a tri-state capability entry: known means an explicit row
// exists in the snapshot; enabled is its value.
type featureValue struct {
	enabled bool
	known   bool
}

// capabilitySnapshot is the per-request capability view used by matching.
// Capability rows are authoritative; legacy fields only fill absent rows.
type capabilitySnapshot struct {
	providers        map[string]map[Protocol]bool
	models           map[string]map[Protocol]bool
	providerFeatures map[string]map[string]featureValue // key = provider\x00protocol
	modelFeatures    map[string]map[string]featureValue // key = provider\x00model\x00protocol
}

func newCapabilitySnapshot(rows []model.ProviderCapability, providers map[string]*model.Provider) capabilitySnapshot {
	out := capabilitySnapshot{
		providers:        make(map[string]map[Protocol]bool),
		models:           make(map[string]map[Protocol]bool),
		providerFeatures: make(map[string]map[string]featureValue),
		modelFeatures:    make(map[string]map[string]featureValue),
	}
	for id, p := range providers {
		out.providers[id] = map[Protocol]bool{
			ProtocolOpenAIChat:        true,
			ProtocolOpenAI:            true,
			ProtocolOpenAIResponses:   p.ResponsesEnabled,
			ProtocolAnthropicMessages: p.MessagesEnabled,
			ProtocolGemini:            p.GeminiEnabled,
		}
	}
	for _, c := range rows {
		if c.Feature == "native" && c.Source != "legacy" {
			if out.providers[c.ProviderID] == nil {
				out.providers[c.ProviderID] = map[Protocol]bool{}
			}
			out.providers[c.ProviderID][Protocol(c.Protocol)] = c.Enabled
		}
		// Non-native capability rows are scoped by protocol. Legacy rows are
		// ignored as explicit overrides.
		if c.Feature == "native" || c.Source == "legacy" {
			continue
		}
		key := c.ProviderID + "\x00" + c.Protocol
		if out.providerFeatures[key] == nil {
			out.providerFeatures[key] = map[string]featureValue{}
		}
		out.providerFeatures[key][c.Feature] = featureValue{known: true, enabled: c.Enabled}
	}
	return out
}

func (s capabilitySnapshot) supports(providerID string, protocol Protocol) bool {
	if p, ok := s.providers[providerID]; ok {
		return p[protocol]
	}
	return false
}

func (s capabilitySnapshot) supportsModel(providerID, modelName string, protocol Protocol) bool {
	if m, ok := s.models[providerID+"\x00"+modelName]; ok {
		if v, exists := m[protocol]; exists {
			if _, providerKnown := s.providers[providerID]; providerKnown {
				return v
			}
			return false
		}
	}
	return s.supports(providerID, protocol)
}

// featureEnabled returns whether the provider/model supports a canonical
// feature for the given target protocol. Precedence is model explicit >
// provider explicit > default. Unknown providers are always false. Streaming
// defaults to true when unknown; all other features default to false in enforce
// mode and true in observe mode.
func (s capabilitySnapshot) featureEnabled(providerID, modelName string, protocol Protocol, feature string, enforce bool) (bool, bool) {
	if _, providerKnown := s.providers[providerID]; !providerKnown {
		return false, false
	}
	modelKey := providerID + "\x00" + modelName + "\x00" + string(protocol)
	if m, ok := s.modelFeatures[modelKey]; ok {
		if v, exists := m[feature]; exists {
			return v.enabled, true
		}
	}
	providerKey := providerID + "\x00" + string(protocol)
	if p, ok := s.providerFeatures[providerKey]; ok {
		if v, exists := p[feature]; exists {
			return v.enabled, true
		}
	}
	if feature == string(model.FeatureStreaming) {
		return true, false
	}
	if enforce {
		return false, false
	}
	return true, false
}

func (s capabilitySnapshot) withModels(rows []model.ModelCapability) capabilitySnapshot {
	if s.models == nil {
		s.models = make(map[string]map[Protocol]bool)
	}
	if s.modelFeatures == nil {
		s.modelFeatures = make(map[string]map[string]featureValue)
	}
	for _, c := range rows {
		key := c.ProviderID + "\x00" + c.ModelName
		if c.Feature == "native" && c.Source != "legacy" {
			if s.models[key] == nil {
				s.models[key] = map[Protocol]bool{}
			}
			s.models[key][Protocol(c.Protocol)] = c.Enabled
		}
		if c.Feature == "native" || c.Source == "legacy" {
			continue
		}
		featureKey := key + "\x00" + c.Protocol
		if s.modelFeatures[featureKey] == nil {
			s.modelFeatures[featureKey] = map[string]featureValue{}
		}
		s.modelFeatures[featureKey][c.Feature] = featureValue{known: true, enabled: c.Enabled}
	}
	return s
}
