package wffile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/justynroberts/workflow-utility/internal/pd"
)

func strp(s string) *string { return &s }

func apiWorkflow() pd.Workflow {
	return pd.Workflow{
		ID: "PWF1", Type: "incident_workflow", Name: "WF", Description: "d",
		Team:      &pd.Reference{ID: "PTEAM1", Type: "team", Summary: "SRE"},
		IsEnabled: boolp(false),
		Steps: []pd.Step{{
			ID: "PSTEP1", Type: "step", Name: "Loop",
			ActionConfiguration: pd.ActionConfiguration{
				ActionID: "pagerduty.com:logic:loop:1", Description: "loops",
				Inputs:  []pd.Input{{Name: "B", Value: "2", ParameterType: "text"}, {Name: "A", Value: "1"}},
				Outputs: json.RawMessage(`[{"name":"x"}]`),
				InlineStepsInputs: []pd.InlineStepsInput{{Name: "Actions", Value: pd.InlineSteps{Steps: []pd.Step{{
					ID: "PSTEP2", Name: "Inner",
					ActionConfiguration: pd.ActionConfiguration{ActionID: "pagerduty.com:x:y:1", Inputs: []pd.Input{{Name: "N", Value: "v"}}},
				}}}}},
			},
		}},
	}
}

func TestFromAPI(t *testing.T) {
	f := FromAPI(apiWorkflow(), []pd.Trigger{
		{TriggerType: "conditional", Condition: strp("c"), IsDisabled: boolp(true), Services: []pd.Reference{
			{ID: "PSVC2"},                   // name from map
			{ID: "PSVC3", Summary: "Bravo"}, // name from summary
			{ID: "PSVC4"},                   // nothing known: ID
			{ID: "PSVC5", Name: "alpha"},    // name field
		}},
		{TriggerType: "manual", IsSubscribedToAllServices: true, Services: []pd.Reference{{ID: "PSVC9", Summary: "hidden"}}},
		{TriggerType: "incident_type", IncidentTypes: []string{"b", "a"}, IsSubscribedToAllServices: true},
	}, "SRE", map[string]string{"PSVC2": "Zulu"})

	if f.Format != Format || f.Workflow.Team != "SRE" || f.Workflow.Enabled == nil || *f.Workflow.Enabled {
		t.Errorf("workflow fields wrong: %+v", f.Workflow)
	}
	s := f.Workflow.Steps[0]
	if s.Action != "pagerduty.com:logic:loop:1" || s.ActionDescription != "loops" {
		t.Errorf("step = %+v", s)
	}
	if !reflect.DeepEqual(s.Inputs, OrderedMap{{"B", "2"}, {"A", "1"}}) {
		t.Errorf("inputs = %v (order must be kept)", s.Inputs)
	}
	if len(s.InlineSteps) != 1 || s.InlineSteps[0].Name != "Actions" || s.InlineSteps[0].Steps[0].Name != "Inner" {
		t.Errorf("inline = %+v", s.InlineSteps)
	}
	if got := f.Triggers[0].Services; !reflect.DeepEqual(got, []string{"Bravo", "PSVC4", "Zulu", "alpha"}) {
		t.Errorf("services = %v, want sorted name/summary/ID fallbacks", got)
	}
	if !f.Triggers[0].Disabled || f.Triggers[0].Condition != "c" {
		t.Errorf("trigger 0 = %+v", f.Triggers[0])
	}
	if len(f.Triggers[1].Services) != 0 || !f.Triggers[1].AllServices {
		t.Errorf("all_services trigger should list no services: %+v", f.Triggers[1])
	}
	if !reflect.DeepEqual(f.Triggers[2].IncidentTypes, []string{"b", "a"}) {
		t.Errorf("incident types = %v", f.Triggers[2].IncidentTypes)
	}
}

func TestFromAPITeamFallsBackToID(t *testing.T) {
	if f := FromAPI(apiWorkflow(), nil, "", nil); f.Workflow.Team != "PTEAM1" {
		t.Errorf("team = %q, want the ID", f.Workflow.Team)
	}
	w := apiWorkflow()
	w.Team = nil
	if f := FromAPI(w, nil, "ignored", nil); f.Workflow.Team != "" {
		t.Errorf("no team should give empty, got %q", f.Workflow.Team)
	}
}

