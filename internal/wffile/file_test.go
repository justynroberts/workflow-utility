package wffile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// roundTrip marshals f, writes it, and loads it back.
func roundTrip(t *testing.T, f *File) (*File, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sub", "wf.yaml")
	data, err := Marshal(f, p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load after Marshal: %v\n%s", err, data)
	}
	return got, string(data)
}

func richFile() *File {
	return &File{
		Format:     Format,
		ExportedAt: "2026-11-19T12:00:00Z",
		Source:     &Source{Account: "acme.pagerduty.com", Region: "us", WorkflowID: "PWF0001", URL: "https://acme.pagerduty.com/x"},
		Workflow: Workflow{
			Name:        "🔐 Security: \"Major\" #1",
			Description: "  leading spaces, a colon: here, and # not a comment",
			Team:        "SRE",
			Enabled:     boolp(true),
			Steps: []Step{
				{
					Name:   "Post update 💡",
					Action: "pagerduty.com:incident-workflows:send-status-update:1",
					Inputs: OrderedMap{
						{"Zeta", "first"},
						{"Alpha", "second"},
						{"Message", "line one\nline two\n  indented"},
						{"EmojiLines", "first 🔐\nsecond"},
						{"Quotes", `she said "hi" and 'bye'`},
						{"Colon", "key: value # hash"},
						{"Leading", "   spaced"},
						{"Int", 42},
						{"Float", 1.5},
						{"Bool", false},
						{"List", []any{"a", 1, true}},
						{"Map", map[string]any{"k": "v"}},
						{"Empty", ""},
						{"Middle", "m"},
					},
				},
				{
					Name:   "Loop",
					Action: "pagerduty.com:logic:incident-workflows-loop-until:3",
					Inputs: OrderedMap{{"Condition", "incident.status matches 'resolved'"}},
					InlineSteps: InlineStepsMap{
						{Name: "Actions", Steps: []Step{{
							Name:   "Branch",
							Action: "pagerduty.com:logic:if-else:1",
							InlineSteps: InlineStepsMap{
								{Name: "Then", Steps: []Step{{
									Name:   "Deeper",
									Action: "pagerduty.com:logic:if-else:1",
									InlineSteps: InlineStepsMap{
										{Name: "Else", Steps: []Step{{Name: "Leaf", Action: "pagerduty.com:incident-workflows:add-note:1", Inputs: OrderedMap{{"Note", "deep"}}}}},
									},
								}}},
								{Name: "Else", Steps: []Step{{Name: "Other", Action: "pagerduty.com:incident-workflows:add-note:1"}}},
							},
						}}},
					},
				},
			},
		},
		Triggers: []Trigger{
			{Type: "manual", AllServices: true},
			{Type: "conditional", Condition: "incident.priority matches 'P1'", Services: []string{"Checkout API", "Payments"}},
			{Type: "incident_type", IncidentTypes: []string{"security_incident", "major_default"}, AllServices: true, Disabled: true},
		},
	}
}

func TestMarshalLoadRoundTrip(t *testing.T) {
	f := richFile()
	got, out := roundTrip(t, f)

	// yaml decodes ints as int, floats as float64, so compare against the same.
	if !reflect.DeepEqual(got, f) {
		t.Errorf("round trip changed the file\nwant %#v\ngot  %#v\n%s", f, got, out)
	}
	// Input order preserved.
	var keys []string
	for _, kv := range got.Workflow.Steps[0].Inputs {
		keys = append(keys, kv.Key)
	}
	want := []string{"Zeta", "Alpha", "Message", "EmojiLines", "Quotes", "Colon", "Leading", "Int", "Float", "Bool", "List", "Map", "Empty", "Middle"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("input order = %v, want %v", keys, want)
	}
	if strings.Contains(out, `\U`) || strings.Contains(out, `\u`) {
		t.Errorf("output still contains unicode escapes:\n%s", out)
	}
	for _, s := range []string{"🔐 Security", "Post update 💡"} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
}

