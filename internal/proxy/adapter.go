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

type conversionAdapter struct {
	from Protocol
	to   Protocol
}

func (a conversionAdapter) PrepareAttempt(body []byte, c candidate) (AttemptPreparation, error) {
	prep := AttemptPreparation{
		InboundProtocol:  a.from,
		UpstreamProtocol: a.to,
		ConversionMode:   ConversionModeConvert,
	}
	switch {
	case a.from == ProtocolAnthropicMessages && a.to == ProtocolOpenAIResponses:
		converted, err := messagesToResponsesRequest(body, c.modelName)
		if err != nil {
			return AttemptPreparation{}, err
		}
		prep.Body = converted
		prep.Path = "/v1/responses"
		prep.ConvertResponse = func(body []byte) ([]byte, error) { return responsesToMessagesResponse(body, c.ruleLabel) }
	case a.from == ProtocolOpenAIResponses && a.to == ProtocolAnthropicMessages:
		converted, err := responsesToMessagesRequest(body, c.modelName)
		if err != nil {
			return AttemptPreparation{}, err
		}
		prep.Body = converted
		prep.Path = "/v1/messages"
		prep.SuppressBearerAuth = true
		prep.ConvertResponse = func(body []byte) ([]byte, error) { return messagesToResponsesResponse(body, c.ruleLabel) }
	default:
		return AttemptPreparation{}, errUnsupportedConversion(a.from, a.to)
	}
	return prep, nil
}