func TestToAPI(t *testing.T) {
	f := &File{Format: Format, Workflow: Workflow{
		Name: "WF", Team: " sre ", Enabled: boolp(true),
		Steps: []Step{{
			Name: "S", Action: "a:b:1", ActionDescription: "ad",
			Inputs:      OrderedMap{{"Empty", ""}, {"Nil", nil}, {"Zero", 0}, {"False", false}, {"StrZero", "0"}, {"Space", " "}},
			InlineSteps: InlineStepsMap{{Name: "Then", Steps: []Step{{Name: "I", Action: "c:d:1", Inputs: OrderedMap{{"E", ""}}}}}},
		}},
	}, Triggers: []Trigger{
		{Type: "conditional", Condition: "x", Services: []string{"PSVC1", "payments"}, Disabled: true},
		{Type: "incident_type", IncidentTypes: []string{"t"}, AllServices: true},
	}}
	r := &Resolver{
		Teams:    []pd.Named{{ID: "PT", Name: "SRE"}},
		Services: []pd.Named{{ID: "PSVC1", Name: "Checkout"}, {ID: "PSVC2", Name: "Payments"}},
	}
	w, trs, errs := r.ToAPI(f)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if w.Team == nil || w.Team.ID != "PT" || w.Team.Type != "team_reference" {
		t.Errorf("team = %+v", w.Team)
	}
	if w.IsEnabled == nil || !*w.IsEnabled {
		t.Errorf("enabled = %v", w.IsEnabled)
	}
	var names []string
	for _, in := range w.Steps[0].ActionConfiguration.Inputs {
		names = append(names, in.Name)
		if in.ParameterType != "" {
			t.Errorf("parameter_type must not be sent: %+v", in)
		}
	}
	if !reflect.DeepEqual(names, []string{"Zero", "False", "StrZero", "Space"}) {
		t.Errorf("inputs sent = %v; want \"\" and nil dropped, 0/false/\"0\" kept", names)
	}
	if inner := w.Steps[0].ActionConfiguration.InlineStepsInputs[0].Value.Steps[0]; inner.ActionConfiguration.Inputs == nil || len(inner.ActionConfiguration.Inputs) != 0 {
		t.Errorf("nested empty input should be dropped, inputs must be [] not null: %#v", inner.ActionConfiguration.Inputs)
	}

	raw, _ := json.Marshal(w.Steps)
	for _, bad := range []string{`"id"`, `"type":"step"`, `"outputs"`, `"parameter_type"`, `"self"`, `"html_url"`, `"created_at"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("payload must not contain %s: %s", bad, raw)
		}
	}
	if !strings.Contains(string(raw), `"inputs":[]`) {
		t.Errorf("inner step with no inputs should send [] : %s", raw)
	}

	if trs[0].IsDisabled == nil || !*trs[0].IsDisabled || trs[0].Condition == nil || *trs[0].Condition != "x" {
		t.Errorf("trigger 0 = %+v", trs[0])
	}
	if got := []string{trs[0].Services[0].ID, trs[0].Services[1].ID}; !reflect.DeepEqual(got, []string{"PSVC1", "PSVC2"}) || trs[0].Services[0].Type != "service_reference" {
		t.Errorf("services resolved to %v", trs[0].Services)
	}
	if trs[1].IsDisabled != nil || !reflect.DeepEqual(trs[1].IncidentTypes, []string{"t"}) || !trs[1].IsSubscribedToAllServices {
		t.Errorf("trigger 1 = %+v", trs[1])
	}
	if trs[1].Condition != nil {
		t.Errorf("empty condition should not be sent")
	}
}

func TestToAPINoTeamNoEnabled(t *testing.T) {
	w, _, errs := (&Resolver{}).ToAPI(&File{Workflow: Workflow{Name: "x"}})
	if len(errs) != 0 || w.Team != nil || w.IsEnabled != nil || w.Steps == nil {
		t.Errorf("got %+v %v", w, errs)
	}
}

func TestToAPIReturnsAllErrors(t *testing.T) {
	f := &File{Workflow: Workflow{Name: "x", Team: "Platform"}, Triggers: []Trigger{
		{Type: "manual", Services: []string{"Nope", "Checkout"}},
		{Type: "manual", Services: []string{"Dup"}},
	}}
	r := &Resolver{
		Teams:    []pd.Named{{ID: "PT1", Name: "Platform Eng"}},
		Services: []pd.Named{{ID: "PS1", Name: "Checkout"}, {ID: "PS2", Name: "dup"}, {ID: "PS3", Name: "DUP "}},
	}
	_, trs, errs := r.ToAPI(f)
	all := errStrings(errs)
	if len(errs) != 3 {
		t.Fatalf("want 3 errors, got %d:\n%s", len(errs), all)
	}
	for _, want := range []string{
		`team "Platform" does not exist in the target account (did you mean "Platform Eng"?)`,
		`triggers[1]: service "Nope" does not exist`,
		`triggers[2]: service name "Dup" matches 2 services (PS2, PS3)`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if len(trs[0].Services) != 1 || trs[0].Services[0].ID != "PS1" {
		t.Errorf("resolvable services should still resolve: %+v", trs[0].Services)
	}
}

func TestResolve(t *testing.T) {
	items := []pd.Named{{ID: "PA", Name: "Alpha"}, {ID: "PB", Name: " beta "}, {ID: "PC", Name: "Gamma"}}
	for want, in := range map[string]string{"PA": "PA", "PB": "BETA", "PC": " gamma"} {
		if got, err := resolve("service", in, items); err != nil || got != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// An ID wins over a name that happens to equal another item's ID.
	if got, _ := resolve("service", "PB", append(items, pd.Named{ID: "PZ", Name: "PB"})); got != "PB" {
		t.Errorf("ID should win, got %q", got)
	}
	_, err := resolve("service", "zzz", items)
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no suggestion expected for unrelated name: %v", err)
	}
	_, err = resolve("service", "alph", items)
	if err == nil || !strings.Contains(err.Error(), `did you mean "Alpha"`) {
		t.Errorf("want suggestion, got %v", err)
	}
	many := []pd.Named{{ID: "1", Name: "api a"}, {ID: "2", Name: "api b"}, {ID: "3", Name: "api c"}, {ID: "4", Name: "api d"}}
	if s := suggest("api", many); strings.Count(s, `"`) != 6 {
		t.Errorf("suggest should cap at 3, got %s", s)
	}
}

