package proxy

import (
	"net/url"
	"strings"
)

const geminiUpstreamKeyPlaceholder = "__AUTOAPI_UPSTREAM_KEY__"

// ProtocolAdapter prepares an upstream attempt for a given candidate.
// Phase 2 extension point: adapters may translate between inbound and upstream protocols.
type ProtocolAdapter interface {
	PrepareAttempt(body []byte, candidate candidate) (AttemptPreparation, error)
}

// nativeAdapter handles same-protocol passthrough: rewrite model only.
type nativeAdapter struct{}

func (nativeAdapter) PrepareAttempt(body []byte, c candidate) (AttemptPreparation, error) {
	if c.protocol == ProtocolGemini {
		return AttemptPreparation{
			Body:                body,
			Path:                rewriteGeminiPathModel(c.upstreamPath, c.modelName),
			UpstreamQueryParams: url.Values{"key": []string{geminiUpstreamKeyPlaceholder}},
			InboundProtocol:     c.protocol,
			UpstreamProtocol:    c.protocol,
			SuppressBearerAuth:  true,
			ConversionMode:      ConversionModeNative,
		}, nil
	}
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

func rewriteGeminiPathModel(path, modelName string) string {
	if modelName == "" {
		return path
	}
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return path
	}
	modelAction := strings.TrimPrefix(path, prefix)
	colon := strings.LastIndex(modelAction, ":")
	if colon < 0 || colon == len(modelAction)-1 {
		return path
	}
	action := modelAction[colon+1:]
	return prefix + url.PathEscape(modelName) + ":" + action
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
	case a.from == ProtocolOpenAIChat && a.to == ProtocolOpenAIResponses:
		converted, err := chatToResponsesRequest(body, c.modelName)
		if err != nil {
			return AttemptPreparation{}, err
		}
		prep.Body = converted
		prep.Path = "/v1/responses"
		prep.ConvertResponse = func(body []byte) ([]byte, error) { return responsesToChatResponse(body, c.ruleLabel) }
		prep.NewStreamConverter = newResponsesToChatStreamConverter
	case a.from == ProtocolOpenAIResponses && a.to == ProtocolOpenAIChat:
		converted, err := responsesToChatRequest(body, c.modelName)
		if err != nil {
			return AttemptPreparation{}, err
		}
		prep.Body = converted
		prep.Path = "/v1/chat/completions"
		prep.ConvertResponse = func(body []byte) ([]byte, error) { return chatToResponsesResponse(body, c.ruleLabel) }
		prep.NewStreamConverter = newChatToResponsesStreamConverter
	case a.from == ProtocolAnthropicMessages && a.to == ProtocolOpenAIResponses:
		converted, err := messagesToResponsesRequest(body, c.modelName)
		if err != nil {
			return AttemptPreparation{}, err
		}
		prep.Body = converted
		prep.Path = "/v1/responses"
		prep.ConvertResponse = func(body []byte) ([]byte, error) { return responsesToMessagesResponse(body, c.ruleLabel) }
		prep.NewStreamConverter = newResponsesToMessagesStreamConverter
	case a.from == ProtocolOpenAIResponses && a.to == ProtocolAnthropicMessages:
		converted, err := responsesToMessagesRequest(body, c.modelName)
		if err != nil {
			return AttemptPreparation{}, err
		}
		prep.Body = converted
		prep.Path = "/v1/messages"
		prep.SuppressBearerAuth = true
		prep.ConvertResponse = func(body []byte) ([]byte, error) { return messagesToResponsesResponse(body, c.ruleLabel) }
		// The request is Responses -> Messages, while the streamed response
		// must be converted in the opposite wire direction.
		prep.NewStreamConverter = newMessagesToResponsesStreamConverter
	default:
		return AttemptPreparation{}, errUnsupportedConversion(a.from, a.to)
	}
	return prep, nil
}
