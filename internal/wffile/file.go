// Package wffile defines the human-readable YAML file pdwf exports and imports,
// and converts between it and the PagerDuty API shapes.
//
// The file deliberately holds no account-specific IDs where a name will do:
// teams and services are written by name and resolved by name on import, so a
// file exported from one PagerDuty account can be imported into another.
package wffile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const Format = "pdwf/v1"

type File struct {
	Format     string    `yaml:"format"`
	ExportedAt string    `yaml:"exported_at,omitempty"`
	Source     *Source   `yaml:"source,omitempty"`
	Workflow   Workflow  `yaml:"workflow"`
	Triggers   []Trigger `yaml:"triggers,omitempty"`
}

// Source is informational only; import ignores it.
type Source struct {
	Account    string `yaml:"account,omitempty"`
	Region     string `yaml:"region,omitempty"`
	WorkflowID string `yaml:"workflow_id,omitempty"`
	URL        string `yaml:"url,omitempty"`
}

type Workflow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Team is a team name (or ID). Empty means not team-scoped.
	Team    string `yaml:"team,omitempty"`
	Enabled *bool  `yaml:"enabled,omitempty"`
	Steps   []Step `yaml:"steps"`
}

type Step struct {
	Name              string         `yaml:"name"`
	Description       string         `yaml:"description,omitempty"`
	Action            string         `yaml:"action"`
	ActionDescription string         `yaml:"action_description,omitempty"`
	Inputs            OrderedMap     `yaml:"inputs,omitempty"`
	InlineSteps       InlineStepsMap `yaml:"inline_steps,omitempty"`
}

type Trigger struct {
	// Type is manual, conditional or incident_type.
	Type      string `yaml:"type"`
	Condition string `yaml:"condition,omitempty"`
	// IncidentTypes are incident type names, for type: incident_type.
	IncidentTypes []string `yaml:"incident_types,omitempty"`
	Disabled      bool     `yaml:"disabled,omitempty"`
	// AllServices subscribes the trigger to every service.
	AllServices bool `yaml:"all_services,omitempty"`
	// Services are service names (or IDs).
	Services []string `yaml:"services,omitempty"`
}

// OrderedMap is a YAML mapping that keeps key order, used for step inputs.
type OrderedMap []KV

type KV struct {
	Key   string
	Value any
}

func (m OrderedMap) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, kv := range m {
		var v yaml.Node
		if err := v.Encode(kv.Value); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: kv.Key}, &v)
	}
	return n, nil
}

func (m *OrderedMap) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: inputs must be a list of \"name: value\" lines", n.Line)
	}
	out := OrderedMap{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		if seen[k] {
			return fmt.Errorf("line %d: input %q appears twice", n.Content[i].Line, k)
		}
		seen[k] = true
		var v any
		if err := n.Content[i+1].Decode(&v); err != nil {
			return err
		}
		out = append(out, KV{k, v})
	}
	*m = out
	return nil
}

// InlineStepsMap holds nested step lists (branches, loops) keyed by input name.
type InlineStepsMap []InlineBlock

type InlineBlock struct {
	Name  string
	Steps []Step
}

func (m InlineStepsMap) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, b := range m {
		var v yaml.Node
		if err := v.Encode(b.Steps); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: b.Name}, &v)
	}
	return n, nil
}

func (m *InlineStepsMap) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: inline_steps must map a name to a list of steps", n.Line)
	}
	out := InlineStepsMap{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		// node.Decode does not inherit the decoder's KnownFields(true), so
		// check nested step keys here or typos would be silently dropped.
		if err := checkStepKeys(n.Content[i+1]); err != nil {
			return err
		}
		var steps []Step
		if err := n.Content[i+1].Decode(&steps); err != nil {
			return err
		}
		out = append(out, InlineBlock{n.Content[i].Value, steps})
	}
	*m = out
	return nil
}

var stepKeys = map[string]bool{"name": true, "description": true, "action": true, "action_description": true, "inputs": true, "inline_steps": true}

func checkStepKeys(seq *yaml.Node) error {
	if seq.Kind != yaml.SequenceNode {
		return nil // Decode reports the type error with a line number
	}
	for _, step := range seq.Content {
		if step.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(step.Content); j += 2 {
			if k := step.Content[j]; !stepKeys[k.Value] {
				return fmt.Errorf("line %d: field %s not found in type wffile.Step (check the spelling and indentation of that key)", k.Line, k.Value)
			}
		}
	}
	return nil
}

const header = `# PagerDuty incident workflow, exported by pdwf.
#
# This file is safe to edit by hand. Step inputs are copied exactly, so
# review it before sharing or committing: HTTP, webhook or script steps can
# contain URLs, tokens or other secrets.
# Teams and services are referenced by NAME, so it can be imported into a
# different PagerDuty account as long as teams/services with the same names exist.
#
#   Check it:   pdwf validate %s
#   Import it:  pdwf import %s   (asks for the target account's key, shows a plan first)
#
`

// Marshal renders a file with a short explanatory header.
func Marshal(f *File, filename string) ([]byte, error) {
	var buf bytes.Buffer
	name := filepath.Base(filename)
	fmt.Fprintf(&buf, header, name, name)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	raw := buf.Bytes()
	pretty := unescapeUnicode(raw)
	if !bytes.Equal(pretty, raw) && !sameContent(raw, pretty) {
		// The readable form would change a value (e.g. a value that literally
		// contains "\U0001F510"), so keep the escaped, exact form.
		return raw, nil
	}
	return pretty, nil
}

