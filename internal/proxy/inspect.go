package proxy

import (
	"autoapi/internal/model"
	"encoding/json"
	"fmt"
	"strings"
)

// chatInspectResult carries the routing fields and requirements parsed from a
// single chat completion body.
type chatInspectResult struct {
	Model           string
	Task            string
	Stream          bool
	ReasoningEffort string
	Messages        []json.RawMessage
	Requirements    model.RequestRequirements
}

// responsesInspectResult carries the routing fields and requirements parsed
// from a single Responses body.
type responsesInspectResult struct {
	Model           string
	Stream          bool
	ReasoningEffort string
	Requirements    model.RequestRequirements
}

// messagesInspectResult carries the routing fields and requirements parsed
// from a single Messages body.
type messagesInspectResult struct {
	Model           string
	Stream          bool
	ReasoningEffort string
	Requirements    model.RequestRequirements
}

// geminiInspectResult carries the routing fields and requirements parsed from
// a single Gemini generateContent body.
type geminiInspectResult struct {
	Stream          bool
	ReasoningEffort string
	Requirements    model.RequestRequirements
}

// requestParseError is a 422-level request validation failure.
type requestParseError struct {
	msg   string
	cause error
}

func (e *requestParseError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.cause)
	}
	return e.msg
}

func (e *requestParseError) Unwrap() error { return e.cause }

func appendFeature(set []model.Feature, f model.Feature) []model.Feature {
	for _, x := range set {
		if x == f {
			return set
		}
	}
	return append(set, f)
}

func requireField(obj map[string]json.RawMessage, key, prefix string) error {
	if _, ok := obj[key]; !ok {
		return &requestParseError{msg: fmt.Sprintf("%s.%s is required", prefix, key)}
	}
	return nil
}

func requireObjectField(obj map[string]json.RawMessage, key, prefix string) error {
	raw, ok := obj[key]
	if !ok {
		return &requestParseError{msg: fmt.Sprintf("%s.%s is required", prefix, key)}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return &requestParseError{msg: fmt.Sprintf("%s.%s must be an object", prefix, key), cause: err}
	}
	return nil
}

// ---------- Chat Completions ----------

var knownChatTopLevel = map[string]bool{
	"model": true, "messages": true, "stream": true, "tools": true, "tool_choice": true, "metadata": true,
	"response_format": true, "temperature": true, "top_p": true, "max_tokens": true,
	"max_completion_tokens": true, "frequency_penalty": true, "presence_penalty": true,
	"n": true, "user": true, "stop": true, "seed": true, "logit_bias": true,
	"logprobs": true, "top_logprobs": true, "parallel_tool_calls": true,
	"service_tier": true, "reasoning_effort": true, "task": true,
}

var knownChatContentTypes = map[string]bool{
	"text": true, "image_url": true, "image": true, "input_audio": true, "audio": true,
	"tool_calls": true,
}

func inspectChatRequest(body []byte) (chatInspectResult, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return chatInspectResult{}, &requestParseError{msg: "Invalid JSON body", cause: err}
	}
	var res chatInspectResult
	if raw, ok := fields["model"]; ok {
		if err := json.Unmarshal(raw, &res.Model); err != nil {
			return chatInspectResult{}, &requestParseError{msg: "model must be a string", cause: err}
		}
	}
	if res.Model == "" {
		return chatInspectResult{}, &requestParseError{msg: "model is required"}
	}
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &res.Stream); err != nil {
			return chatInspectResult{}, &requestParseError{msg: "stream must be a boolean", cause: err}
		}
	}
	if raw, ok := fields["task"]; ok {
		if err := json.Unmarshal(raw, &res.Task); err != nil {
			return chatInspectResult{}, &requestParseError{msg: "task must be a string", cause: err}
		}
	}
	reqs := &model.RequestRequirements{}
	if _, hasMaxCompletion := fields["max_completion_tokens"]; hasMaxCompletion {
		if _, hasMaxTokens := fields["max_tokens"]; hasMaxTokens {
			return chatInspectResult{}, &requestParseError{msg: "max_completion_tokens and max_tokens cannot both be set"}
		}
	}

	if raw, ok := fields["tools"]; ok {
		if err := parseChatTools(raw); err != nil {
			return chatInspectResult{}, err
		}
		if hasArrayItems(raw) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
		}
		if hasNonFunctionTools(raw) {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
		if hasUnknownChatFunctionToolFields(raw) {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	if raw, ok := fields["tool_choice"]; ok && raw != nil && string(raw) != "null" && string(raw) != `""` {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	if raw, ok := fields["metadata"]; ok && raw != nil && string(raw) != "null" && string(raw) != `{}` {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	// Chat audio output/input: explicit audio object or modalities containing audio.
	if raw, ok := fields["audio"]; ok && raw != nil && string(raw) != "null" {
		reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
	}
	if raw, ok := fields["modalities"]; ok {
		var mods []string
		if err := json.Unmarshal(raw, &mods); err == nil {
			for _, m := range mods {
				if m == "audio" || m == "audio_input" {
					reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
				}
			}
		}
	}
	if raw, ok := fields["response_format"]; ok {
		structured, err := inspectChatResponseFormat(raw)
		if err != nil {
			return chatInspectResult{}, err
		}
		if structured {
			reqs.Features = appendFeature(reqs.Features, model.FeatureStructuredOutput)
		}
	}
	if raw, ok := fields["reasoning_effort"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return chatInspectResult{}, &requestParseError{msg: "reasoning_effort must be a string", cause: err}
		}
		if s != "" {
			reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
		}
		res.ReasoningEffort = s
	}
	// OpenRouter-style chat requests carry a reasoning object instead of the
	// reasoning_effort string. Extraction is logging-only here; routing treats
	// the unknown top-level key as native-only as before.
	if res.ReasoningEffort == "" {
		if raw, ok := fields["reasoning"]; ok && raw != nil && string(raw) != "null" {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err == nil {
				if effort, ok := obj["effort"]; ok {
					var s string
					if err := json.Unmarshal(effort, &s); err == nil {
						res.ReasoningEffort = s
					}
				}
			}
		}
	}
	if err := markChatNonConvertibleFields(fields, reqs); err != nil {
		return chatInspectResult{}, err
	}

	msgs, err := parseChatMessages(fields["messages"])
	if err != nil {
		return chatInspectResult{}, err
	}
	for _, m := range msgs {
		if m.Role != "system" && m.Role != "developer" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
		}
		if hasCacheControl(m.raw) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
		}
		for _, block := range m.ContentBlocks {
			if !knownChatContentTypes[block.Type] {
				reqs.NativeOnly = true
				reqs.UnknownSemantic = true
				continue
			}
			switch block.Type {
			case "image_url", "image":
				reqs.Features = appendFeature(reqs.Features, model.FeatureVision)
			case "input_audio", "audio":
				reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
			case "tool_calls":
				reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
			}
			if hasCacheControl(block.raw) {
				reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
			}
		}
	}

	if res.Stream {
		reqs.Features = appendFeature(reqs.Features, model.FeatureStreaming)
	}

	for k := range fields {
		if !knownChatTopLevel[k] {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
			break
		}
	}

	res.Requirements = *reqs
	return res, nil
}