func TestTriggerKey(t *testing.T) {
	a := pd.Trigger{TriggerType: "incident_type", Condition: strp(" x "), IncidentTypes: []string{"b", "a"}}
	b := pd.Trigger{TriggerType: "incident_type", Condition: strp("x"), IncidentTypes: []string{"a", "b"}}
	if TriggerKey(a) != TriggerKey(b) {
		t.Error("keys should match regardless of order and whitespace")
	}
	if a.IncidentTypes[0] != "b" {
		t.Error("TriggerKey must not reorder the caller's slice")
	}
	c := pd.Trigger{TriggerType: "incident_type", IncidentTypes: []string{"a"}}
	if TriggerKey(a) == TriggerKey(c) {
		t.Error("different incident types should differ")
	}
	if TriggerKey(pd.Trigger{TriggerType: "manual"}) == TriggerKey(pd.Trigger{TriggerType: "conditional"}) {
		t.Error("types should differ")
	}
	if TriggerKey(pd.Trigger{TriggerType: "manual"}) != TriggerKey(pd.Trigger{TriggerType: "manual", Condition: strp("")}) {
		t.Error("nil and empty condition should match")
	}
}

// API -> file -> API keeps everything import needs.
func TestAPIFileAPIRoundTrip(t *testing.T) {
	src := apiWorkflow()
	f := FromAPI(src, nil, "SRE", nil)
	w, _, errs := (&Resolver{Teams: []pd.Named{{ID: "PT2", Name: "SRE"}}}).ToAPI(f)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if w.Team.ID != "PT2" || w.Name != src.Name || w.Description != src.Description || *w.IsEnabled != *src.IsEnabled {
		t.Errorf("workflow = %+v", w)
	}
	got := w.Steps[0].ActionConfiguration
	if got.ActionID != src.Steps[0].ActionConfiguration.ActionID || got.Description != "loops" || len(got.Inputs) != 2 ||
		got.InlineStepsInputs[0].Value.Steps[0].ActionConfiguration.Inputs[0].Value != "v" {
		t.Errorf("steps = %+v", got)
	}
}
