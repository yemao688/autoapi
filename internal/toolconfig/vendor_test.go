package toolconfig

import "testing"

func TestNormalizeVendor(t *testing.T) {
	tests := map[string]string{
		"":                             VendorOpenAICompatible,
		"unknown":                      VendorOpenAICompatible,
		VendorOpenAIResponses:          VendorOpenAIResponses,
		VendorOpenAICompatible:         VendorOpenAICompatible,
		VendorAnthropic:                VendorAnthropic,
		VendorAmazonBedrock:            VendorAmazonBedrock,
		VendorGoogleGemini:             VendorGoogleGemini,
		"@ai-sdk/openai":               VendorOpenAIResponses,
		"@ai-sdk/openai-compatible":    VendorOpenAICompatible,
		"@ai-sdk/anthropic":            VendorAnthropic,
		"@ai-sdk/amazon-bedrock":       VendorAmazonBedrock,
		"@ai-sdk/google":               VendorGoogleGemini,
		"@ai-sdk/google-generative-ai": VendorGoogleGemini,
	}
	for input, want := range tests {
		if got := NormalizeVendor(input); got != want {
			t.Errorf("NormalizeVendor(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenCodeNpmPackage(t *testing.T) {
	tests := map[string]string{
		VendorOpenAIResponses:  "@ai-sdk/openai",
		VendorOpenAICompatible: "@ai-sdk/openai-compatible",
		VendorAnthropic:        "@ai-sdk/anthropic",
		VendorAmazonBedrock:    "@ai-sdk/amazon-bedrock",
		VendorGoogleGemini:     "@ai-sdk/google",
		"@ai-sdk/google":       "@ai-sdk/google",
	}
	for input, want := range tests {
		if got := OpenCodeNpmPackage(input); got != want {
			t.Errorf("OpenCodeNpmPackage(%q) = %q, want %q", input, got, want)
		}
	}
}