func markChatNonConvertibleFields(fields map[string]json.RawMessage, reqs *model.RequestRequirements) error {
	mark := func(key string) {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	if raw, ok := fields["n"]; ok {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return &requestParseError{msg: "n must be an integer", cause: err}
		}
		if n != 1 {
			mark("n")
		}
	}
	if raw, ok := fields["stop"]; ok && meaningfulJSON(raw) {
		mark("stop")
	}
	if raw, ok := fields["seed"]; ok && raw != nil && string(raw) != "null" {
		mark("seed")
	}
	for _, key := range []string{"user", "service_tier", "logit_bias", "logprobs", "top_logprobs"} {
		if raw, ok := fields[key]; ok && meaningfulJSON(raw) {
			mark(key)
		}
	}
	if raw, ok := fields["task"]; ok && meaningfulJSON(raw) {
		mark("task")
	}
	for _, key := range []string{"frequency_penalty", "presence_penalty"} {
		if raw, ok := fields[key]; ok {
			var n float64
			if err := json.Unmarshal(raw, &n); err != nil {
				return &requestParseError{msg: key + " must be a number", cause: err}
			}
			if n != 0 {
				mark(key)
			}
		}
	}
	if raw, ok := fields["parallel_tool_calls"]; ok {
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return &requestParseError{msg: "parallel_tool_calls must be a boolean", cause: err}
		}
		if enabled {
			mark("parallel_tool_calls")
		}
	}
	return nil
}

