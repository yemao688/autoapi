package proxy

import (
	"autoapi/internal/model"
	"strings"
	"testing"
)

func TestInspectChatFeatures(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       []model.Feature
		nativeOnly bool
		unknown    bool
	}{
		{"text only", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil, false, false},
		{"tools", `{"model":"m","tools":[{"type":"function","function":{"name":"n","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`, []model.Feature{model.FeatureTools}, false, false},
		{"tool result", `{"model":"m","messages":[{"role":"tool","tool_call_id":"x","content":"ok"}]}`, []model.Feature{model.FeatureTools}, false, false},
		{"vision", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png"}}]}]}`, []model.Feature{model.FeatureVision}, false, false},
		{"audio", `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x","format":"mp3"}}]}]}`, []model.Feature{model.FeatureAudio}, false, false},
		{"structured", `{"model":"m","response_format":{"type":"json_object"},"messages":[]}`, []model.Feature{model.FeatureStructuredOutput}, false, false},
		{"streaming", `{"model":"m","stream":true,"messages":[]}`, []model.Feature{model.FeatureStreaming}, false, false},
		{"cache_control", `{"model":"m","messages":[{"role":"user","content":"hi","cache_control":{"type":"ephemeral"}}]}`, []model.Feature{model.FeatureCacheControl}, false, false},
		{"unknown content block", `{"model":"m","messages":[{"role":"user","content":[{"type":"custom"}]}]}`, nil, true, true},
		{"unknown top-level", `{"model":"m","messages":[],"foo":"bar"}`, nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectChatRequest([]byte(tc.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q in %+v", f, res.Requirements.Features)
				}
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
			if tc.unknown && !res.Requirements.UnknownSemantic {
				t.Fatal("expected unknown semantic")
			}
		})
	}
}

func TestInspectChatMalformedAndInvalidTool(t *testing.T) {
	for _, body := range []string{
		`{`,
		`{"model":"m","tools":"not-array"}`,
		`{"model":"m","tools":[{"type":"function"}]}`,
		`{"model":"m","tools":[{"type":"function","function":{}}]}`,
		`{"model":"m","tools":[{"type":"function","function":{"parameters":[]}}]}`,
		`{"model":"m","messages":[{"role":"user","tool_calls":[{"type":"function","function":{"arguments":"not json"}}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":{}}]}`,
	} {
		if _, err := inspectChatRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		} else if _, ok := err.(*requestParseError); !ok {
			t.Fatalf("expected requestParseError for %s, got %T", body, err)
		}
	}
}

func TestInspectResponsesFeatures(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		want    []model.Feature
	}{
		{"text", `{"model":"m","input":"hi"}`, false, nil},
		{"tools", `{"model":"m","input":"hi","tools":[{"type":"function","name":"n","parameters":{"type":"object"}}]}`, false, []model.Feature{model.FeatureTools}},
		{"structured", `{"model":"m","input":"hi","response_format":{"type":"json_schema","json_schema":{"name":"s","schema":{}}}}`, false, []model.Feature{model.FeatureStructuredOutput}},
		{"text format", `{"model":"m","input":"hi","text":{"format":{"type":"json_schema","json_schema":{"name":"s","schema":{}}}}}`, false, []model.Feature{model.FeatureStructuredOutput}},
		{"streaming", `{"model":"m","input":"hi","stream":true}`, false, []model.Feature{model.FeatureStreaming}},
		{"stateful rejected", `{"model":"m","input":"hi","previous_response_id":"x"}`, true, nil},
		{"unknown input block", `{"model":"m","input":[{"type":"custom"}]}`, false, nil},
		{"message item inferred", `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, false, nil},
		{"input image", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`, false, []model.Feature{model.FeatureVision}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectResponsesRequest([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q", f)
				}
			}
		})
	}
}

