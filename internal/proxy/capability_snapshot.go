package proxy

import "autoapi/internal/model"

// capabilitySnapshot is the per-request capability view used by matching.
// Capability rows are authoritative; legacy fields only fill absent rows.
type capabilitySnapshot struct {
	providers map[string]map[Protocol]bool
	models    map[string]map[Protocol]bool
}

func newCapabilitySnapshot(rows []model.ProviderCapability, providers map[string]*model.Provider) capabilitySnapshot {
	out := capabilitySnapshot{providers: make(map[string]map[Protocol]bool), models: make(map[string]map[Protocol]bool)}
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
		if c.Feature != "native" || c.Source == "legacy" {
			continue
		}
		if out.providers[c.ProviderID] == nil {
			out.providers[c.ProviderID] = map[Protocol]bool{}
		}
		out.providers[c.ProviderID][Protocol(c.Protocol)] = c.Enabled
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

func (s capabilitySnapshot) withModels(rows []model.ModelCapability) capabilitySnapshot {
	if s.models == nil {
		s.models = make(map[string]map[Protocol]bool)
	}
	for _, c := range rows {
		if c.Feature == "native" && c.Source != "legacy" {
			key := c.ProviderID + "\x00" + c.ModelName
			if s.models[key] == nil {
				s.models[key] = map[Protocol]bool{}
			}
			s.models[key][Protocol(c.Protocol)] = c.Enabled
		}
	}
	return s
}