func meaningfulJSON(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` || string(raw) == `false` || string(raw) == `0` || string(raw) == `[]` || string(raw) == `{}` {
		return false
	}
	return true
}

func parseChatTools(raw json.RawMessage) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "tools must be an array", cause: err}
	}
	for i, item := range arr {
		var t map[string]json.RawMessage
		if err := json.Unmarshal(item, &t); err != nil {
			return &requestParseError{msg: fmt.Sprintf("tools[%d] must be an object", i), cause: err}
		}
		typ, ok := t["type"]
		if !ok {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].type is required", i)}
		}
		var typStr string
		if err := json.Unmarshal(typ, &typStr); err != nil {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].type must be a string", i), cause: err}
		}
		if typStr != "function" {
			continue
		}
		fn, ok := t["function"]
		if !ok {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].function is required", i)}
		}
		var fdef map[string]json.RawMessage
		if err := json.Unmarshal(fn, &fdef); err != nil {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].function must be an object", i), cause: err}
		}
		name, ok := fdef["name"]
		if !ok {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].function.name is required", i)}
		}
		var nameStr string
		if err := json.Unmarshal(name, &nameStr); err != nil || nameStr == "" {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].function.name must be a non-empty string", i), cause: err}
		}
		if params, ok := fdef["parameters"]; ok && params != nil && string(params) != "null" {
			var pobj map[string]json.RawMessage
			if err := json.Unmarshal(params, &pobj); err != nil {
				return &requestParseError{msg: fmt.Sprintf("tools[%d].function.parameters must be an object", i), cause: err}
			}
		}
	}
	return nil
}

func hasNonFunctionTools(raw json.RawMessage) bool {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	for _, item := range arr {
		var t struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &t); err == nil && t.Type != "" && t.Type != "function" {
			return true
		}
	}
	return false
}

func hasUnknownChatFunctionToolFields(raw json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		var fn map[string]json.RawMessage
		if json.Unmarshal(tool["function"], &fn) != nil {
			continue
		}
		for key := range fn {
			if key != "name" && key != "description" && key != "parameters" && key != "strict" {
				return true
			}
		}
	}
	return false
}

func hasArrayItems(raw json.RawMessage) bool {
	var arr []json.RawMessage
	return json.Unmarshal(raw, &arr) == nil && len(arr) > 0
}

func hasStructuredOutput(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	if t, ok := obj["type"]; ok {
		var s string
		_ = json.Unmarshal(t, &s)
		if s == "json_object" || s == "json_schema" || s == "json" {
			return true
		}
	}
	if t, ok := obj["text"]; ok {
		var text map[string]json.RawMessage
		if err := json.Unmarshal(t, &text); err == nil {
			if f, ok := text["format"]; ok {
				var ft map[string]json.RawMessage
				if err := json.Unmarshal(f, &ft); err == nil {
					if _, hasType := ft["type"]; hasType {
						return true
					}
				}
			}
		}
	}
	return false
}

func inspectChatResponseFormat(raw json.RawMessage) (bool, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "json_object" || s == "json_schema" || s == "json" {
			return true, nil
		}
		return false, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false, &requestParseError{msg: "response_format must be a string or object", cause: err}
	}
	if t, ok := obj["type"]; ok {
		var typ string
		if err := json.Unmarshal(t, &typ); err != nil {
			return false, &requestParseError{msg: "response_format.type must be a string", cause: err}
		}
		if typ == "json_object" || typ == "json_schema" || typ == "json" {
			return true, nil
		}
	}
	if t, ok := obj["text"]; ok {
		var text map[string]json.RawMessage
		if err := json.Unmarshal(t, &text); err != nil {
			return false, &requestParseError{msg: "response_format.text must be an object", cause: err}
		}
		if f, ok := text["format"]; ok {
			var ft map[string]json.RawMessage
			if err := json.Unmarshal(f, &ft); err != nil {
				return false, &requestParseError{msg: "response_format.text.format must be an object", cause: err}
			}
			if _, hasType := ft["type"]; hasType {
				return true, nil
			}
		}
	}
	return false, nil
}

type chatMessage struct {
	Role          string
	ContentBlocks []chatContentBlock
	ToolCalls     []json.RawMessage
	raw           map[string]json.RawMessage
}

type chatContentBlock struct {
	Type string
	raw  map[string]json.RawMessage
}

func parseChatMessages(raw json.RawMessage) ([]chatMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, &requestParseError{msg: "messages must be an array", cause: err}
	}
	out := make([]chatMessage, 0, len(arr))
	for i, item := range arr {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(item, &m); err != nil {
			return nil, &requestParseError{msg: fmt.Sprintf("messages[%d] must be an object", i), cause: err}
		}
		msg := chatMessage{raw: m}
		if role, ok := m["role"]; ok {
			if err := json.Unmarshal(role, &msg.Role); err != nil {
				return nil, &requestParseError{msg: fmt.Sprintf("messages[%d].role must be a string", i), cause: err}
			}
		}
		if rawTc, ok := m["tool_calls"]; ok {
			var tcs []json.RawMessage
			if err := json.Unmarshal(rawTc, &tcs); err != nil {
				return nil, &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls must be an array", i), cause: err}
			}
			for j, tc := range tcs {
				if err := validateChatToolCall(tc, i, j); err != nil {
					return nil, err
				}
			}
			msg.ToolCalls = tcs
		}
		if content, ok := m["content"]; ok {
			blocks, err := parseChatContent(content, i)
			if err != nil {
				return nil, err
			}
			msg.ContentBlocks = blocks
		}
		out = append(out, msg)
	}
	return out, nil
}

func parseChatContent(raw json.RawMessage, msgIdx int) ([]chatContentBlock, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []chatContentBlock{{Type: "text"}}, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, &requestParseError{msg: fmt.Sprintf("messages[%d].content must be a string or array", msgIdx), cause: err}
	}
	out := make([]chatContentBlock, 0, len(arr))
	for i, item := range arr {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil {
			return nil, &requestParseError{msg: fmt.Sprintf("messages[%d].content[%d] must be an object", msgIdx, i), cause: err}
		}
		var typ string
		if t, ok := obj["type"]; ok {
			if err := json.Unmarshal(t, &typ); err != nil {
				return nil, &requestParseError{msg: fmt.Sprintf("messages[%d].content[%d].type must be a string", msgIdx, i), cause: err}
			}
		} else {
			return nil, &requestParseError{msg: fmt.Sprintf("messages[%d].content[%d].type is required", msgIdx, i)}
		}
		out = append(out, chatContentBlock{Type: typ, raw: obj})
	}
	return out, nil
}

func validateChatToolCall(raw json.RawMessage, msgIdx, callIdx int) error {
	var tc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tc); err != nil {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d] must be an object", msgIdx, callIdx), cause: err}
	}
	for _, required := range []string{"id", "type", "function"} {
		if _, ok := tc[required]; !ok {
			return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d] requires %s", msgIdx, callIdx, required)}
		}
	}
	var t string
	if err := json.Unmarshal(tc["type"], &t); err != nil || t != "function" {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].type must be 'function'", msgIdx, callIdx)}
	}
	var fdef map[string]json.RawMessage
	if err := json.Unmarshal(tc["function"], &fdef); err != nil {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].function must be an object", msgIdx, callIdx), cause: err}
	}
	name, ok := fdef["name"]
	if !ok {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].function.name is required", msgIdx, callIdx)}
	}
	var nameStr string
	if err := json.Unmarshal(name, &nameStr); err != nil || nameStr == "" {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].function.name must be a non-empty string", msgIdx, callIdx), cause: err}
	}
	args, ok := fdef["arguments"]
	if !ok {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].function.arguments is required", msgIdx, callIdx)}
	}
	var argStr string
	if err := json.Unmarshal(args, &argStr); err != nil {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].function.arguments must be a string", msgIdx, callIdx), cause: err}
	}
	var argObj interface{}
	if err := json.Unmarshal([]byte(argStr), &argObj); err != nil {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].tool_calls[%d].function.arguments must be valid JSON", msgIdx, callIdx), cause: err}
	}
	return nil
}

func hasCacheControl(raw map[string]json.RawMessage) bool {
	_, ok := raw["cache_control"]
	return ok
}

// ---------- Responses ----------

var knownResponsesTopLevel = map[string]bool{
	"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true,
	"temperature": true, "top_p": true, "max_output_tokens": true, "stream": true,
	"response_format": true, "text": true, "parallel_tool_calls": true, "truncation": true,
	"user": true, "metadata": true, "reasoning": true, "reasoning_effort": true,
}

func inspectResponsesRequest(body []byte) (responsesInspectResult, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return responsesInspectResult{}, &requestParseError{msg: "Invalid JSON body", cause: err}
	}
	var res responsesInspectResult
	if raw, ok := fields["model"]; ok {
		if err := json.Unmarshal(raw, &res.Model); err != nil {
			return responsesInspectResult{}, &requestParseError{msg: "model must be a string", cause: err}
		}
	}
	if res.Model == "" {
		return responsesInspectResult{}, &requestParseError{msg: "model is required"}
	}
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &res.Stream); err != nil {
			return responsesInspectResult{}, &requestParseError{msg: "stream must be a boolean", cause: err}
		}
	}
	reqs := &model.RequestRequirements{}

	// Stateful fields are still globally rejected for Responses.
	for _, f := range []string{"previous_response_id", "conversation", "background", "store"} {
		if raw, ok := fields[f]; ok {
			if f == "background" || f == "store" {
				var b bool
				if json.Unmarshal(raw, &b) == nil && !b {
					continue
				}
			}
			reqs.Features = appendFeature(reqs.Features, model.FeatureStateful)
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
			return responsesInspectResult{}, &requestParseError{msg: "stateful Responses fields are not supported: " + f}
		}
	}

	if raw, ok := fields["tools"]; ok {
		if err := parseResponsesTools(raw); err != nil {
			return responsesInspectResult{}, err
		}
		if hasArrayItems(raw) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
		}
		if hasNonFunctionResponsesTools(raw) {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
		if hasUnknownResponsesFunctionToolFields(raw) {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	if raw, ok := fields["tool_choice"]; ok && raw != nil && string(raw) != "null" && string(raw) != `""` {
		convertible, err := responsesToolChoiceConvertible(raw)
		if err != nil {
			return responsesInspectResult{}, err
		}
		if !convertible {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	if raw, ok := fields["parallel_tool_calls"]; ok && raw != nil && string(raw) != "null" && string(raw) != `false` {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	if raw, ok := fields["truncation"]; ok && raw != nil && string(raw) != "null" && string(raw) != `""` {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	if raw, ok := fields["metadata"]; ok && meaningfulJSON(raw) {
		if _, err := normalizeJSONObjectField(raw, "metadata"); err != nil {
			return responsesInspectResult{}, &requestParseError{msg: err.Error(), cause: err}
		}
	}
	// Responses reasoning object (distinct from reasoning_effort string).
	if raw, ok := fields["reasoning"]; ok && raw != nil && string(raw) != "null" {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return responsesInspectResult{}, &requestParseError{msg: "reasoning must be an object", cause: err}
		}
		reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
		if effort, ok := obj["effort"]; ok {
			var s string
			if err := json.Unmarshal(effort, &s); err != nil {
				return responsesInspectResult{}, &requestParseError{msg: "reasoning.effort must be a string", cause: err}
			}
			res.ReasoningEffort = s
		}
	}
	if raw, ok := fields["response_format"]; ok && raw != nil && string(raw) != "null" {
		if err := validateResponsesResponseFormat(raw); err != nil {
			return responsesInspectResult{}, err
		}
		if hasStructuredOutput(raw) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureStructuredOutput)
		}
	}
	if raw, ok := fields["text"]; ok {
		var text map[string]json.RawMessage
		if err := json.Unmarshal(raw, &text); err != nil {
			return responsesInspectResult{}, &requestParseError{msg: "text must be an object", cause: err}
		}
		if f, ok := text["format"]; ok && f != nil && string(f) != "null" {
			if err := validateResponsesResponseFormat(f); err != nil {
				return responsesInspectResult{}, err
			}
			if hasStructuredOutput(f) {
				reqs.Features = appendFeature(reqs.Features, model.FeatureStructuredOutput)
			}
		}
	}
	if raw, ok := fields["reasoning_effort"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return responsesInspectResult{}, &requestParseError{msg: "reasoning_effort must be a string", cause: err}
		}
		if s != "" {
			reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
		}
		if res.ReasoningEffort == "" {
			res.ReasoningEffort = s
		}
	}
	if raw, ok := fields["input"]; ok {
		if err := inspectResponsesInput(raw, reqs); err != nil {
			return responsesInspectResult{}, err
		}
	}

	if res.Stream {
		reqs.Features = appendFeature(reqs.Features, model.FeatureStreaming)
	}

	for k := range fields {
		if !knownResponsesTopLevel[k] {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
			break
		}
	}

	res.Requirements = *reqs
	return res, nil
}

func responsesToolChoiceConvertible(raw json.RawMessage) (bool, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "auto", nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false, &requestParseError{msg: "tool_choice must be a string or object", cause: err}
	}
	if err := rejectUnknownKeys(obj, "type"); err != nil {
		return false, &requestParseError{msg: err.Error(), cause: err}
	}
	if len(obj["type"]) == 0 {
		return false, &requestParseError{msg: "tool_choice.type is required"}
	}
	if err := json.Unmarshal(obj["type"], &s); err != nil {
		return false, &requestParseError{msg: "tool_choice.type must be a string", cause: err}
	}
	return s == "auto", nil
}

func parseResponsesTools(raw json.RawMessage) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "tools must be an array", cause: err}
	}
	for i, item := range arr {
		var t map[string]json.RawMessage
		if err := json.Unmarshal(item, &t); err != nil {
			return &requestParseError{msg: fmt.Sprintf("tools[%d] must be an object", i), cause: err}
		}
		typ, ok := t["type"]
		if !ok {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].type is required", i)}
		}
		var typStr string
		if err := json.Unmarshal(typ, &typStr); err != nil {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].type must be a string", i), cause: err}
		}
		if typStr != "function" {
			continue
		}
		name, ok := t["name"]
		if !ok {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].name is required", i)}
		}
		var nameStr string
		if err := json.Unmarshal(name, &nameStr); err != nil || nameStr == "" {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].name must be a non-empty string", i), cause: err}
		}
		if params, ok := t["parameters"]; ok && params != nil && string(params) != "null" {
			var pobj map[string]json.RawMessage
			if err := json.Unmarshal(params, &pobj); err != nil {
				return &requestParseError{msg: fmt.Sprintf("tools[%d].parameters must be an object", i), cause: err}
			}
		}
	}
	return nil
}

func validateResponsesResponseFormat(raw json.RawMessage) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "text" || s == "json_object" || s == "json_schema" || s == "json" {
			return nil
		}
		return &requestParseError{msg: "response_format has invalid value " + s}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return &requestParseError{msg: "response_format must be a string or object", cause: err}
	}
	if t, ok := obj["type"]; ok {
		var typ string
		if err := json.Unmarshal(t, &typ); err != nil {
			return &requestParseError{msg: "response_format.type must be a string", cause: err}
		}
		if typ == "text" || typ == "json_object" || typ == "json_schema" || typ == "json" {
			return nil
		}
		return &requestParseError{msg: "response_format.type has invalid value " + typ}
	}
	return nil
}

func hasNonFunctionResponsesTools(raw json.RawMessage) bool {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	for _, item := range arr {
		var t struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &t); err == nil && t.Type != "" && t.Type != "function" {
			return true
		}
	}
	return false
}

func hasUnknownResponsesFunctionToolFields(raw json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		var typ string
		if json.Unmarshal(tool["type"], &typ) != nil || typ != "function" {
			continue
		}
		for key := range tool {
			if key != "type" && key != "name" && key != "description" && key != "parameters" && key != "strict" {
				return true
			}
		}
	}
	return false
}

func inspectResponsesInput(raw json.RawMessage, reqs *model.RequestRequirements) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return &requestParseError{msg: "input must not be empty"}
		}
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "input must be a string or array", cause: err}
	}
	for i, item := range arr {
		if err := inspectResponsesInputItem(item, reqs, i); err != nil {
			return err
		}
	}
	return nil
}

func inspectResponsesInputItem(raw json.RawMessage, reqs *model.RequestRequirements, idx int) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return &requestParseError{msg: fmt.Sprintf("input[%d] must be an object", idx), cause: err}
	}
	if hasCacheControl(obj) {
		reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
	}
	typ := inferResponsesInputType(obj)
	switch typ {
	case "text":
		// free-form string or text content block
	case "image", "image_url":
		reqs.Features = appendFeature(reqs.Features, model.FeatureVision)
	case "audio":
		reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
	case "file":
		reqs.Features = appendFeature(reqs.Features, model.FeatureDocument)
	case "function_call", "function_call_output":
		reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
	case "message":
		if content, ok := obj["content"]; ok {
			if err := inspectResponsesMessageContent(content, reqs, idx); err != nil {
				return err
			}
		}
	case "":
		return &requestParseError{msg: fmt.Sprintf("input[%d].type is required", idx)}
	default:
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	return nil
}

func inferResponsesInputType(obj map[string]json.RawMessage) string {
	if t, ok := obj["type"]; ok {
		var typ string
		_ = json.Unmarshal(t, &typ)
		return typ
	}
	if _, hasRole := obj["role"]; hasRole {
		return "message"
	}
	return ""
}

var knownResponsesContentTypes = map[string]bool{
	"input_text": true, "output_text": true, "refusal": true,
	"input_image": true, "input_audio": true, "input_file": true,
}

func inspectResponsesMessageContent(raw json.RawMessage, reqs *model.RequestRequirements, idx int) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: fmt.Sprintf("input[%d].content must be a string or array", idx), cause: err}
	}
	for i, item := range arr {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(item, &block); err != nil {
			return &requestParseError{msg: fmt.Sprintf("input[%d].content[%d] must be an object", idx, i), cause: err}
		}
		var typ string
		if t, ok := block["type"]; ok {
			if err := json.Unmarshal(t, &typ); err != nil {
				return &requestParseError{msg: fmt.Sprintf("input[%d].content[%d].type must be a string", idx, i), cause: err}
			}
		} else {
			return &requestParseError{msg: fmt.Sprintf("input[%d].content[%d].type is required", idx, i)}
		}
		switch typ {
		case "input_text", "output_text", "refusal":
			// text-like blocks
		case "input_image":
			reqs.Features = appendFeature(reqs.Features, model.FeatureVision)
		case "input_audio":
			reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
		case "input_file":
			reqs.Features = appendFeature(reqs.Features, model.FeatureDocument)
		default:
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	return nil
}

// ---------- Anthropic Messages ----------

var knownMessagesTopLevel = map[string]bool{
	"model": true, "messages": true, "system": true, "tools": true, "tool_choice": true,
	"max_tokens": true, "temperature": true, "top_p": true, "stream": true,
	"metadata": true, "stop_sequences": true, "thinking": true, "reasoning_effort": true,
}

var knownMessagesContentTypes = map[string]bool{
	"text": true, "image": true, "tool_use": true, "tool_result": true,
	"document": true, "thinking": true, "redacted_thinking": true, "audio": true,
}

func inspectMessagesRequest(body []byte) (messagesInspectResult, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return messagesInspectResult{}, &requestParseError{msg: "Invalid JSON body", cause: err}
	}
	var res messagesInspectResult
	if raw, ok := fields["model"]; ok {
		if err := json.Unmarshal(raw, &res.Model); err != nil {
			return messagesInspectResult{}, &requestParseError{msg: "model must be a string", cause: err}
		}
	}
	if res.Model == "" {
		return messagesInspectResult{}, &requestParseError{msg: "model is required"}
	}
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &res.Stream); err != nil {
			return messagesInspectResult{}, &requestParseError{msg: "stream must be a boolean", cause: err}
		}
	}
	reqs := &model.RequestRequirements{}

	if raw, ok := fields["tools"]; ok {
		if err := parseMessagesTools(raw); err != nil {
			return messagesInspectResult{}, err
		}
		if hasArrayItems(raw) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
		}
		if hasNonFunctionMessagesTools(raw) {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	if raw, ok := fields["tool_choice"]; ok && raw != nil && string(raw) != "null" && string(raw) != `""` {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	if raw, ok := fields["metadata"]; ok && raw != nil && string(raw) != "null" && string(raw) != `{}` {
		reqs.NativeOnly = true
		reqs.UnknownSemantic = true
	}
	if raw, ok := fields["thinking"]; ok {
		var think map[string]json.RawMessage
		if err := json.Unmarshal(raw, &think); err != nil {
			return messagesInspectResult{}, &requestParseError{msg: "thinking must be an object", cause: err}
		}
		if t, ok := think["type"]; ok {
			var typ string
			if err := json.Unmarshal(t, &typ); err != nil {
				return messagesInspectResult{}, &requestParseError{msg: "thinking.type must be a string", cause: err}
			}
			if typ == "enabled" {
				reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
			}
		}
	}
	if raw, ok := fields["reasoning_effort"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
			res.ReasoningEffort = s
		}
	}
	if raw, ok := fields["stop_sequences"]; ok {
		var seq []string
		if err := json.Unmarshal(raw, &seq); err != nil {
			return messagesInspectResult{}, &requestParseError{msg: "stop_sequences must be an array of strings", cause: err}
		}
		if len(seq) > 0 {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	if raw, ok := fields["system"]; ok {
		if err := inspectMessagesSystem(raw, reqs); err != nil {
			return messagesInspectResult{}, err
		}
	}
	if raw, ok := fields["messages"]; ok {
		if err := inspectMessagesMessages(raw, reqs); err != nil {
			return messagesInspectResult{}, err
		}
	}

	if res.Stream {
		reqs.Features = appendFeature(reqs.Features, model.FeatureStreaming)
	}

	for k := range fields {
		if !knownMessagesTopLevel[k] {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
			break
		}
	}

	res.Requirements = *reqs
	return res, nil
}

func parseMessagesTools(raw json.RawMessage) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "tools must be an array", cause: err}
	}
	for i, item := range arr {
		var t map[string]json.RawMessage
		if err := json.Unmarshal(item, &t); err != nil {
			return &requestParseError{msg: fmt.Sprintf("tools[%d] must be an object", i), cause: err}
		}
		name, ok := t["name"]
		if !ok {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].name is required", i)}
		}
		var nameStr string
		if err := json.Unmarshal(name, &nameStr); err != nil || nameStr == "" {
			return &requestParseError{msg: fmt.Sprintf("tools[%d].name must be a non-empty string", i), cause: err}
		}
		if schema, ok := t["input_schema"]; ok && schema != nil && string(schema) != "null" {
			var sobj map[string]json.RawMessage
			if err := json.Unmarshal(schema, &sobj); err != nil {
				return &requestParseError{msg: fmt.Sprintf("tools[%d].input_schema must be an object", i), cause: err}
			}
		}
	}
	return nil
}

func hasNonFunctionMessagesTools(raw json.RawMessage) bool {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return false
	}
	for _, item := range arr {
		var t struct {
			Type        string          `json:"type"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(item, &t); err == nil {
			if t.Type != "" && t.Type != "function" {
				return true
			}
		}
	}
	return false
}