func TestInspectResponsesToolSchema(t *testing.T) {
	valid := `{"model":"m","input":"hi","tools":[{"type":"function","name":"n","parameters":{"type":"object"}}]}`
	if _, err := inspectResponsesRequest([]byte(valid)); err != nil {
		t.Fatalf("valid responses tools rejected: %v", err)
	}
	invalid := []string{
		`{"model":"m","input":"hi","tools":[{"type":"function"}]}`,
		`{"model":"m","input":"hi","tools":[{"type":"function","name":"","parameters":{}}]}`,
		`{"model":"m","input":"hi","tools":[{"type":"function","name":"n","parameters":[]}]}`,
	}
	for _, body := range invalid {
		if _, err := inspectResponsesRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestInspectMessagesFeatures(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       []model.Feature
		nativeOnly bool
	}{
		{"text", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil, false},
		{"vision", `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x","media_type":"image/png"}}]}]}`, []model.Feature{model.FeatureVision}, false},
		{"audio", `{"model":"m","messages":[{"role":"user","content":[{"type":"audio","source":{"type":"base64","data":"x","media_type":"audio/mp3"}}]}]}`, []model.Feature{model.FeatureAudio}, false},
		{"document", `{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","data":"x","media_type":"application/pdf"}}]}]}`, []model.Feature{model.FeatureDocument}, false},
		{"tool use", `{"model":"m","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"n","input":{}}]}]}`, []model.Feature{model.FeatureTools}, false},
		{"tool result", `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_use_id":"x","content":"ok"}]}`, []model.Feature{model.FeatureTools}, false},
		{"reasoning", `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"y"}]}]}`, []model.Feature{model.FeatureReasoning}, false},
		{"cache", `{"model":"m","system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`, []model.Feature{model.FeatureCacheControl}, true},
		{"stop sequences", `{"model":"m","messages":[{"role":"user","content":"hi"}],"stop_sequences":["x"]}`, nil, true},
		{"unknown content", `{"model":"m","messages":[{"role":"user","content":[{"type":"video"}]}]}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectMessagesRequest([]byte(tc.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q", f)
				}
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
		})
	}
}

func TestInspectMessagesToolSchema(t *testing.T) {
	valid := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"n","input_schema":{"type":"object"}}]}`
	if _, err := inspectMessagesRequest([]byte(valid)); err != nil {
		t.Fatalf("valid anthropic tools rejected: %v", err)
	}
	invalid := []string{
		`{"model":"m","messages":[],"tools":[{}]}`,
		`{"model":"m","messages":[],"tools":[{"name":""}]}`,
		`{"model":"m","messages":[],"tools":[{"name":"n","input_schema":[]}]}`,
	}
	for _, body := range invalid {
		if _, err := inspectMessagesRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestInspectMessagesKnownShapeErrors(t *testing.T) {
	invalid := []string{
		`{"model":"m","messages":"hi"}`,
		`{"model":"m","messages":[{"role":123,"content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image"}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{}]}]}`,
		`{"model":"m","system":[{"type":123}]}`,
		`{"model":"m","stop_sequences":"x"}`,
		`{"model":"m","tools":[{"name":"n","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"tool_use"}]}]}`,
	}
	for _, body := range invalid {
		if _, err := inspectMessagesRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestInspectGeminiFeatures(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       []model.Feature
		nativeOnly bool
	}{
		{"text", `{"contents":[{"parts":[{"text":"hi"}]}]}`, nil, false},
		{"image", `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"x"}}]}]}`, []model.Feature{model.FeatureVision}, false},
		{"audio", `{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/mp3","data":"x"}}]}]}`, []model.Feature{model.FeatureAudio}, false},
		{"document", `{"contents":[{"parts":[{"fileData":{"mimeType":"application/pdf","fileUri":"x"}}]}]}`, []model.Feature{model.FeatureDocument}, false},
		{"tools", `{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"n"}]}]}`, []model.Feature{model.FeatureTools}, false},
		{"cache", `{"contents":[{"parts":[{"text":"hi"}]}],"cachedContent":"x"}`, []model.Feature{model.FeatureCacheControl}, false},
		{"unknown part", `{"contents":[{"parts":[{"custom":"x"}]}]}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectGeminiRequest([]byte(tc.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q", f)
				}
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
		})
	}
}

func TestInspectGeminiToolSchema(t *testing.T) {
	valid := `{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"n"}]}]}`
	if _, err := inspectGeminiRequest([]byte(valid)); err != nil {
		t.Fatalf("valid gemini tools rejected: %v", err)
	}
	invalid := []string{
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[]}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":""}]}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":"x"}]}`,
	}
	for _, body := range invalid {
		if _, err := inspectGeminiRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestInspectGeminiKnownShapeErrors(t *testing.T) {
	invalid := []string{
		`{"contents":"hi"}`,
		`{"contents":[{"parts":"hi"}]}`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png"}}]}]}`,
		`{"contents":[{"parts":[{"inlineData":[]}]}]}`,
	}
	for _, body := range invalid {
		if _, err := inspectGeminiRequest([]byte(body)); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestInspectMessagesSystemArrayAlwaysNativeOnly(t *testing.T) {
	textArray := `{"model":"m","system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"hi"}]}`
	res, err := inspectMessagesRequest([]byte(textArray))
	if err != nil {
		t.Fatalf("text system array rejected: %v", err)
	}
	if !res.Requirements.NativeOnly || !res.Requirements.UnknownSemantic {
		t.Fatalf("text system array must be native-only: %+v", res.Requirements)
	}
	if res.Requirements.Has(model.FeatureCacheControl) {
		t.Fatal("plain text system array unexpectedly has cache feature")
	}

	cacheArray := `{"model":"m","system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"messages":[]}`
	res, err = inspectMessagesRequest([]byte(cacheArray))
	if err != nil {
		t.Fatalf("cached system array rejected: %v", err)
	}
	if !res.Requirements.Has(model.FeatureCacheControl) || !res.Requirements.NativeOnly {
		t.Fatalf("cached system array requirements: %+v", res.Requirements)
	}

	for _, body := range []string{
		`{"model":"m","system":[{"type":"text"}],"messages":[]}`,
		`{"model":"m","system":[{"type":"text","text":123}],"messages":[]}`,
		`{"model":"m","system":[{}],"messages":[]}`,
	} {
		if _, err := inspectMessagesRequest([]byte(body)); err == nil {
			t.Fatalf("expected system array shape error: %s", body)
		}
	}
}

func TestInspectGeminiPartReasoningMetadataIsOrderIndependent(t *testing.T) {
	cases := []string{
		`{"contents":[{"parts":[{"text":"hi","thought":true}]}]}`,
		`{"contents":[{"parts":[{"text":"hi","thoughtSignature":"sig"}]}]}`,
		`{"contents":[{"parts":[{"functionCall":{"name":"lookup"},"thoughtSignature":"sig"}]}]}`,
	}
	for _, body := range cases {
		for i := 0; i < 5; i++ {
			res, err := inspectGeminiRequest([]byte(body))
			if err != nil {
				t.Fatalf("iteration %d body=%s: %v", i, body, err)
			}
			if !res.Requirements.Has(model.FeatureReasoning) {
				t.Fatalf("iteration %d missing reasoning for %s", i, body)
			}
		}
	}
	for _, body := range []string{
		`{"contents":[{"parts":[{"text":"hi","thought":"true"}]}]}`,
		`{"contents":[{"parts":[{"text":"hi","thoughtSignature":123}]}]}`,
		`{"contents":[{"parts":[{"functionCall":{"name":"lookup"},"thoughtSignature":false}]}]}`,
	} {
		if _, err := inspectGeminiRequest([]byte(body)); err == nil {
			t.Fatalf("expected reasoning metadata shape error: %s", body)
		}
	}
}

func TestInspectMalformed(t *testing.T) {
	for _, fn := range []func([]byte) (interface{}, error){
		func(b []byte) (interface{}, error) { r, e := inspectChatRequest(b); return r, e },
		func(b []byte) (interface{}, error) { r, e := inspectResponsesRequest(b); return r, e },
		func(b []byte) (interface{}, error) { r, e := inspectMessagesRequest(b); return r, e },
		func(b []byte) (interface{}, error) { r, e := inspectGeminiRequest(b); return r, e },
	} {
		if _, err := fn([]byte(`{`)); err == nil || !strings.Contains(err.Error(), "Invalid JSON") {
			t.Fatalf("expected invalid json error, got %v", err)
		}
	}
}

func TestInspectChatPreservationAndAudio(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       []model.Feature
		nativeOnly bool
		wantErr    bool
	}{
		{"tool_choice marks native", `{"model":"m","messages":[],"tool_choice":"auto"}`, nil, true, false},
		{"metadata marks native", `{"model":"m","messages":[],"metadata":{"client":"x"}}`, nil, true, false},
		{"non-function tool marks native", `{"model":"m","messages":[],"tools":[{"type":"code_interpreter"}]}`, nil, true, false},
		{"audio object", `{"model":"m","messages":[],"audio":{"voice":"alloy","format":"pcm16"}}`, []model.Feature{model.FeatureAudio}, false, false},
		{"modalities audio", `{"model":"m","messages":[],"modalities":["audio"]}`, []model.Feature{model.FeatureAudio}, false, false},
		{"response_format wrong shape", `{"model":"m","messages":[],"response_format":["x"]}`, nil, false, true},
		{"missing model", `{"messages":[]}`, nil, false, true},
		{"empty model", `{"model":"","messages":[]}`, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectChatRequest([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q", f)
				}
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
		})
	}
}

func TestInspectMessagesPreservation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		nativeOnly bool
		wantErr    bool
	}{
		{"tool_choice marks native", `{"model":"m","messages":[],"tool_choice":"auto"}`, true, false},
		{"metadata marks native", `{"model":"m","messages":[],"metadata":{"client":"x"}}`, true, false},
		{"system block array non-text", `{"model":"m","system":[{"type":"image","source":{"type":"base64","data":"x","media_type":"image/png"}}],"messages":[]}`, true, false},
		{"non-function tool marks native", `{"model":"m","messages":[],"tools":[{"name":"x","type":"host"}]}`, true, false},
		{"thinking wrong shape", `{"model":"m","messages":[],"thinking":"enabled"}`, false, true},
		{"missing model", `{"messages":[]}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectMessagesRequest([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
		})
	}
}

func TestInspectResponsesPreservationAndReasoning(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       []model.Feature
		nativeOnly bool
		wantErr    bool
	}{
		{"tool_choice marks native", `{"model":"m","input":"hi","tool_choice":"auto"}`, nil, true, false},
		{"parallel_tool_calls marks native", `{"model":"m","input":"hi","parallel_tool_calls":true}`, nil, true, false},
		{"truncation marks native", `{"model":"m","input":"hi","truncation":"auto"}`, nil, true, false},
		{"non-function tool marks native", `{"model":"m","input":"hi","tools":[{"type":"host","name":"x"}]}`, nil, true, false},
		{"reasoning object", `{"model":"m","input":"hi","reasoning":{"effort":"medium"}}`, []model.Feature{model.FeatureReasoning}, false, false},
		{"reasoning wrong shape", `{"model":"m","input":"hi","reasoning":"medium"}`, nil, false, true},
		{"response_format wrong shape", `{"model":"m","input":"hi","response_format":["x"]}`, nil, false, true},
		{"text wrong shape", `{"model":"m","input":"hi","text":"plain"}`, nil, false, true},
		{"missing model", `{"input":"hi"}`, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectResponsesRequest([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q", f)
				}
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
		})
	}
}

func TestInspectGeminiCapabilitiesAndShape(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		want       []model.Feature
		nativeOnly bool
		wantErr    bool
	}{
		{"googleSearch native", `{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}}]}`, nil, true, false},
		{"codeExecution native", `{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"codeExecution":{}}]}`, nil, true, false},
		{"thinkingConfig reasoning", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":1000}}}`, []model.Feature{model.FeatureReasoning}, false, false},
		{"responseMimeType structured", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"responseMimeType":"application/json"}}`, []model.Feature{model.FeatureStructuredOutput}, false, false},
		{"responseSchema structured", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"responseSchema":{"type":"object"}}}`, []model.Feature{model.FeatureStructuredOutput}, false, false},
		{"fileData image", `{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"x"}}]}]}`, []model.Feature{model.FeatureVision}, false, false},
		{"fileData audio", `{"contents":[{"parts":[{"fileData":{"mimeType":"audio/mp3","fileUri":"x"}}]}]}`, []model.Feature{model.FeatureAudio}, false, false},
		{"inlineData missing mimeType", `{"contents":[{"parts":[{"inlineData":{"data":"x"}}]}]}`, nil, false, true},
		{"generationConfig wrong shape", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":[]}`, nil, false, true},
		{"toolConfig wrong shape", `{"contents":[{"parts":[{"text":"hi"}]}],"toolConfig":"x"}`, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspectGeminiRequest([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, f := range tc.want {
				if !res.Requirements.Has(f) {
					t.Fatalf("missing feature %q", f)
				}
			}
			if tc.nativeOnly && !res.Requirements.NativeOnly {
				t.Fatal("expected native only")
			}
		})
	}
}
