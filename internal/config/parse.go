package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// level is the parsed content of one config level for one project.
type level struct {
	path  string // project path (user level only)
	port  int
	rules []*Rule
}

// parser walks a yaml.Node tree and collects errors with line numbers.
type parser struct {
	file string
	errs []error
}

func (p *parser) errorf(n *yaml.Node, format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf("%s:%d: %s", p.file, n.Line, fmt.Sprintf(format, args...)))
}

// readFile parses a YAML file into its document node. A missing file or an
// empty document yields nil.
func (p *parser) readFile() *yaml.Node {
	data, err := os.ReadFile(p.file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		p.errs = append(p.errs, err)
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// yaml errors already carry "line N".
		p.errs = append(p.errs, fmt.Errorf("%s: %w", p.file, err))
		return nil
	}
	if len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

// fields checks that n is a mapping with only the allowed keys and returns
// key → value node.
func (p *parser) fields(n *yaml.Node, what string, allowed ...string) map[string]*yaml.Node {
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a mapping", what)
		return nil
	}
	out := make(map[string]*yaml.Node)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !slices.Contains(allowed, k.Value) {
			p.errorf(k, "unknown key %q in %s", k.Value, what)
			continue
		}
		if _, dup := out[k.Value]; dup {
			p.errorf(k, "duplicate key %q", k.Value)
			continue
		}
		out[k.Value] = v
	}
	return out
}

func (p *parser) str(n *yaml.Node, key string) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		p.errorf(n, "%s must be a string", key)
		return ""
	}
	return n.Value
}

func (p *parser) boolean(n *yaml.Node, key string) bool {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
		p.errorf(n, "%s must be true or false", key)
		return false
	}
	b, _ := strconv.ParseBool(n.Value)
	return b
}

func (p *parser) port(n *yaml.Node) int {
	v, err := strconv.Atoi(n.Value)
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || err != nil || v < 1 || v > 65535 {
		p.errorf(n, "port must be an integer between 1 and 65535")
		return 0
	}
	return v
}

// level parses the keys shared by the project file and a user-level project
// entry: port, deny_chaining, allow.
func (p *parser) level(f map[string]*yaml.Node) level {
	var l level
	if n, ok := f["port"]; ok {
		l.port = p.port(n)
	}
	denyChaining := true
	if n, ok := f["deny_chaining"]; ok {
		denyChaining = p.boolean(n, "deny_chaining")
	}
	allow, ok := f["allow"]
	if !ok {
		return l
	}
	if allow.Kind != yaml.SequenceNode {
		p.errorf(allow, "allow must be a list")
		return l
	}
	for _, rn := range allow.Content {
		if r := p.rule(rn, denyChaining); r != nil {
			l.rules = append(l.rules, r)
		}
	}
	return l
}

func (p *parser) rule(n *yaml.Node, denyChaining bool) *Rule {
	f := p.fields(n, "rule", "prefix", "pattern", "description", "example")
	if f == nil {
		return nil
	}
	before := len(p.errs)
	r := &Rule{DenyChaining: denyChaining, Source: fmt.Sprintf("%s:%d", p.file, n.Line)}

	prefix, hasPrefix := f["prefix"]
	pattern, hasPattern := f["pattern"]
	switch {
	case hasPrefix && hasPattern:
		p.errorf(n, "rule must have either prefix or pattern, not both")
	case hasPrefix:
		r.Prefix = p.str(prefix, "prefix")
	case hasPattern:
		r.Pattern = p.str(pattern, "pattern")
		if r.Pattern == "" {
			p.errorf(pattern, "pattern is empty")
		}
	default:
		p.errorf(n, "rule must have a prefix or a pattern")
	}
	for _, key := range []string{"description", "example"} {
		v, ok := f[key]
		if !ok {
			p.errorf(n, "rule is missing %s", key)
			continue
		}
		if s := p.str(v, key); s == "" {
			p.errorf(v, "%s is empty", key)
		} else if key == "description" {
			r.Description = s
		} else {
			r.Example = s
		}
	}
	if len(p.errs) > before {
		return nil
	}
	if err := newRule(r); err != nil {
		at := prefix
		if hasPattern {
			at = pattern
		}
		p.errorf(at, "%v", err)
		return nil
	}
	return r
}

// parseProjectFile parses <project>/.hoex.yaml.
func parseProjectFile(file string) (*level, []error) {
	p := &parser{file: file}
	root := p.readFile()
	if root == nil {
		return nil, p.errs
	}
	f := p.fields(root, "project config", "port", "deny_chaining", "allow")
	if f == nil {
		return nil, p.errs
	}
	l := p.level(f)
	return &l, p.errs
}

// parseUserFile parses the user-level file into one level per project entry.
func parseUserFile(file string) ([]level, []error) {
	p := &parser{file: file}
	root := p.readFile()
	if root == nil {
		return nil, p.errs
	}
	f := p.fields(root, "user config", "projects")
	projects, ok := f["projects"]
	if !ok {
		if f != nil {
			p.errorf(root, "missing projects list")
		}
		return nil, p.errs
	}
	if projects.Kind != yaml.SequenceNode {
		p.errorf(projects, "projects must be a list")
		return nil, p.errs
	}
	var levels []level
	for _, pn := range projects.Content {
		pf := p.fields(pn, "project entry", "path", "port", "deny_chaining", "allow")
		if pf == nil {
			continue
		}
		pathNode, ok := pf["path"]
		if !ok {
			p.errorf(pn, "project entry is missing path")
			continue
		}
		path := p.str(pathNode, "path")
		if path != "" && !filepath.IsAbs(path) {
			p.errorf(pathNode, "path must be absolute: %s", path)
			continue
		}
		l := p.level(pf)
		l.path = path
		levels = append(levels, l)
	}
	return levels, p.errs
}