func inspectMessagesSystem(raw json.RawMessage, reqs *model.RequestRequirements) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "system must be a string or array", cause: err}
	}
	// The Messages↔Responses converter does not support the Messages system
	// array representation at all. Keep validating each block below, but force
	// conversion to remain native-only even when every block is text.
	reqs.NativeOnly = true
	reqs.UnknownSemantic = true
	for i, item := range arr {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(item, &block); err != nil {
			return &requestParseError{msg: fmt.Sprintf("system[%d] must be an object", i), cause: err}
		}
		if hasCacheControl(block) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
		}
		var typ string
		if t, ok := block["type"]; ok {
			if err := json.Unmarshal(t, &typ); err != nil {
				return &requestParseError{msg: fmt.Sprintf("system[%d].type must be a string", i), cause: err}
			}
		} else {
			return &requestParseError{msg: fmt.Sprintf("system[%d].type is required", i)}
		}
		if typ == "text" {
			text, ok := block["text"]
			if !ok {
				return &requestParseError{msg: fmt.Sprintf("system[%d].text is required", i)}
			}
			var textValue string
			if err := json.Unmarshal(text, &textValue); err != nil {
				return &requestParseError{msg: fmt.Sprintf("system[%d].text must be a string", i), cause: err}
			}
		}
		if typ != "text" && typ != "" {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	return nil
}

