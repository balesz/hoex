// Package config loads and validates the two-level hoex configuration:
// the user file (~/.config/hoex/config.yaml) and the project file
// (<project>/.hoex.yaml). Rules from both levels add up.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// ProjectFileName is the project-level config file in the project root.
	ProjectFileName = ".hoex.yaml"
	// DefaultPort is used when neither level sets a port.
	DefaultPort = 47800
)

// Config is the effective configuration for one project.
type Config struct {
	// Port is the TCP port the host listens on.
	Port int
	// Rules are the allow rules of both levels, user level first.
	Rules []*Rule
	// ProjectFile is the absolute path of the project config file.
	ProjectFile string
	// Checksum identifies the content of the project file the configuration
	// was loaded from (see Checksum).
	Checksum string
	// Warnings are non-fatal problems, e.g. a rule that allows a shell.
	Warnings []string
}

// Rule allows a command by prefix or by regular expression.
type Rule struct {
	Prefix      string
	Pattern     string
	Description string
	Example     string
	// DenyChaining is the deny_chaining setting of the level that defined the rule.
	DenyChaining bool
	// Source is "file:line" of the rule, for messages.
	Source string

	words []string
	re    *regexp.Regexp
}

// Matches reports whether argv is allowed by the rule.
//
// A prefix matches whole words: "flutter" matches ["flutter", "test"] but not
// ["flutterx"]. A pattern always matches the whole command, the argv elements
// joined by single spaces.
func (r *Rule) Matches(argv []string) bool {
	if r.re != nil {
		return r.re.MatchString(strings.Join(argv, " "))
	}
	if len(argv) < len(r.words) {
		return false
	}
	for i, w := range r.words {
		if argv[i] != w {
			return false
		}
	}
	return true
}

// String returns the rule as written in the config.
func (r *Rule) String() string {
	if r.re != nil {
		return "pattern " + r.Pattern
	}
	return "prefix " + r.Prefix
}

// newRule finishes a parsed rule: compiles the pattern or splits the prefix.
func newRule(r *Rule) error {
	if r.Pattern != "" {
		re, err := regexp.Compile(`^(?:` + r.Pattern + `)$`)
		if err != nil {
			return err
		}
		r.re = re
		return nil
	}
	r.words = strings.Fields(r.Prefix)
	if len(r.words) == 0 {
		return errors.New("prefix is empty")
	}
	return nil
}

// UserFilePath returns the path of the user-level config file:
// $XDG_CONFIG_HOME/hoex/config.yaml, or ~/.config/hoex/config.yaml on every
// platform (also macOS, where os.UserConfigDir would point elsewhere).
func UserFilePath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "hoex", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "hoex", "config.yaml"), nil
}

// Checksum returns the SHA-256 of a project file's content in hex. A missing
// file counts as empty.
func Checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadProjectFile returns the content of the project file at projectRoot,
// nil if it does not exist.
func ReadProjectFile(projectRoot string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, ProjectFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// Load reads the effective configuration for the project at projectRoot.
// Missing files are not an error. userFile may be empty to skip the user level.
// All validation errors are returned together, each as "file:line: message".
func Load(projectRoot, userFile string) (*Config, error) {
	root, err := resolve(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("project root: %w", err)
	}
	data, err := ReadProjectFile(root)
	if err != nil {
		return nil, err
	}
	return LoadData(root, userFile, data)
}

// LoadData is Load with the project file content already read, so that the
// configuration is exactly the content that was checked and shown. nil data
// is a missing project file.
func LoadData(projectRoot, userFile string, project []byte) (*Config, error) {
	root, err := resolve(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("project root: %w", err)
	}
	cfg := &Config{
		Port:        DefaultPort,
		ProjectFile: filepath.Join(root, ProjectFileName),
		Checksum:    Checksum(project),
	}

	var errs []error
	if userFile != "" {
		levels, perr := parseUserFile(userFile)
		errs = append(errs, perr...)
		for _, l := range levels {
			if samePath(l.path, root) {
				cfg.add(l)
			}
		}
	}
	level, perr := parseProjectFile(cfg.ProjectFile, project)
	errs = append(errs, perr...)
	if level != nil {
		cfg.add(*level)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	for _, r := range cfg.Rules {
		cfg.Warnings = append(cfg.Warnings, ruleWarnings(r)...)
	}
	return cfg, nil
}

// add merges one level: rules add up, a later level's port wins.
func (c *Config) add(l level) {
	if l.port != 0 {
		c.Port = l.port
	}
	c.Rules = append(c.Rules, l.rules...)
}

// shells are interpreters that would run arbitrary code if allowed.
var shells = []string{"bash", "sh", "zsh", "dash", "ksh", "fish", "env", "xargs"}

// ruleWarnings flags rules that are likely mistakes or holes.
func ruleWarnings(r *Rule) []string {
	var w []string
	for _, sh := range shells {
		if r.Matches([]string{sh, "-c", "x"}) {
			w = append(w, fmt.Sprintf("%s: %s allows %q, which can run any command", r.Source, r, sh+" -c"))
			break
		}
	}
	if !r.Matches(strings.Fields(r.Example)) {
		w = append(w, fmt.Sprintf("%s: example %q is not allowed by its own rule", r.Source, r.Example))
	}
	return w
}

func resolve(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return filepath.Clean(abs), nil
}

func samePath(a, b string) bool {
	ra, err := resolve(a)
	return err == nil && ra == b
}
