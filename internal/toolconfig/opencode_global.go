package toolconfig

import (
	"fmt"

	"github.com/tailscale/hujson"
)

// OpencodeGlobalSettings is the curated set of top-level opencode config keys
// this app manages. Empty string / nil means "remove the key / leave unset".
// A non-boolean existing autoupdate value is treated as unset by the read
// helper; an explicit preview/apply can replace it with a boolean or remove it.
type OpencodeGlobalSettings struct {
	Model      string
	SmallModel string
	Theme      string
	Share      string
	Autoupdate *bool
}

// ReadOpencodeGlobalSettings reads the five curated top-level settings. A
// missing file or key produces a zero value. Invalid scalar values are treated
// as unset; the planner remains able to overwrite an owned value safely.
func ReadOpencodeGlobalSettings(homeDir string) (OpencodeGlobalSettings, error) {
	doc, err := readJSONDocument(resolvedOpenCodePath(homeDir))
	if err != nil {
		return OpencodeGlobalSettings{}, err
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return OpencodeGlobalSettings{}, fmt.Errorf("parse opencode config: %w", err)
	}
	if err := requireUniqueObjectMembers(root); err != nil {
		return OpencodeGlobalSettings{}, err
	}
	if err := requireUniqueKeys(root, "model", "small_model", "theme", "share", "autoupdate"); err != nil {
		return OpencodeGlobalSettings{}, err
	}

	settings := OpencodeGlobalSettings{}
	for name, target := range map[string]*string{
		"model":       &settings.Model,
		"small_model": &settings.SmallModel,
		"theme":       &settings.Theme,
		"share":       &settings.Share,
	} {
		value := objectMemberValue(root, name)
		if value == nil {
			continue
		}
		literal, ok := value.Value.(hujson.Literal)
		if ok && literal.Kind() == hujson.Kind('"') {
			*target = literal.String()
		}
	}
	if value := objectMemberValue(root, "autoupdate"); value != nil {
		if literal, ok := value.Value.(hujson.Literal); ok {
			switch literal.Kind() {
			case hujson.Kind('t'):
				value := true
				settings.Autoupdate = &value
			case hujson.Kind('f'):
				value := false
				settings.Autoupdate = &value
			}
		}
	}
	return settings, nil
}

// PlanOpencodeGlobalChange renders only the five curated top-level leaves.
// Existing root comments and all other keys remain in the hujson document.
func PlanOpencodeGlobalChange(homeDir string, settings OpencodeGlobalSettings) (*ChangeSet, error) {
	if settings.Share != "" && settings.Share != "manual" && settings.Share != "auto" && settings.Share != "disabled" {
		return nil, fmt.Errorf("%w: invalid opencode share value %q", ErrInvalidPreset, settings.Share)
	}
	configPath, before, err := snapshotFile(resolvedOpenCodePath(homeDir), absoluteHomeDir(homeDir))
	if err != nil {
		return nil, err
	}
	doc, err := parseJSONBytes(before)
	if err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	if err := requireUniqueObjectMembers(root); err != nil {
		return nil, err
	}
	if err := requireUniqueKeys(root, "model", "small_model", "theme", "share", "autoupdate"); err != nil {
		return nil, err
	}

	for _, field := range []struct {
		name  string
		value string
	}{
		{"model", settings.Model},
		{"small_model", settings.SmallModel},
		{"theme", settings.Theme},
		{"share", settings.Share},
	} {
		if field.value == "" {
			if err := removeObjectMember(root, field.name); err != nil {
				return nil, err
			}
			continue
		}
		value, err := jsonValue(field.value)
		if err != nil {
			return nil, err
		}
		if err := setObjectMember(root, field.name, value); err != nil {
			return nil, err
		}
	}
	if settings.Autoupdate == nil {
		if err := removeObjectMember(root, "autoupdate"); err != nil {
			return nil, err
		}
	} else if value, err := jsonValue(*settings.Autoupdate); err != nil {
		return nil, err
	} else if err := setObjectMember(root, "autoupdate", value); err != nil {
		return nil, err
	}

	change := changeForSnapshot(ResOpencodeConfig, configPath, false, before)
	change.After = doc.Pack()
	return &ChangeSet{
		Tool:    ToolOpencode,
		Changes: []FileChange{change},
	}, nil
}
