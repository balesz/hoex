package skill

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/balesz/hoex/internal/config"
)

// claudeSettingsFile is the shared project settings file of Claude Code.
var claudeSettingsFile = filepath.Join(".claude", "settings.json")

// claudeDeny keeps Claude Code's file tools and the file commands it
// recognizes in Bash away from the project config. A leading "/" anchors the
// path at the project root in project settings; Edit rules cover every tool
// that writes files.
var claudeDeny = []string{
	"Read(/" + config.ProjectFileName + ")",
	"Edit(/" + config.ProjectFileName + ")",
}

// claudeSettings adds the deny rules to .claude/settings.json, keeping
// everything else in it. It returns the file if it changed it.
func claudeSettings(root string) ([]string, error) {
	data, err := readFile(root, claudeSettingsFile)
	if err != nil {
		return nil, err
	}
	out, changed, err := mergeDeny(data, claudeDeny)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", claudeSettingsFile, err)
	}
	if !changed {
		return nil, nil
	}
	if err := writeFile(root, claudeSettingsFile, out); err != nil {
		return nil, err
	}
	return []string{claudeSettingsFile}, nil
}

// mergeDeny adds the rules missing from permissions.deny of a settings
// document. Without a change it returns the input unchanged. Keys are
// written sorted, which may reorder an existing file.
func mergeDeny(data []byte, rules []string) ([]byte, bool, error) {
	settings := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, false, fmt.Errorf("not valid JSON: %w", err)
		}
		if settings == nil {
			settings = map[string]any{}
		}
	}
	perms := map[string]any{}
	if p, ok := settings["permissions"]; ok {
		if perms, ok = p.(map[string]any); !ok {
			return nil, false, fmt.Errorf("permissions is not an object")
		}
	}
	var deny []any
	if d, ok := perms["deny"]; ok {
		if deny, ok = d.([]any); !ok {
			return nil, false, fmt.Errorf("permissions.deny is not a list")
		}
	}
	changed := false
	for _, r := range rules {
		if !slices.Contains(deny, any(r)) {
			deny = append(deny, r)
			changed = true
		}
	}
	if !changed {
		return data, false, nil
	}
	perms["deny"] = deny
	settings["permissions"] = perms

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return nil, false, err
	}
	return b.Bytes(), true, nil
}
