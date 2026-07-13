// streamusage.go parses SSE usage from a streaming response on the fly. Unlike
// parseStreamUsage (usage.go) which scans a fully-buffered body, the
// streamUsageAccumulator consumes byte chunks in order and updates an
// accumulated view of token usage as `data: {...}` lines arrive. This is the
// "true pass-through" companion: there is no buffered body to re-scan, so the
// proxy tees incoming bytes into the accumulator while forwarding them to the
// client in real time.
//
// The accumulator is dialect-agnostic: it captures both OpenAI
// (prompt_tokens, completion_tokens, prompt_tokens_details.cached_tokens) and
// Anthropic (input_tokens, output_tokens, cache_read_input_tokens,
// cache_creation_input_tokens) usage fields. "Last seen wins" per field, so
// the final OpenAI event before stream termination overrides any earlier partials.
package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// streamUsageJSON is the union of OpenAI and Anthropic usage field
// layouts. Either family may populate the struct; the accumulator
// merges whichever fields are present. Anthropic's message_start
// event wraps usage under `message`; message_delta and the final
// OpenAI event expose usage at the top level.
type streamUsageJSON struct {
	Usage    *streamUsageFields `json:"usage"`
	Type     string             `json:"type"`
	Response *struct {
		Usage *streamUsageFields `json:"usage"`
	} `json:"response"`
	Message *struct {
		Usage *streamUsageFields `json:"usage"`
	} `json:"message"`
}

// streamUsageFields is the per-event usage payload. The zero value
// of every field means "not observed"; last non-zero wins.
type streamUsageFields struct {
	PromptTokens  int   `json:"prompt_tokens"`
	InputTokens   int   `json:"input_tokens"`
	OutputTokens  int   `json:"output_tokens"`
	CompletionTok int   `json:"completion_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	PromptDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// streamUsageAccumulator is a streaming SSE usage parser. Feed it raw bytes
// in arrival order; the accumulator holds onto the last-seen value for each
// usage field across all parsed `data: {...}` lines and reports them via
// Usage(). Done() returns true once a [DONE] marker or a Responses terminal
// event is observed.
type streamUsageAccumulator struct {
	// buf holds any partial line carried over from the previous Feed call
	// (i.e. a line that arrived without a trailing newline yet).
	buf []byte
	// Last-seen usage values. Zero means "not yet observed". Per-field
	// overwrite so a non-zero value in a later chunk replaces an earlier
	// one — this is correct for OpenAI (single final usage event) and
	// Anthropic (cumulative usage that may be re-emitted as the model
	// generates more output).
	input         int
	output        int
	cacheHit      int64
	cacheCreation int64
	seenDone      bool
	terminal      string
}

// Feed processes the next chunk of SSE bytes. It extracts any complete
// `data: {...}` lines and updates the accumulated usage state. Any
// trailing partial line is retained for the next call. Once a terminal marker
// is observed, subsequent calls are no-ops.
func (a *streamUsageAccumulator) Feed(p []byte) {
	if a.seenDone || len(p) == 0 {
		return
	}
	// Append to carry-over buffer.
	if cap(a.buf) == 0 {
		a.buf = make([]byte, 0, 4096)
	}
	a.buf = append(a.buf, p...)

	for {
		nl := bytes.IndexByte(a.buf, '\n')
		if nl < 0 {
			break
		}
		// Copy the line out of a.buf BEFORE we shift the buffer,
		// because the shift may overwrite the line's underlying
		// memory (a.buf's storage is reused on the next append).
		line := append([]byte(nil), a.buf[:nl]...)
		// Drop trailing \r so CRLF line endings don't trip up the prefix check.
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		// Shift the remaining bytes to the front of the buffer.
		remaining := append([]byte(nil), a.buf[nl+1:]...)
		a.buf = a.buf[:0]
		a.buf = append(a.buf, remaining...)

		a.processLine(line)
		if a.seenDone {
			// Drop the rest of the buffer; we won't parse any more.
			a.buf = nil
			return
		}
	}
}

// processLine handles one SSE line: extract usage from `data: {...}` or
// note [DONE]. Comments (lines starting with ':') and event: lines are
// ignored.
func (a *streamUsageAccumulator) processLine(line []byte) {
	if len(line) == 0 {
		return
	}
	// SSE comments start with ':' and are explicitly ignored.
	if line[0] == ':' {
		return
	}
	// We only care about data: lines; ignore event:, id:, retry:, etc.
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(line[len("data:"):])
	if len(data) == 0 {
		return
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		a.seenDone = true
		return
	}
	var u streamUsageJSON
	if err := json.Unmarshal(data, &u); err != nil {
		return
	}
	// Anthropic message_start nests usage under "message"; OpenAI
	// and Anthropic message_delta expose usage at the top level.
	// Pick whichever is present.
	var usage *streamUsageFields
	if u.Usage != nil {
		usage = u.Usage
	} else if u.Response != nil && u.Response.Usage != nil {
		usage = u.Response.Usage
	} else if u.Message != nil && u.Message.Usage != nil {
		usage = u.Message.Usage
	}
	if usage == nil {
		// Responses streams terminate with response.completed/failed/
		// incomplete events and do not require an OpenAI [DONE] marker.
		if isResponsesTerminal(u.Type) {
			a.terminal = strings.TrimPrefix(u.Type, "response.")
			a.seenDone = true
		}
		return
	}
	if isResponsesTerminal(u.Type) {
		a.terminal = strings.TrimPrefix(u.Type, "response.")
		a.seenDone = true
	}
	// Last non-zero wins per field. OpenAI streams typically send a
	// single final usage event before [DONE], so this is just "the
	// final usage". Anthropic may re-emit cumulative usage on
	// message_delta — we want the most recent counts.
	if usage.PromptTokens > 0 {
		a.input = usage.PromptTokens
	}
	if usage.InputTokens > 0 {
		a.input = usage.InputTokens
	}
	if usage.CompletionTok > 0 {
		a.output = usage.CompletionTok
	}
	if usage.OutputTokens > 0 {
		a.output = usage.OutputTokens
	}
	if usage.CacheRead > 0 {
		a.cacheHit = usage.CacheRead
	}
	if usage.CacheCreation > 0 {
		a.cacheCreation = usage.CacheCreation
	}
	if usage.PromptDetails != nil && usage.PromptDetails.CachedTokens > 0 {
		a.cacheHit = usage.PromptDetails.CachedTokens
	}
	if usage.InputDetails != nil && usage.InputDetails.CachedTokens > 0 {
		a.cacheHit = usage.InputDetails.CachedTokens
	}
}

// Usage returns the accumulated input/output/cache tokens. Returns the
// last-seen non-zero value for each field; zero means "no usage field was
// ever observed" so the caller can decide whether to fall back to
// inputEstimate.
func (a *streamUsageAccumulator) Usage() (input, output int, cacheHit, cacheCreation int64) {
	return a.input, a.output, a.cacheHit, a.cacheCreation
}

// Done reports whether the stream observed [DONE] or a Responses terminal
// event. Successful reports whether that terminal was [DONE] or
// response.completed; response.failed and response.incomplete are terminal
// but unsuccessful.
func (a *streamUsageAccumulator) Done() bool {
	return a.seenDone
}

func (a *streamUsageAccumulator) TerminalState() string { return a.terminal }
func (a *streamUsageAccumulator) Successful() bool {
	return (a.terminal == "" && a.seenDone) || a.terminal == "completed"
}

func isResponsesTerminal(typ string) bool {
	switch typ {
	case "response.completed", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}
