package service

import (
	"strings"
	"testing"
	"time"
)

func TestParseChatNonStreamContentForms(t *testing.T) {
	s := &Service{}
	start := time.Now()
	t.Run("string content", func(t *testing.T) {
		result := s.parseChatNonStream([]byte(`{"choices":[{"message":{"content":" ok "},"finish_reason":"stop"}]}`), start)
		if !result.OK || result.Response != " ok " || result.FinishReason != "stop" {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("array content", func(t *testing.T) {
		result := s.parseChatNonStream([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"hello"},{"type":"text","text":" world"}]},"finish_reason":"stop"}]}`), start)
		if !result.OK || result.Response != "hello world" {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("length without visible output is diagnostic", func(t *testing.T) {
		result := s.parseChatNonStream([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`), start)
		if result.OK || result.FinishReason != "length" || !strings.Contains(result.Error, "max_tokens") || !strings.Contains(result.Error, "reasoning") {
			t.Fatalf("result=%+v", result)
		}
	})
}

func TestParseChatStreamReasoningAndLength(t *testing.T) {
	s := &Service{}
	start := time.Now()
	stream := "data: {\"choices\":[{\"delta\":{\"reasoning\":\"hidden\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"
	result := s.parseChatStream(strings.NewReader(stream), start)
	if result.OK || result.FinishReason != "length" || !strings.Contains(result.Error, "max_tokens") || !strings.Contains(result.Error, "reasoning") {
		t.Fatalf("result=%+v", result)
	}
}
