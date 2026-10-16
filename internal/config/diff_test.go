package config

import (
	"slices"
	"testing"
)

func loadData(t *testing.T, project string) *Config {
	t.Helper()
	cfg, err := LoadData(t.TempDir(), "", []byte(project))
	if err != nil {
		t.Fatalf("LoadData: %v", err)
	}
	return cfg
}

func TestDiff(t *testing.T) {
	const base = `allow:
  - prefix: flutter
    description: Flutter
    example: flutter test
  - pattern: 'npm (test|run build)'
    description: npm
    example: npm test
`
	tests := []struct {
		name     string
		old, new string
		want     []string
	}{
		{"same", base, base, nil},
		{"comment and order of keys", base, "# hi\nallow:\n  - description: Flutter\n    prefix: flutter\n    example: flutter test\n  - pattern: 'npm (test|run build)'\n    example: npm test\n    description: npm\n", nil},
		{"added", base, base + "  - prefix: rm\n    description: rm\n    example: rm x\n", []string{"+ prefix rm"}},
		{"removed", base, "allow:\n  - prefix: flutter\n    description: Flutter\n    example: flutter test\n", []string{"- pattern npm (test|run build)"}},
		{"changed pattern", base, "allow:\n  - prefix: flutter\n    description: Flutter\n    example: flutter test\n  - pattern: 'npm .*'\n    description: npm\n    example: npm test\n",
			[]string{"- pattern npm (test|run build)", "+ pattern npm .*"}},
		{"chaining allowed", base, "deny_chaining: false\n" + base, []string{
			"- prefix flutter", "- pattern npm (test|run build)",
			"+ prefix flutter (chaining allowed)", "+ pattern npm (test|run build) (chaining allowed)",
		}},
		{"description", base, "allow:\n  - prefix: flutter\n    description: Flutter SDK\n    example: flutter test\n  - pattern: 'npm (test|run build)'\n    description: npm\n    example: npm test\n",
			[]string{"~ prefix flutter (description or example)"}},
		{"port", base, "port: 47801\n" + base, []string{"~ port 47800 → 47801 (takes effect after a restart)"}},
		{"duplicate added", base, base + "  - prefix: flutter\n    description: again\n    example: flutter run\n", []string{"+ prefix flutter"}},
		{"file removed", base, "", []string{"- prefix flutter", "- pattern npm (test|run build)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Diff(loadData(t, tt.old), loadData(t, tt.new))
			if !slices.Equal(got, tt.want) {
				t.Errorf("Diff:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestChecksumOfLoadedContent(t *testing.T) {
	root, _ := setup(t, "", "allow: []\n")
	cfg := mustLoad(t, root, "")
	if cfg.Checksum != Checksum([]byte("allow: []\n")) {
		t.Errorf("checksum %s is not of the file content", cfg.Checksum)
	}
	if missing := mustLoad(t, t.TempDir(), ""); missing.Checksum != Checksum(nil) {
		t.Errorf("missing file: checksum %s", missing.Checksum)
	}
}