// sameContent reports whether two renderings decode to identical data.
func sameContent(a, b []byte) bool {
	var x, y any
	if yaml.Unmarshal(a, &x) != nil || yaml.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Load reads and strictly parses a file: unknown keys (typos) are errors.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s is not a valid pdwf file: %s", path, cleanYAMLErr(err))
	}
	return &f, nil
}

func cleanYAMLErr(err error) string {
	s := strings.TrimPrefix(err.Error(), "yaml: ")
	s = strings.ReplaceAll(s, "unmarshal errors:\n  ", "")
	if strings.Contains(s, "not found in type") {
		s += " (check the spelling and indentation of that key)"
	}
	return s
}

var triggerTypes = map[string]bool{"manual": true, "conditional": true, "incident_type": true}

// Validate returns every problem found, so the user can fix them all at once.
func Validate(f *File) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if f.Format != Format {
		if f.Format == "" {
			add("missing \"format: %s\" line at the top (is this a pdwf export?)", Format)
		} else {
			add("format is %q, this version of pdwf reads %q", f.Format, Format)
		}
	}
	if strings.TrimSpace(f.Workflow.Name) == "" {
		add("workflow.name is empty")
	}
	validateSteps(f.Workflow.Steps, "workflow.steps", add)

	for i, t := range f.Triggers {
		where := fmt.Sprintf("triggers[%d]", i+1)
		if !triggerTypes[t.Type] {
			add("%s: type %q is not one of manual, conditional, incident_type", where, t.Type)
		}
		if t.Type == "conditional" && strings.TrimSpace(t.Condition) == "" {
			add("%s: conditional trigger needs a condition", where)
		}
		if t.Type == "incident_type" && len(t.IncidentTypes) == 0 {
			add("%s: incident_type trigger needs an incident_types list", where)
		}
		if t.AllServices && len(t.Services) > 0 {
			add("%s: set either all_services: true or a services list, not both", where)
		}
	}
	return errs
}

func validateSteps(steps []Step, where string, add func(string, ...any)) {
	for i, s := range steps {
		w := fmt.Sprintf("%s[%d] (%q)", where, i+1, s.Name)
		if strings.TrimSpace(s.Name) == "" {
			add("%s: step has no name", w)
		}
		if !strings.Contains(s.Action, ":") {
			add("%s: action %q does not look like a PagerDuty action ID (e.g. pagerduty.com:incident-workflows:send-status-update:1)", w, s.Action)
		}
		for _, b := range s.InlineSteps {
			validateSteps(b.Steps, w+".inline_steps."+b.Name, add)
		}
	}
}

var idLike = regexp.MustCompile(`^[PQ][A-Z0-9]{6,13}$|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Warnings flags input values that look like account-specific PagerDuty IDs,
// which will usually not exist in a different account.
func Warnings(f *File) []string {
	var out []string
	if len(f.Workflow.Steps) == 0 {
		out = append(out, "workflow has no steps (it will be imported empty)")
	}
	var walk func([]Step, string)
	walk = func(steps []Step, where string) {
		for _, s := range steps {
			for _, kv := range s.Inputs {
				if str, ok := kv.Value.(string); ok && idLike.MatchString(str) {
					out = append(out, fmt.Sprintf("step %q input %q is %q, which looks like an ID from the source account. If you are importing into a different account, change it to the matching ID there.", where+s.Name, kv.Key, str))
				}
			}
			for _, b := range s.InlineSteps {
				walk(b.Steps, where+s.Name+" > ")
			}
		}
	}
	walk(f.Workflow.Steps, "")
	return out
}

// ActionIDs lists every action a file uses, with the step names using each.
func ActionIDs(f *File) map[string][]string {
	out := map[string][]string{}
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, s := range steps {
			out[s.Action] = append(out[s.Action], s.Name)
			for _, b := range s.InlineSteps {
				walk(b.Steps)
			}
		}
	}
	walk(f.Workflow.Steps)
	return out
}

// JoinErrors formats a list of validation errors as an indented list.
func JoinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	lines := make([]string, len(errs))
	for i, e := range errs {
		lines[i] = "  - " + e.Error()
	}
	return errors.New(strings.Join(lines, "\n"))
}

var yamlUnicodeEscape = regexp.MustCompile(`\\U[0-9A-Fa-f]{8}|\\u[0-9A-Fa-f]{4}`)

// unescapeUnicode turns the \U0001F510-style escapes yaml.v3 writes for emoji
// back into the characters themselves. Both are valid inside double-quoted
// YAML strings, and the literal character is what a person wants to read.
func unescapeUnicode(b []byte) []byte {
	return yamlUnicodeEscape.ReplaceAllFunc(b, func(m []byte) []byte {
		n, err := strconv.ParseUint(string(m[2:]), 16, 32)
		r := rune(n)
		if err != nil || !utf8.ValidRune(r) || !unicode.IsPrint(r) || (r >= 0xD800 && r <= 0xDFFF) {
			return m
		}
		return []byte(string(r))
	})
}
