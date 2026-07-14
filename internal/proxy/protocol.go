package proxy

import "net/http"

type Protocol string

const (
	ProtocolOpenAI          Protocol = "openai"
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolUnknown         Protocol = ""
)

type ConversionMode string

const (
	ConversionModeNative ConversionMode = "native"
)

// AttemptPreparation carries per-attempt protocol decisions that replace
// the current hardcoded "use r.URL.Path and rewriteBodyModel" logic.
type AttemptPreparation struct {
	Body             []byte
	Path             string
	ExtraHeaders     http.Header
	InboundProtocol  Protocol
	UpstreamProtocol Protocol
	// ConversionMode: ConversionModeNative for same-protocol passthrough.
	ConversionMode ConversionMode
}