func TestMarshalEnabledVariants(t *testing.T) {
	for _, en := range []*bool{boolp(true), boolp(false), nil} {
		f := &File{Format: Format, Workflow: Workflow{Name: "x", Enabled: en, Steps: []Step{{Name: "s", Action: "a:b"}}}}
		got, out := roundTrip(t, f)
		if !reflect.DeepEqual(got.Workflow.Enabled, en) {
			t.Errorf("enabled %v -> %v", en, got.Workflow.Enabled)
		}
		if en == nil && strings.Contains(out, "enabled:") {
			t.Errorf("nil enabled should be omitted:\n%s", out)
		}
	}
}

func TestMarshalHeaderUsesBaseName(t *testing.T) {
	data, err := Marshal(richFile(), "/some/very/long/path/my-flow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	head := strings.SplitN(string(data), "format:", 2)[0]
	if strings.Contains(head, "/some/very") {
		t.Errorf("header contains the full path:\n%s", head)
	}
	if strings.Count(head, "my-flow.yaml") != 2 {
		t.Errorf("header should name the file twice:\n%s", head)
	}
}

func TestMarshalHeaderMatchesCLI(t *testing.T) {
	data, _ := Marshal(richFile(), "x.yaml")
	if strings.Contains(string(data), "--env") || !strings.Contains(string(data), "pdwf import x.yaml") {
		t.Errorf("header must show a working import command:\n%s", data)
	}
}

func TestUnescapeUnicode(t *testing.T) {
	cases := map[string]string{
		`"\U0001F510 lock"`:  `"🔐 lock"`,
		`"\u00e9t\u00e9"`:    `"été"`,
		`"\uD83D"`:           `"\uD83D"`, // surrogate half stays escaped
		`"\u0007"`:           `"\u0007"`, // non-printable stays escaped
		`"\U0011FFFF"`:       `"\U0011FFFF"`,
		`"\u12G4 \U1234567"`: `"\u12G4 \U1234567"`, // not valid hex / too short
		`plain text, no esc`: `plain text, no esc`,
	}
	for in, want := range cases {
		if got := string(unescapeUnicode([]byte(in))); got != want {
			t.Errorf("unescapeUnicode(%s) = %s, want %s", in, got, want)
		}
	}
}

// A value that literally contains the characters \U0001F510 must survive.
func TestLiteralBackslashUSurvives(t *testing.T) {
	for name, v := range map[string]string{
		"plain":        `C:\Users\U0001F510`,
		"block":        "line\n" + `literal \U0001F510 text` + "\nend",
		"double-quote": "bell\a and " + `\U0001F510`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &File{Format: Format, Workflow: Workflow{Name: "x", Steps: []Step{{Name: "s", Action: "a:b", Inputs: OrderedMap{{"V", v}}}}}}
			p := filepath.Join(t.TempDir(), "x.yaml")
			data, err := Marshal(f, p)
			if err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(p, data, 0o644)
			got, err := Load(p)
			if err != nil || got.Workflow.Steps[0].Inputs[0].Value != v {
				var gv any
				if got != nil {
					gv = got.Workflow.Steps[0].Inputs[0].Value
				}
				t.Errorf("value %q came back as %q (err %v)", v, gv, err)
			}
		})
	}
}

func TestLoadStrictness(t *testing.T) {
	base := "format: pdwf/v1\nworkflow:\n  name: X\n  steps:\n    - name: s\n      action: a:b\n"
	cases := map[string]struct{ content, want string }{
		"top-level typo":  {base + "trigers: []\n", "trigers"},
		"workflow typo":   {"format: pdwf/v1\nworkflow:\n  name: X\n  stepz: []\n", "stepz"},
		"step typo":       {"format: pdwf/v1\nworkflow:\n  name: X\n  steps:\n    - name: s\n      actoin: a:b\n", "actoin"},
		"trigger typo":    {base + "triggers:\n  - type: manual\n    al_services: true\n", "al_services"},
		"duplicate input": {"format: pdwf/v1\nworkflow:\n  name: X\n  steps:\n    - name: s\n      action: a:b\n      inputs:\n        A: 1\n        A: 2\n", `"A"`},
		"inputs not map":  {"format: pdwf/v1\nworkflow:\n  name: X\n  steps:\n    - name: s\n      action: a:b\n      inputs: [a, b]\n", "inputs must be"},
		"inline not map":  {"format: pdwf/v1\nworkflow:\n  name: X\n  steps:\n    - name: s\n      action: a:b\n      inline_steps: [a]\n", "inline_steps must"},
		"not yaml at all": {"\t: : [\n", "not a valid pdwf file"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "f.yaml", c.content))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want error containing %q, got %v", c.want, err)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: got %v", err)
	}
}

