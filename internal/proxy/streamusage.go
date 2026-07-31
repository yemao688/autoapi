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
	Code     string             `json:"code"`
	Error    *streamUsageError  `json:"error"`
	Response *struct {
		Usage             *streamUsageFields `json:"usage"`
		Error             *streamUsageError  `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Message json.RawMessage `json:"message"`
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
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type streamUsageError struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Status  string          `json:"status"`
}

type geminiStreamCandidate struct {
	Candidates []struct {
		Content struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *streamUsageFields `json:"usageMetadata"`
}

// streamUsageAccumulator is a streaming SSE usage parser. Feed it raw bytes
// in arrival order; the accumulator holds onto the last-seen value for each
// usage field across all parsed `data: {...}` lines and reports them via
// Usage(). Done() returns true once a [DONE] marker or a Responses terminal
// event is observed. response.incomplete is terminal and counts as a
// successful completion (for example, max_output_tokens); response.failed
// remains terminal but unsuccessful.
type streamUsageAccumulator struct {
	// buf holds any partial line carried over from the previous Feed call
	// (i.e. a line that arrived without a trailing newline yet).
	buf []byte
	// Last-seen usage values. Zero means "not yet observed". Per-field
	// overwrite so a non-zero value in a later chunk replaces an earlier
	// one — this is correct for OpenAI (single final usage event) and
	// Anthropic (cumulative usage that may be re-emitted as the model
	// generates more output).
	input          int
	output         int
	cacheHit       int64
	cacheCreation  int64
	seenDone       bool
	terminal       string
	lastEvent      string
	errored        bool
	errorMessage   string
	producedOutput bool
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
	if bytes.HasPrefix(line, []byte("event:")) {
		a.lastEvent = string(bytes.TrimSpace(line[len("event:"):]))
		if a.lastEvent == "message_stop" {
			a.terminal = "message_stop"
			a.seenDone = true
		}
		return
	}
	// We only care about data: lines; ignore id:, retry:, etc.
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
	if streamEventProducesOutput(data, u.Type) {
		a.producedOutput = true
	}
	// If the data looks like Gemini, attach usage from Gemini's usageMetadata.
	if u.Usage == nil && u.Response == nil && len(u.Message) == 0 {
		if geminiUsage := geminiUsageFromData(data, &u); geminiUsage != nil {
			u.Usage = geminiUsage
		}
	}
	if a.lastEvent == "message_stop" || u.Type == "message_stop" {
		a.terminal = "message_stop"
		a.seenDone = true
	}
	// Error events are terminal even when they carry no usage. This must be
	// checked before the usage==nil return below: clients commonly cancel as
	// soon as they receive one of these events.
	if message, ok := streamProtocolError(&u); ok {
		a.errored = true
		a.errorMessage = message
		a.terminal = "error"
		a.seenDone = true
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
	} else if len(u.Message) > 0 {
		var message struct {
			Usage *streamUsageFields `json:"usage"`
		}
		if json.Unmarshal(u.Message, &message) == nil {
			usage = message.Usage
		}
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
	// Gemini streaming: no [DONE]; finishReason in any data event is terminal.
	if hasGeminiFinishReason(data) {
		a.terminal = "completed"
		a.seenDone = true
	}
	// A terminal error from Gemini also marks the stream done (unsuccessful).
	if u.Error != nil {
		a.terminal = "error"
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
	if usage.PromptTokenCount > 0 {
		a.input = usage.PromptTokenCount
	}
	if usage.CandidatesTokenCount > 0 {
		a.output = usage.CandidatesTokenCount
	}
	if usage.TotalTokenCount > 0 {
		if a.input == 0 || a.output == 0 {
			// totalTokenCount alone cannot be split reliably; fall
			// back to treating it as output if no prompt count seen.
			if a.input == 0 && a.output == 0 {
				a.output = usage.TotalTokenCount
			}
		}
	}
}

// ProducedOutput is deliberately independent from usage and terminal state:
// lifecycle, usage-only, and empty terminal events are not output.
func (a *streamUsageAccumulator) ProducedOutput() bool { return a.producedOutput }

func streamEventProducesOutput(data []byte, typ string) bool {
	var v struct {
		Choices []struct {
			Delta struct {
				Content          string            `json:"content"`
				Refusal          string            `json:"refusal"`
				Reasoning        string            `json:"reasoning"`
				ReasoningContent string            `json:"reasoning_content"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
				FunctionCall     json.RawMessage   `json:"function_call"`
				Audio            json.RawMessage   `json:"audio"`
			} `json:"delta"`
			Message struct {
				Content          string            `json:"content"`
				Refusal          string            `json:"refusal"`
				Reasoning        string            `json:"reasoning"`
				ReasoningContent string            `json:"reasoning_content"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
				FunctionCall     json.RawMessage   `json:"function_call"`
				Audio            json.RawMessage   `json:"audio"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Output   []json.RawMessage `json:"output"`
		Response *struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
		Delta struct {
			Text        string `json:"text"`
			Refusal     string `json:"refusal"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		ContentBlock struct {
			Text        string          `json:"text"`
			Type        string          `json:"type"`
			InputSchema json.RawMessage `json:"input"`
		} `json:"content_block"`
		Content    []json.RawMessage `json:"content"`
		Candidates []struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		// Responses uses a string delta for output_text/refusal and an
		// object delta for function arguments. Keep this probe tolerant of
		// those dialect-specific shapes.
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			return false
		}
		if typ == "response.output_text.delta" || typ == "response.refusal.delta" || typ == "response.reasoning_text.delta" || typ == "response.reasoning_summary_text.delta" {
			var s string
			return json.Unmarshal(raw["delta"], &s) == nil && s != ""
		}
		if typ == "response.function_call_arguments.delta" {
			var s string
			return json.Unmarshal(raw["delta"], &s) == nil && s != ""
		}
		return false
	}
	for _, c := range v.Choices {
		if c.Delta.Content != "" || c.Delta.Refusal != "" || c.Delta.Reasoning != "" || c.Delta.ReasoningContent != "" || len(c.Delta.ToolCalls) > 0 || rawJSONPresent(c.Delta.FunctionCall) || rawJSONPresent(c.Delta.Audio) ||
			c.Message.Content != "" || c.Message.Refusal != "" || c.Message.Reasoning != "" || c.Message.ReasoningContent != "" || len(c.Message.ToolCalls) > 0 || rawJSONPresent(c.Message.FunctionCall) || rawJSONPresent(c.Message.Audio) || c.FinishReason == "content_filter" || c.FinishReason == "refusal" {
			return true
		}
	}
	if strings.HasPrefix(typ, "response.output_") || strings.HasPrefix(typ, "response.content_part") || typ == "response.reasoning_summary_text.delta" || typ == "response.reasoning_text.delta" {
		if typ == "response.output_item.added" || typ == "response.output_item.done" {
			var raw struct {
				Item json.RawMessage `json:"item"`
			}
			return json.Unmarshal(data, &raw) == nil && len(raw.Item) > 0 && string(bytes.TrimSpace(raw.Item)) != "null"
		}
		if typ == "response.output_text.delta" || typ == "response.refusal.delta" || typ == "response.function_call_arguments.delta" {
			var raw struct {
				Delta json.RawMessage `json:"delta"`
			}
			if json.Unmarshal(data, &raw) != nil {
				return false
			}
			var s string
			return json.Unmarshal(raw.Delta, &s) == nil && s != ""
		}
		if typ == "response.reasoning_text.delta" || typ == "response.reasoning_summary_text.delta" {
			var raw struct {
				Delta json.RawMessage `json:"delta"`
			}
			if json.Unmarshal(data, &raw) == nil {
				var s string
				return json.Unmarshal(raw.Delta, &s) == nil && s != ""
			}
			return false
		}
		if typ == "response.content_part.added" {
			var raw struct {
				Part json.RawMessage `json:"part"`
			}
			return json.Unmarshal(data, &raw) == nil && len(raw.Part) > 0 && string(bytes.TrimSpace(raw.Part)) != "null"
		}
		return len(v.Content) > 0 || v.Delta.Text != "" || v.Delta.Refusal != "" || v.Delta.PartialJSON != ""
	}
	if len(v.Output) > 0 || v.Response != nil && len(v.Response.Output) > 0 {
		return true
	}
	if typ == "content_block_start" {
		// A block declaration is a structural commitment even when text is
		// initially empty; tool blocks likewise carry semantic output in the
		// block shape itself.
		return v.ContentBlock.Type != "" || v.ContentBlock.Text != "" || len(v.ContentBlock.InputSchema) > 0
	}
	if typ == "content_block_delta" {
		return v.Delta.Text != "" || v.Delta.Refusal != "" || v.Delta.PartialJSON != ""
	}
	if typ == "message_stop" && len(v.Content) > 0 {
		return true
	}
	for _, c := range v.Candidates {
		// Gemini finishReason is terminal policy metadata, but a non-empty
		// finish reason is still the provider's semantic completion signal.
		if len(c.Content.Parts) > 0 || c.FinishReason != "" {
			return true
		}
	}
	return false
}

func rawJSONPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// Usage returns the accumulated input/output/cache tokens. Returns the
// last-seen non-zero value for each field; zero means "no usage field was
// ever observed" so the caller can decide whether to fall back to
// inputEstimate.
func (a *streamUsageAccumulator) Usage() (input, output int, cacheHit, cacheCreation int64) {
	return a.input, a.output, a.cacheHit, a.cacheCreation
}

// Done reports whether the stream observed [DONE] or a Responses terminal
// event. Successful reports whether that terminal was [DONE],
// response.completed, or response.incomplete. response.failed is terminal but
// unsuccessful.
func (a *streamUsageAccumulator) Done() bool {
	return a.seenDone
}

func (a *streamUsageAccumulator) TerminalState() string { return a.terminal }
func (a *streamUsageAccumulator) Successful() bool {
	return (a.terminal == "" && a.seenDone) || a.terminal == "completed" || a.terminal == "incomplete" || a.terminal == "message_stop"
}

func (a *streamUsageAccumulator) Errored() bool { return a.errored }

func (a *streamUsageAccumulator) ErrorMessage() string { return a.errorMessage }

func streamProtocolError(u *streamUsageJSON) (string, bool) {
	if u.Error != nil {
		if u.Error.Message != "" {
			return u.Error.Message, true
		}
		if u.Error.Status != "" {
			return u.Error.Status, true
		}
		return "upstream protocol error", true
	}
	if u.Type == "error" {
		var message string
		if json.Unmarshal(u.Message, &message) == nil && message != "" {
			return message, true
		}
		return "upstream protocol error", true
	}
	if u.Response != nil {
		if u.Response.Error != nil {
			if u.Response.Error.Message != "" {
				return u.Response.Error.Message, true
			}
			if u.Response.Error.Status != "" {
				return u.Response.Error.Status, true
			}
			return "upstream protocol error", true
		}
	}
	// Responses also has a top-level error event with code/message, while
	// Anthropic and Gemini put the same object under error; the shared Error
	// field above deliberately handles all three dialects.
	return "", false
}

func isResponsesTerminal(typ string) bool {
	switch typ {
	case "response.completed", "response.failed", "response.incomplete", "completed":
		return true
	default:
		return false
	}
}

func hasGeminiFinishReason(data []byte) bool {
	if !bytes.Contains(data, []byte(`"candidates"`)) {
		return false
	}
	var g geminiStreamCandidate
	if err := json.Unmarshal(data, &g); err != nil {
		return false
	}
	for _, c := range g.Candidates {
		if c.FinishReason != "" {
			return true
		}
	}
	return false
}

func geminiUsageFromData(data []byte, u *streamUsageJSON) *streamUsageFields {
	if !bytes.Contains(data, []byte(`"usageMetadata"`)) {
		return nil
	}
	var g geminiStreamCandidate
	if err := json.Unmarshal(data, &g); err != nil {
		return nil
	}
	if g.UsageMetadata == nil {
		return nil
	}
	return g.UsageMetadata
}
