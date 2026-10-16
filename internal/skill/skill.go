// Package skill generates the agent skill that tells the agent in the
// sandbox which commands to run on the host through hoex, and installs it
// into the project. Skills are written in English; rule descriptions are
// copied as the user wrote them.
package skill

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"

	"github.com/balesz/hoex/internal/config"
	"github.com/balesz/hoex/internal/projfs"
)

//go:embed templates
var templates embed.FS

// agent knows where and how one coding agent takes a skill.
type agent struct {
	// skillFile is the skill's path relative to the project root.
	skillFile string
	template  string
	// install does the agent-specific setup besides the skill file and
	// returns the files it changed.
	install func(root string) ([]string, error)
}

var agents = map[string]agent{
	"claude": {
		skillFile: filepath.Join(".claude", "skills", "hoex", "SKILL.md"),
		template:  "claude.md.tmpl",
		install:   claudeSettings,
	},
}

// Supported returns the agent names Install accepts.
func Supported() []string {
	names := make([]string, 0, len(agents))
	for n := range agents {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Install writes the skill for the named agent into the project at root and
// does the agent's setup. It returns the files written.
func Install(name, root string, cfg *config.Config) ([]string, error) {
	a, ok := agents[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q; supported: %s", name, strings.Join(Supported(), ", "))
	}
	if err := a.write(root, cfg); err != nil {
		return nil, err
	}
	changed, err := a.install(root)
	return append([]string{a.skillFile}, changed...), err
}

// Update regenerates the skills already installed in the project at root and
// returns the files written. A project without skills is left alone.
func Update(root string, cfg *config.Config) ([]string, error) {
	var written []string
	for _, name := range Supported() {
		a := agents[name]
		if _, err := os.Lstat(filepath.Join(root, a.skillFile)); err != nil {
			continue
		}
		if err := a.write(root, cfg); err != nil {
			return written, err
		}
		written = append(written, a.skillFile)
	}
	return written, nil
}

// Generate returns the skill for the named agent.
func Generate(name string, cfg *config.Config) ([]byte, error) {
	a, ok := agents[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", name)
	}
	return a.generate(cfg)
}

func (a agent) write(root string, cfg *config.Config) error {
	data, err := a.generate(cfg)
	if err != nil {
		return err
	}
	return writeFile(root, a.skillFile, data)
}

func (a agent) generate(cfg *config.Config) ([]byte, error) {
	t, err := template.ParseFS(templates, "templates/"+a.template)
	if err != nil {
		return nil, err
	}
	d, err := newData(cfg)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// data is what the templates see.
type data struct {
	Frontmatter  string
	Rules        []rule
	PipeExample  string
	DenyChaining bool // some rule rejects chaining marks
}

type rule struct {
	Title, Description, Allows, Example string
	ChainingAllowed                     bool
}

func newData(cfg *config.Config) (*data, error) {
	d := &data{PipeExample: "<command>"}
	var commands []string
	for _, r := range cfg.Rules {
		v := rule{Description: r.Description, Example: r.Example, ChainingAllowed: !r.DenyChaining}
		if r.Prefix != "" {
			prefix := strings.Join(strings.Fields(r.Prefix), " ")
			v.Title = "`" + prefix + "`"
			v.Allows = fmt.Sprintf("`%s` with any arguments", prefix)
			commands = append(commands, "`"+prefix+"`")
		} else {
			v.Title = "`" + r.Example + "`"
			v.Allows = fmt.Sprintf("commands matching the regular expression `^(?:%s)$` (arguments joined by single spaces)", r.Pattern)
			commands = append(commands, "`"+r.Example+"`")
		}
		d.DenyChaining = d.DenyChaining || r.DenyChaining
		d.Rules = append(d.Rules, v)
	}
	if len(cfg.Rules) > 0 {
		d.PipeExample = cfg.Rules[0].Example
	}
	commands = slices.Compact(commands)

	desc := "Runs commands on the host machine from the sandbox through hoex. No commands are allowed yet."
	if len(commands) > 0 {
		desc = fmt.Sprintf("Use this to run %s commands: they must run on the host through hoex, never directly in the sandbox.", list(commands))
	}
	fm, err := yaml.Marshal(struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}{"hoex", desc})
	if err != nil {
		return nil, err
	}
	d.Frontmatter = string(fm)
	return d, nil
}

// list joins items as "a", "a and b" or "a, b and c".
func list(items []string) string {
	if len(items) <= 2 {
		return strings.Join(items, " and ")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// writeFile writes rel inside root atomically, never through a link to
// outside the project: the sandbox could replace .claude with one.
func writeFile(root, rel string, data []byte) error {
	return projfs.WriteFile(root, rel, data, 0o644)
}

// readFile reads rel inside root; a missing file is nil without error.
func readFile(root, rel string) ([]byte, error) {
	return projfs.ReadFile(root, rel)
}
