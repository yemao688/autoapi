package toolconfig

// Canonical interface-format keys stored in Preset.Vendor.
const (
	VendorOpenAIResponses  = "openai-responses"
	VendorOpenAICompatible = "openai-compatible"
	VendorAnthropic        = "anthropic"
	VendorAmazonBedrock    = "amazon-bedrock"
	VendorGoogleGemini     = "google-gemini"
)

// NormalizeVendor converts legacy OpenCode npm package names and canonical
// interface-format keys into the value persisted in Preset.Vendor.
func NormalizeVendor(vendor string) string {
	switch vendor {
	case VendorOpenAIResponses, "@ai-sdk/openai":
		return VendorOpenAIResponses
	case VendorOpenAICompatible, "@ai-sdk/openai-compatible":
		return VendorOpenAICompatible
	case VendorAnthropic, "@ai-sdk/anthropic":
		return VendorAnthropic
	case VendorAmazonBedrock, "@ai-sdk/amazon-bedrock":
		return VendorAmazonBedrock
	case VendorGoogleGemini, "@ai-sdk/google", "@ai-sdk/google-generative-ai":
		return VendorGoogleGemini
	default:
		return VendorOpenAICompatible
	}
}

// OpenCodeNpmPackage maps a canonical interface-format key to the package
// OpenCode expects in a provider entry. Legacy package values are accepted for
// compatibility with presets created before the canonical keys were added.
func OpenCodeNpmPackage(vendor string) string {
	switch NormalizeVendor(vendor) {
	case VendorOpenAIResponses:
		return "@ai-sdk/openai"
	case VendorAnthropic:
		return "@ai-sdk/anthropic"
	case VendorAmazonBedrock:
		return "@ai-sdk/amazon-bedrock"
	case VendorGoogleGemini:
		return "@ai-sdk/google"
	case VendorOpenAICompatible:
		fallthrough
	default:
		return "@ai-sdk/openai-compatible"
	}
}