func inspectMessagesMessages(raw json.RawMessage, reqs *model.RequestRequirements) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "messages must be an array", cause: err}
	}
	for i, item := range arr {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(item, &msg); err != nil {
			return &requestParseError{msg: fmt.Sprintf("messages[%d] must be an object", i), cause: err}
		}
		if hasCacheControl(msg) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
		}
		if role, ok := msg["role"]; ok {
			var r string
			if err := json.Unmarshal(role, &r); err != nil {
				return &requestParseError{msg: fmt.Sprintf("messages[%d].role must be a string", i), cause: err}
			}
			if r == "tool" {
				reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
			}
		}
		if content, ok := msg["content"]; ok {
			if err := inspectMessagesContent(content, reqs, i); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectMessagesContent(raw json.RawMessage, reqs *model.RequestRequirements, msgIdx int) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: fmt.Sprintf("messages[%d].content must be a string or array", msgIdx), cause: err}
	}
	for i, item := range arr {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(item, &block); err != nil {
			return &requestParseError{msg: fmt.Sprintf("messages[%d].content[%d] must be an object", msgIdx, i), cause: err}
		}
		if hasCacheControl(block) {
			reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
		}
		var typ string
		if t, ok := block["type"]; ok {
			if err := json.Unmarshal(t, &typ); err != nil {
				return &requestParseError{msg: fmt.Sprintf("messages[%d].content[%d].type must be a string", msgIdx, i), cause: err}
			}
		} else {
			return &requestParseError{msg: fmt.Sprintf("messages[%d].content[%d].type is required", msgIdx, i)}
		}
		switch typ {
		case "image":
			reqs.Features = appendFeature(reqs.Features, model.FeatureVision)
			if err := requireObjectField(block, "source", fmt.Sprintf("messages[%d].content[%d]", msgIdx, i)); err != nil {
				return err
			}
		case "audio":
			reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
			if err := requireObjectField(block, "source", fmt.Sprintf("messages[%d].content[%d]", msgIdx, i)); err != nil {
				return err
			}
		case "document":
			reqs.Features = appendFeature(reqs.Features, model.FeatureDocument)
			if err := requireObjectField(block, "source", fmt.Sprintf("messages[%d].content[%d]", msgIdx, i)); err != nil {
				return err
			}
		case "tool_use", "tool_result":
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
			prefix := fmt.Sprintf("messages[%d].content[%d]", msgIdx, i)
			if typ == "tool_use" {
				for _, k := range []string{"id", "name", "input"} {
					if err := requireField(block, k, prefix); err != nil {
						return err
					}
				}
				if err := requireObjectField(block, "input", prefix); err != nil {
					return err
				}
			} else {
				for _, k := range []string{"tool_use_id", "content"} {
					if err := requireField(block, k, prefix); err != nil {
						return err
					}
				}
			}
		case "thinking", "redacted_thinking":
			reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
			prefix := fmt.Sprintf("messages[%d].content[%d]", msgIdx, i)
			if typ == "thinking" {
				for _, k := range []string{"thinking", "signature"} {
					if err := requireField(block, k, prefix); err != nil {
						return err
					}
				}
			} else {
				if err := requireField(block, "data", prefix); err != nil {
					return err
				}
			}
		}
		if !knownMessagesContentTypes[typ] {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	return nil
}

// ---------- Gemini ----------

var knownGeminiTopLevel = map[string]bool{
	"contents": true, "tools": true, "toolConfig": true, "systemInstruction": true,
	"generationConfig": true, "safetySettings": true, "cachedContent": true,
}

func inspectGeminiRequest(body []byte) (geminiInspectResult, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return geminiInspectResult{}, &requestParseError{msg: "Invalid JSON body", cause: err}
	}
	var res geminiInspectResult
	reqs := &model.RequestRequirements{}

	if raw, ok := fields["tools"]; ok {
		hasFunc, hasNonFunc, err := parseGeminiTools(raw)
		if err != nil {
			return geminiInspectResult{}, err
		}
		if hasFunc {
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
		}
		if hasNonFunc {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
		}
	}
	if raw, ok := fields["generationConfig"]; ok && raw != nil && string(raw) != "null" {
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return geminiInspectResult{}, &requestParseError{msg: "generationConfig must be an object", cause: err}
		}
		if hasField(raw, "thinkingConfig") {
			reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
			// Gemini 3 exposes a thinking level string ("low"/"high"); Gemini 2.5
			// uses a numeric thinkingBudget, which is not an effort label.
			var thinkingCfg map[string]json.RawMessage
			if cfgRaw, cfgOK := cfg["thinkingConfig"]; cfgOK {
				if err := json.Unmarshal(cfgRaw, &thinkingCfg); err == nil {
					if levelRaw, levelOK := thinkingCfg["thinkingLevel"]; levelOK {
						var level string
						if err := json.Unmarshal(levelRaw, &level); err == nil {
							res.ReasoningEffort = level
						}
					}
				}
			}
		}
		if hasField(raw, "responseMimeType") || hasField(raw, "responseSchema") {
			reqs.Features = appendFeature(reqs.Features, model.FeatureStructuredOutput)
		}
	}
	if raw, ok := fields["toolConfig"]; ok && raw != nil && string(raw) != "null" {
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return geminiInspectResult{}, &requestParseError{msg: "toolConfig must be an object", cause: err}
		}
	}
	if raw, ok := fields["cachedContent"]; ok && raw != nil && string(raw) != "null" && string(raw) != `""` {
		reqs.Features = appendFeature(reqs.Features, model.FeatureCacheControl)
	}
	if raw, ok := fields["contents"]; ok {
		if err := inspectGeminiContents(raw, reqs); err != nil {
			return geminiInspectResult{}, err
		}
	}
	if raw, ok := fields["systemInstruction"]; ok {
		if err := inspectGeminiContent(raw, reqs, -1); err != nil {
			return geminiInspectResult{}, err
		}
	}

	for k := range fields {
		if !knownGeminiTopLevel[k] {
			reqs.NativeOnly = true
			reqs.UnknownSemantic = true
			break
		}
	}

	res.Requirements = *reqs
	return res, nil
}

