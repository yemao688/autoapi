package proxy

import "autoapi/internal/model"

// capabilitySnapshot is the per-request capability view used by matching.
// Capability rows are authoritative; legacy fields only fill absent rows.
type capabilitySnapshot map[string]map[Protocol]bool

func newCapabilitySnapshot(rows []model.ProviderCapability, providers map[string]*model.Provider) capabilitySnapshot {
	out := make(capabilitySnapshot)
	for id, p := range providers {
		out[id] = map[Protocol]bool{
			ProtocolOpenAIChat:        true,
			ProtocolOpenAI:            true,
			ProtocolOpenAIResponses:   p.ResponsesEnabled,
			ProtocolAnthropicMessages: p.MessagesEnabled,
			ProtocolGemini:            p.GeminiEnabled,
		}
	}
	for _, c := range rows {
		if c.Feature != "native" {
			continue
		}
		if out[c.ProviderID] == nil {
			out[c.ProviderID] = map[Protocol]bool{}
		}
		out[c.ProviderID][Protocol(c.Protocol)] = c.Enabled
	}
	return out
}

func (s capabilitySnapshot) supports(providerID string, protocol Protocol) bool {
	if p, ok := s[providerID]; ok {
		return p[protocol]
	}
	return protocol == ProtocolOpenAI || protocol == ProtocolOpenAIChat
}
