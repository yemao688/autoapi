package proxy

// ProtocolAdapter prepares an upstream attempt for a given candidate.
// Phase 2 extension point: adapters may translate between inbound and upstream protocols.
type ProtocolAdapter interface {
	PrepareAttempt(body []byte, candidate candidate) (AttemptPreparation, error)
}

// nativeAdapter handles same-protocol passthrough: rewrite model only.
type nativeAdapter struct{}

func (nativeAdapter) PrepareAttempt(body []byte, c candidate) (AttemptPreparation, error) {
	rewrittenBody, err := rewriteBodyModel(body, c.modelName)
	if err != nil {
		return AttemptPreparation{}, err
	}
	prep := AttemptPreparation{
		Body:             rewrittenBody,
		Path:             c.upstreamPath,
		InboundProtocol:  c.protocol,
		UpstreamProtocol: c.protocol,
		ConversionMode:   ConversionModeNative,
	}
	if c.protocol == ProtocolAnthropicMessages {
		prep.SuppressBearerAuth = true
	}
	return prep, nil
}