func parseGeminiTools(raw json.RawMessage) (hasFunc, hasNonFunc bool, err error) {
	var arr []json.RawMessage
	if unmarshalErr := json.Unmarshal(raw, &arr); unmarshalErr != nil {
		return false, false, &requestParseError{msg: "tools must be an array", cause: unmarshalErr}
	}
	for i, item := range arr {
		var t map[string]json.RawMessage
		if unmarshalErr := json.Unmarshal(item, &t); unmarshalErr != nil {
			return false, false, &requestParseError{msg: fmt.Sprintf("tools[%d] must be an object", i), cause: unmarshalErr}
		}
		decls, ok := t["functionDeclarations"]
		if !ok {
			hasNonFunc = true
			continue
		}
		var darr []json.RawMessage
		if unmarshalErr := json.Unmarshal(decls, &darr); unmarshalErr != nil {
			return false, false, &requestParseError{msg: fmt.Sprintf("tools[%d].functionDeclarations must be an array", i), cause: unmarshalErr}
		}
		if len(darr) == 0 {
			return false, false, &requestParseError{msg: fmt.Sprintf("tools[%d].functionDeclarations must not be empty", i)}
		}
		for j, d := range darr {
			var decl map[string]json.RawMessage
			if unmarshalErr := json.Unmarshal(d, &decl); unmarshalErr != nil {
				return false, false, &requestParseError{msg: fmt.Sprintf("tools[%d].functionDeclarations[%d] must be an object", i, j), cause: unmarshalErr}
			}
			name, ok := decl["name"]
			if !ok {
				return false, false, &requestParseError{msg: fmt.Sprintf("tools[%d].functionDeclarations[%d].name is required", i, j)}
			}
			var nameStr string
			if unmarshalErr := json.Unmarshal(name, &nameStr); unmarshalErr != nil || nameStr == "" {
				return false, false, &requestParseError{msg: fmt.Sprintf("tools[%d].functionDeclarations[%d].name must be a non-empty string", i, j), cause: unmarshalErr}
			}
		}
		hasFunc = true
	}
	return hasFunc, hasNonFunc, nil
}