// Typos inside nested (inline) steps must be caught like top-level ones.
func TestLoadStrictnessNested(t *testing.T) {
	content := "format: pdwf/v1\nworkflow:\n  name: X\n  steps:\n    - name: s\n      action: a:b\n      inline_steps:\n        Then:\n          - name: inner\n            action: c:d\n            descripton: lost\n"
	_, err := Load(writeTemp(t, "f.yaml", content))
	if err == nil {
		t.Fatal("typo inside inline_steps was silently accepted")
	}
	if !strings.Contains(err.Error(), "descripton") {
		t.Errorf("want error naming the key, got %v", err)
	}
}

// Multi-line text is written as a readable block, and emoji survive.
func TestMultilineAndEmojiStyle(t *testing.T) {
	_, out := roundTrip(t, richFile())
	if !strings.Contains(out, "Message: |-\n") {
		t.Errorf("multi-line value should be a literal block:\n%s", out)
	}
	if strings.Contains(out, `EmojiLines: "first 🔐\nsecond"`) {
		t.Log("note: yaml.v3 writes multi-line values containing emoji as a double-quoted string with \\n (readable, but not a block)")
	}
}

func TestLoadTypoHint(t *testing.T) {
	_, err := Load(writeTemp(t, "f.yaml", "format: pdwf/v1\nworkflow:\n  nme: X\n"))
	if err == nil || !strings.Contains(err.Error(), "check the spelling") {
		t.Errorf("want spelling hint, got %v", err)
	}
}

func validFile() *File {
	return &File{Format: Format, Workflow: Workflow{Name: "X", Steps: []Step{{Name: "s", Action: "a:b:c:1"}}}}
}

func errStrings(errs []error) string {
	var s []string
	for _, e := range errs {
		s = append(s, e.Error())
	}
	return strings.Join(s, "\n")
}

func TestValidateRules(t *testing.T) {
	if errs := Validate(validFile()); len(errs) != 0 {
		t.Fatalf("valid file has errors: %v", errs)
	}
	empty := validFile()
	empty.Workflow.Steps = nil
	if errs := Validate(empty); len(errs) != 0 {
		t.Errorf("empty steps must not be an error: %v", errs)
	}

	cases := map[string]struct {
		mod  func(*File)
		want string
	}{
		"format missing":          {func(f *File) { f.Format = "" }, `missing "format: pdwf/v1"`},
		"format wrong":            {func(f *File) { f.Format = "pdwf/v2" }, `format is "pdwf/v2"`},
		"name empty":              {func(f *File) { f.Workflow.Name = "  " }, "workflow.name is empty"},
		"step no name":            {func(f *File) { f.Workflow.Steps[0].Name = "" }, "step has no name"},
		"action empty":            {func(f *File) { f.Workflow.Steps[0].Action = "" }, "does not look like a PagerDuty action ID"},
		"action no colon":         {func(f *File) { f.Workflow.Steps[0].Action = "send-update" }, `action "send-update"`},
		"trigger type":            {func(f *File) { f.Triggers = []Trigger{{Type: "auto"}} }, `triggers[1]: type "auto"`},
		"conditional no cond":     {func(f *File) { f.Triggers = []Trigger{{Type: "conditional", Condition: " "}} }, "needs a condition"},
		"incident_type no types":  {func(f *File) { f.Triggers = []Trigger{{Type: "incident_type"}} }, "needs an incident_types list"},
		"all_services + services": {func(f *File) { f.Triggers = []Trigger{{Type: "manual", AllServices: true, Services: []string{"A"}}} }, "not both"},
		"nested step path": {func(f *File) {
			f.Workflow.Steps[0].InlineSteps = InlineStepsMap{{Name: "Then", Steps: []Step{{Name: "inner", Action: "bad"}}}}
		}, `workflow.steps[1] ("s").inline_steps.Then[1] ("inner")`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := validFile()
			c.mod(f)
			got := errStrings(Validate(f))
			if !strings.Contains(got, c.want) {
				t.Errorf("want error containing %q, got:\n%s", c.want, got)
			}
		})
	}
}

