package toolconfig

import (
	"fmt"
	"strings"
)

// AutoapiBaseURL returns the legacy endpoint shape expected by each supported
// client. Vendor-aware callers should use AutoapiBaseURLForVendor.
func AutoapiBaseURL(tool Tool, relayAddr string) string {
	return AutoapiBaseURLForVendor(tool, relayAddr, "")
}

// AutoapiBaseURLForVendor returns the endpoint shape expected by a supported
// client and OpenCode provider vendor. An empty result means relayAddr was
// empty or the tool is unsupported.
func AutoapiBaseURLForVendor(tool Tool, relayAddr, vendor string) string {
	addr := strings.TrimRight(strings.TrimSpace(relayAddr), "/")
	if addr == "" {
		return ""
	}
	switch tool {
	case ToolOpencode:
		if NormalizeVendor(vendor) == VendorGoogleGemini {
			return addr + "/v1beta"
		}
		return addr + "/v1"
	case ToolCodex:
		return addr + "/v1"
	case ToolClaude:
		return addr
	default:
		return ""
	}
}

// BuildAutoapiPreset builds the relay preset used by an external tool. The
// API key ID is itself the relay token, so the plaintext field is intentionally
// set to the same value and is never decrypted here. The optional vendor keeps
// old callers source-compatible while allowing newer callers to select the
// OpenCode provider interface.
func BuildAutoapiPreset(tool Tool, name, relayAddr, apiKeyID string, models []PresetModel, vendors ...string) (PresetPlaintext, error) {
	vendorInput := ""
	if len(vendors) > 0 {
		vendorInput = vendors[0]
	}
	vendor, err := normalizeAutoapiVendor(tool, vendorInput)
	if err != nil {
		return PresetPlaintext{}, err
	}
	if err := validateAutoapiBuild(tool, name, relayAddr, apiKeyID, models); err != nil {
		return PresetPlaintext{}, err
	}
	baseURL := AutoapiBaseURLForVendor(tool, relayAddr, vendor)

	modelCopy := append([]PresetModel(nil), models...)
	hasDefault := false
	for _, model := range modelCopy {
		if model.Default {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		modelCopy[0].Default = true
	}

	return PresetPlaintext{
		Preset: Preset{
			Tool:       tool,
			Kind:       PresetAutoapi,
			Name:       name,
			ProviderID: "autoapi",
			Vendor:     vendor,
			BaseURL:    baseURL,
			APIKeyID:   apiKeyID,
			Models:     modelCopy,
		},
		APIKey: apiKeyID,
	}, nil
}

func normalizeAutoapiVendor(tool Tool, vendor string) (string, error) {
	vendor = strings.TrimSpace(vendor)
	if vendor == "" {
		if tool == ToolOpencode {
			return VendorOpenAICompatible, nil
		}
		return "", nil
	}
	normalized := NormalizeVendor(vendor)
	if normalized == VendorAmazonBedrock {
		return "", fmt.Errorf("%w: vendor %q is not supported for relay presets", ErrInvalidPreset, vendor)
	}
	return normalized, nil
}

func validateAutoapiBuild(tool Tool, name, relayAddr, apiKeyID string, models []PresetModel) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is empty", ErrInvalidPreset)
	}
	if strings.TrimSpace(relayAddr) == "" {
		return fmt.Errorf("%w: relay address is empty", ErrInvalidPreset)
	}
	if strings.TrimSpace(apiKeyID) == "" {
		return fmt.Errorf("%w: API key ID is empty", ErrInvalidPreset)
	}
	if len(models) == 0 {
		return fmt.Errorf("%w: no models were provided", ErrInvalidPreset)
	}
	if AutoapiBaseURL(tool, relayAddr) == "" {
		return fmt.Errorf("%w: unsupported tool %q", ErrInvalidPreset, tool)
	}
	return nil
}
