package toolconfig

import (
	"testing"

	"github.com/tailscale/hujson"
)

func TestPackFormattedGolden(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "nested objects and arrays",
			in:   `{"server":{"host":"localhost","ports":[80,443]},"enabled":true}`,
			want: "{\n  \"server\": {\n    \"host\": \"localhost\",\n    \"ports\": [\n      80,\n      443\n    ]\n  },\n  \"enabled\": true\n}\n",
		},
		{
			name: "provider block",
			in:   `{"provider":{"autoapi":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:8344/v1"},"models":{"m1":{}}}}}`,
			want: "{\n  \"provider\": {\n    \"autoapi\": {\n      \"npm\": \"@ai-sdk/openai-compatible\",\n      \"options\": {\n        \"baseURL\": \"http://127.0.0.1:8344/v1\"\n      },\n      \"models\": {\n        \"m1\": {}\n      }\n    }\n  }\n}\n",
		},
		{
			name: "array of scalars",
			in:   `{"items":[1,2,3]}`,
			want: "{\n  \"items\": [\n    1,\n    2,\n    3\n  ]\n}\n",
		},
		{
			name: "empty composites stay inline",
			in:   `{"object":{},"array":[]}`,
			want: "{\n  \"object\": {},\n  \"array\": []\n}\n",
		},
		{
			name: "strings do not affect scanning",
			in:   `{"text":"brace } [ and // not a comment","url":"https://example.test/a/*b*/","value":1}`,
			want: "{\n  \"text\": \"brace } [ and // not a comment\",\n  \"url\": \"https://example.test/a/*b*/\",\n  \"value\": 1\n}\n",
		},
		{
			name: "block comments are preserved",
			in:   `{"value":1 /* } [ */,"other":2}`,
			want: "{\n  \"value\": 1 /* } [ */,\n  \"other\": 2\n}\n",
		},
		{
			name: "multiline composites remain unchanged",
			in:   "{\n  \"keep\": {\n    \"nested\": 1\n  }\n}\n",
			want: "{\n  \"keep\": {\n    \"nested\": 1\n  }\n}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := hujson.Parse([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			got, err := packFormatted(doc)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("packFormatted() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPackFormattedIsIdempotent(t *testing.T) {
	doc, err := hujson.Parse([]byte(`{"server":{"host":"localhost","ports":[80,443]},"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := packFormatted(doc)
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := hujson.Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := packFormatted(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Fatalf("packFormatted() is not idempotent:\nfirst: %s\nsecond: %s", first, second)
	}
}

func TestPackFormattedEscapedStringsAndComments(t *testing.T) {
	doc, err := hujson.Parse([]byte(`{"value":"escaped quote: \" } [ // /* */","nested":{"ok":true} /* { [ } ] */}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := packFormatted(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"value\": \"escaped quote: \\\" } [ // /* */\",\n  \"nested\": {\n    \"ok\": true\n  } /* { [ } ] */\n}\n"
	if string(got) != want {
		t.Fatalf("packFormatted() = %q, want %q", got, want)
	}
}

func TestExpandInlineCompositesLeavesLineCommentLayoutUntouched(t *testing.T) {
	in := []byte("{\"value\":1 // keep\n,\"other\":2}")
	got := expandInlineComposites(in)
	if string(got) != string(in) {
		t.Fatalf("expandInlineComposites() = %q, want %q", got, in)
	}
}

func TestPackFormattedNormalizesTrailingNewline(t *testing.T) {
	for _, in := range []string{`{"a":1}`, "{\"a\":1}\n"} {
		doc, err := hujson.Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		got, err := packFormatted(doc)
		if err != nil {
			t.Fatal(err)
		}
		want := "{\n  \"a\": 1\n}\n"
		if string(got) != want {
			t.Errorf("packFormatted(%q) = %q, want %q", in, got, want)
		}
	}
}
