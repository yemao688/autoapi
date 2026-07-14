package proxy

import "net/http"
import "net/url"

type Protocol string

const (
	ProtocolOpenAI            Protocol = "openai"
	ProtocolOpenAIChat        Protocol = "openai_chat"
	ProtocolOpenAIResponses   Protocol = "openai_responses"
	ProtocolAnthropicMessages Protocol = "anthropic_messages"
	ProtocolGemini            Protocol = "gemini"
	ProtocolUnknown           Protocol = ""
)

const AnthropicDefaultVersion = "2023-06-01"

type ConversionMode string

const (
	ConversionModeNative  ConversionMode = "native"
	ConversionModeConvert ConversionMode = "convert"
)

// AttemptPreparation carries per-attempt protocol decisions that replace
// the current hardcoded "use r.URL.Path and rewriteBodyModel" logic.
type AttemptPreparation struct {
	Body                []byte
	Path                string
	ExtraHeaders        http.Header
	UpstreamQueryParams url.Values
	InboundProtocol     Protocol
	UpstreamProtocol    Protocol
	SuppressBearerAuth  bool
	ConvertResponse     func([]byte) ([]byte, error) `json:"-"`
	NewStreamConverter  func() StreamConverter       `json:"-"`
	// ConversionMode: ConversionModeNative for same-protocol passthrough.
	ConversionMode ConversionMode
}