func hasField(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

func inspectGeminiContents(raw json.RawMessage, reqs *model.RequestRequirements) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: "contents must be an array", cause: err}
	}
	for i, item := range arr {
		if err := inspectGeminiContent(item, reqs, i); err != nil {
			return err
		}
	}
	return nil
}

func inspectGeminiContent(raw json.RawMessage, reqs *model.RequestRequirements, idx int) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return &requestParseError{msg: fmt.Sprintf("contents[%d] must be an object", idx), cause: err}
	}
	if parts, ok := obj["parts"]; ok {
		if err := inspectGeminiParts(parts, reqs, idx); err != nil {
			return err
		}
	}
	return nil
}

func inspectGeminiParts(raw json.RawMessage, reqs *model.RequestRequirements, contentIdx int) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return &requestParseError{msg: fmt.Sprintf("contents[%d].parts must be an array", contentIdx), cause: err}
	}
	for i, item := range arr {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(item, &part); err != nil {
			return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d] must be an object", contentIdx, i), cause: err}
		}
		// Gemini thought metadata is independent of the payload kind. Inspect it
		// first, then classify the remaining payload so map iteration order cannot
		// make text/function parts disappear behind thought fields.
		payload := make(map[string]json.RawMessage, len(part))
		if rawThought, ok := part["thought"]; ok {
			var thought bool
			if rawThought == nil || string(rawThought) == "null" || json.Unmarshal(rawThought, &thought) != nil {
				return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d].thought must be a boolean", contentIdx, i)}
			}
			if thought {
				reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
			}
		}
		if rawSignature, ok := part["thoughtSignature"]; ok {
			var signature string
			if rawSignature == nil || string(rawSignature) == "null" || json.Unmarshal(rawSignature, &signature) != nil {
				return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d].thoughtSignature must be a string", contentIdx, i)}
			}
			reqs.Features = appendFeature(reqs.Features, model.FeatureReasoning)
		}
		for key, value := range part {
			if key != "thought" && key != "thoughtSignature" {
				payload[key] = value
			}
		}
		typ := classifyGeminiPart(payload)
		switch typ {
		case "inlineData":
			if err := inspectGeminiInlineData(payload["inlineData"], reqs, contentIdx, i); err != nil {
				return err
			}
		case "fileData":
			if err := inspectGeminiFileData(payload["fileData"], reqs, contentIdx, i); err != nil {
				return err
			}
		case "functionCall", "functionResponse":
			key := typ
			if err := inspectGeminiFunctionCallResponse(payload[key], contentIdx, i); err != nil {
				return err
			}
			reqs.Features = appendFeature(reqs.Features, model.FeatureTools)
		case "text":
		default:
			if len(payload) > 0 {
				reqs.NativeOnly = true
				reqs.UnknownSemantic = true
			}
		}
	}
	return nil
}

