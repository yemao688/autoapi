package toolconfig

import (
	"fmt"
	"strings"
)

// AutoapiBaseURL returns the endpoint shape expected by each supported client.
// An empty result means relayAddr was empty or the tool is unsupported.
func AutoapiBaseURL(tool Tool, relayAddr string) string {
	addr := strings.TrimRight(strings.TrimSpace(relayAddr), "/")
	if addr == "" {
		return ""
	}
	switch tool {
	case ToolOpencode, ToolCodex:
		return addr + "/v1"
	case ToolClaude:
		return addr
	default:
		return ""
	}
}

// BuildAutoapiPreset builds the relay preset used by an external tool. The
// API key ID is itself the relay token, so the plaintext field is intentionally
// set to the same value and is never decrypted here.
func BuildAutoapiPreset(tool Tool, name, relayAddr, apiKeyID string, models []PresetModel) (PresetPlaintext, error) {
	if err := validateAutoapiBuild(tool, name, relayAddr, apiKeyID, models); err != nil {
		return PresetPlaintext{}, err
	}
	baseURL := AutoapiBaseURL(tool, relayAddr)

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

	vendor := ""
	if tool == ToolOpencode {
		vendor = VendorOpenAICompatible
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