func TestValidateReportsAllAtOnce(t *testing.T) {
	f := &File{Workflow: Workflow{Steps: []Step{{}, {Name: "x", Action: "nope"}}}, Triggers: []Trigger{{Type: "bad"}, {Type: "conditional"}}}
	errs := Validate(f)
	// format, name, step1 name, step1 action, step2 action, trigger type, conditional
	if len(errs) != 7 {
		t.Errorf("want 7 errors, got %d:\n%s", len(errs), errStrings(errs))
	}
}

func TestJoinErrors(t *testing.T) {
	if JoinErrors(nil) != nil {
		t.Error("JoinErrors(nil) should be nil")
	}
	got := JoinErrors([]error{errors.New("one"), errors.New("two")}).Error()
	if got != "  - one\n  - two" {
		t.Errorf("JoinErrors = %q", got)
	}
}

func TestWarnings(t *testing.T) {
	f := validFile()
	f.Workflow.Steps[0].Name = "Outer"
	f.Workflow.Steps[0].Inputs = OrderedMap{
		{"User", "PUSER01"},
		{"Long", "Q0ABCDEFGHIJK1"},
		{"Conn", "a2045914-9116-4928-b8b1-3a00055aee55"},
		{"Word", "PRIORITY"},       // letters only but 8 chars matching [A-Z0-9]: see below
		{"Short", "P12345"},        // too short
		{"Lower", "puser01"},       // lowercase
		{"Sentence", "P1 is high"}, // spaces
		{"Upper UUID", "A2045914-9116-4928-B8B1-3A00055AEE55"},
		{"Num", 1234567},
	}
	f.Workflow.Steps[0].InlineSteps = InlineStepsMap{{Name: "Then", Steps: []Step{{Name: "Inner", Action: "a:b", Inputs: OrderedMap{{"Schedule", "PZMYJFZ"}}}}}}
	w := strings.Join(Warnings(f), "\n")
	for _, want := range []string{`"PUSER01"`, `"Q0ABCDEFGHIJK1"`, `"a2045914-9116-4928-b8b1-3a00055aee55"`, `step "Outer > Inner" input "Schedule"`} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings missing %s:\n%s", want, w)
		}
	}
	for _, not := range []string{`"P12345"`, `"puser01"`, `"P1 is high"`, `input "Num"`} {
		if strings.Contains(w, not) {
			t.Errorf("warnings should not flag %s:\n%s", not, w)
		}
	}
	// "PRIORITY" matches ^P[A-Z0-9]{6,13}$ - an ordinary upper-case word is flagged.
	// That is a known false positive of the heuristic; documented here, not asserted.

	if !strings.Contains(strings.Join(Warnings(&File{Format: Format}), ""), "no steps") {
		t.Error("empty workflow should warn")
	}
	if len(Warnings(validFile())) != 0 {
		t.Errorf("clean file should have no warnings: %v", Warnings(validFile()))
	}
}

func TestActionIDs(t *testing.T) {
	got := ActionIDs(richFile())
	want := map[string][]string{
		"pagerduty.com:incident-workflows:send-status-update:1": {"Post update 💡"},
		"pagerduty.com:logic:incident-workflows-loop-until:3":   {"Loop"},
		"pagerduty.com:logic:if-else:1":                         {"Branch", "Deeper"},
		"pagerduty.com:incident-workflows:add-note:1":           {"Leaf", "Other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ActionIDs = %v, want %v", got, want)
	}
}
