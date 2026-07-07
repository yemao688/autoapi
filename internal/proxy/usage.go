// usage.go parses token usage from upstream responses. For non-streaming
// responses it reads the standard usage object. For streaming responses it
// scans SSE data lines and extracts usage from the last/final chunk. If no
// usage is present, it falls back to len(body)/4 heuristics.
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

type usageField struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

type usageResponse struct {
	Usage usageField `json:"usage"`
}

type streamChunk struct {
	Usage *usageField `json:"usage"`
}

// parseJSONUsage extracts usage from a non-streaming JSON response body.
// Returns (0,0) if usage is missing or the body cannot be parsed.
func parseJSONUsage(body []byte) (input, output int) {
	var resp usageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, 0
	}
	return resp.Usage.PromptTokens + resp.Usage.InputTokens,
		resp.Usage.CompletionTokens + resp.Usage.OutputTokens
}

// parseStreamUsage scans SSE data lines and accumulates usage fields. It handles
// both OpenAI (data: {...} ending with [DONE]) and Anthropic (event:
// message_delta followed by data: {"usage":...}) dialects by looking for usage
// in every data payload.
func parseStreamUsage(body []byte) (input, output int) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage == nil {
			continue
		}
		input += chunk.Usage.PromptTokens + chunk.Usage.InputTokens
		output += chunk.Usage.CompletionTokens + chunk.Usage.OutputTokens
	}
	return
}

// estimateChatInputTokens returns the simple heuristic len(messages)/4.
func estimateChatInputTokens(messageCount int) int {
	return messageCount / 4
}

// estimateEmbeddingInputTokens returns the number of input items (or 1 for a
// single string). It is intentionally coarse for v1.
func estimateEmbeddingInputTokens(input interface{}) int {
	switch v := input.(type) {
	case string:
		return 1
	case []interface{}:
		return len(v)
	case []string:
		return len(v)
	default:
		return 0
	}
}