func classifyGeminiPart(part map[string]json.RawMessage) string {
	for _, k := range []string{"text", "inlineData", "fileData", "functionCall", "functionResponse"} {
		if v, ok := part[k]; ok && v != nil && string(v) != "null" {
			return k
		}
	}
	return ""
}

func inspectGeminiInlineData(raw json.RawMessage, reqs *model.RequestRequirements, contentIdx, partIdx int) error {
	var inline map[string]json.RawMessage
	if err := json.Unmarshal(raw, &inline); err != nil {
		return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d].inlineData must be an object", contentIdx, partIdx), cause: err}
	}
	var mt string
	if mime, ok := inline["mimeType"]; ok {
		if err := json.Unmarshal(mime, &mt); err != nil {
			return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d].inlineData.mimeType must be a string", contentIdx, partIdx), cause: err}
		}
	}
	if mt == "" {
		return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d].inlineData.mimeType is required", contentIdx, partIdx)}
	}
	if strings.HasPrefix(mt, "audio/") {
		reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
	} else if strings.HasPrefix(mt, "image/") {
		reqs.Features = appendFeature(reqs.Features, model.FeatureVision)
	} else {
		reqs.Features = appendFeature(reqs.Features, model.FeatureDocument)
	}
	if err := requireField(inline, "data", fmt.Sprintf("contents[%d].parts[%d].inlineData", contentIdx, partIdx)); err != nil {
		return err
	}
	return nil
}

func inspectGeminiFileData(raw json.RawMessage, reqs *model.RequestRequirements, contentIdx, partIdx int) error {
	var fd map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fd); err != nil {
		return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d].fileData must be an object", contentIdx, partIdx), cause: err}
	}
	if err := requireField(fd, "fileUri", fmt.Sprintf("contents[%d].parts[%d].fileData", contentIdx, partIdx)); err != nil {
		return err
	}
	var mt string
	if rawMime, ok := fd["mimeType"]; ok {
		_ = json.Unmarshal(rawMime, &mt)
	}
	switch {
	case strings.HasPrefix(mt, "audio/"):
		reqs.Features = appendFeature(reqs.Features, model.FeatureAudio)
	case strings.HasPrefix(mt, "image/"):
		reqs.Features = appendFeature(reqs.Features, model.FeatureVision)
	default:
		reqs.Features = appendFeature(reqs.Features, model.FeatureDocument)
	}
	return nil
}

func inspectGeminiFunctionCallResponse(raw json.RawMessage, contentIdx, partIdx int) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return &requestParseError{msg: fmt.Sprintf("contents[%d].parts[%d] function call/response must be an object", contentIdx, partIdx), cause: err}
	}
	return requireField(obj, "name", fmt.Sprintf("contents[%d].parts[%d]", contentIdx, partIdx))
}

// ---------- helpers used by handlers ----------

func parseAndInspectResponses(body []byte) (responsesInspectResult, *model.RequestRequirements, error) {
	res, err := inspectResponsesRequest(body)
	if err != nil {
		return responsesInspectResult{}, nil, err
	}
	return res, &res.Requirements, nil
}

func parseAndInspectMessages(body []byte) (messagesInspectResult, *model.RequestRequirements, error) {
	res, err := inspectMessagesRequest(body)
	if err != nil {
		return messagesInspectResult{}, nil, err
	}
	return res, &res.Requirements, nil
}

func parseAndInspectChat(body []byte) (chatInspectResult, *model.RequestRequirements, error) {
	res, err := inspectChatRequest(body)
	if err != nil {
		return chatInspectResult{}, nil, err
	}
	return res, &res.Requirements, nil
}

func parseAndInspectGemini(body []byte) (geminiInspectResult, *model.RequestRequirements, error) {
	res, err := inspectGeminiRequest(body)
	if err != nil {
		return geminiInspectResult{}, nil, err
	}
	return res, &res.Requirements, nil
}
