package pd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var ctx = context.Background()

// offsetHandler serves `total` items under key, paginated by limit/offset.
func offsetHandler(t *testing.T, key string, total int, name func(i int) string, offsets *[]int, check func(*http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		*offsets = append(*offsets, off)
		var items []map[string]any
		for i := off; i < off+limit && i < total; i++ {
			items = append(items, map[string]any{"id": fmt.Sprintf("P%06d", i), "name": name(i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{key: items, "more": off+limit < total, "limit": limit, "offset": off})
	}
}

func TestListWorkflowsPaginatesSortsAndIncludesTeam(t *testing.T) {
	var offsets []int
	names := []string{"beta", "Alpha", "gamma"}
	c, _ := newTestClient(t, offsetHandler(t, "incident_workflows", 250, func(i int) string {
		return fmt.Sprintf("%s-%03d", names[i%3], i)
	}, &offsets, func(r *http.Request) {
		if r.URL.Path != "/incident_workflows" || r.URL.Query().Get("include[]") != "team" {
			t.Errorf("bad request %s", r.URL)
		}
	}))
	all, err := c.ListWorkflows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 250 {
		t.Fatalf("got %d", len(all))
	}
	if fmt.Sprint(offsets) != "[0 100 200]" {
		t.Errorf("offsets %v", offsets)
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) }) {
		t.Error("not sorted case-insensitively")
	}
	if !strings.HasPrefix(all[0].Name, "Alpha") {
		t.Errorf("first is %q, want Alpha-... (case-insensitive sort)", all[0].Name)
	}
}

func TestListTeamsAndServicesPaginate(t *testing.T) {
	for _, tc := range []struct {
		path, key string
		call      func(*Client, context.Context) ([]Named, error)
	}{
		{"/teams", "teams", (*Client).ListTeams},
		{"/services", "services", (*Client).ListServices},
	} {
		var offsets []int
		c, _ := newTestClient(t, offsetHandler(t, tc.key, 101, func(i int) string { return fmt.Sprintf("n%d", i) }, &offsets, func(r *http.Request) {
			if r.URL.Path != tc.path {
				t.Errorf("path %s", r.URL.Path)
			}
		}))
		got, err := tc.call(c, ctx)
		if err != nil || len(got) != 101 || got[100].Name != "n100" || got[100].ID != "P000100" {
			t.Errorf("%s: %d items, err %v", tc.path, len(got), err)
		}
		if fmt.Sprint(offsets) != "[0 100]" {
			t.Errorf("%s offsets %v", tc.path, offsets)
		}
	}
}

func TestListNamedExactPageStops(t *testing.T) {
	var offsets []int
	c, _ := newTestClient(t, offsetHandler(t, "teams", 100, func(i int) string { return "t" }, &offsets, nil))
	got, err := c.ListTeams(ctx)
	if err != nil || len(got) != 100 || len(offsets) != 1 {
		t.Errorf("got %d items over %d requests, err %v", len(got), len(offsets), err)
	}
}

func TestListTriggersCursorAndUnknownFields(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/incident_workflows/triggers" || q.Get("workflow_id") != "PWF" {
			t.Errorf("bad request %s", r.URL)
		}
		cur := q.Get("cursor")
		cursors = append(cursors, cur)
		switch cur {
		case "":
			fmt.Fprint(w, `{"triggers":[{"id":"T1","trigger_type":"manual","zeta_field":1,"created_at":"x"}],"next_cursor":"c2","more":true}`)
		case "c2":
			fmt.Fprint(w, `{"triggers":[{"id":"T2","trigger_type":"incident_type","incident_types":["sec"],"is_disabled":true,"alpha_field":2,"zeta_field":3,
				"workflow":{"id":"PWF","name":"n","published":true,"created_at":"x"},"permissions":{"restricted":false}}],"next_cursor":"c3","more":false}`)
		default:
			t.Errorf("should have stopped at more=false, asked for cursor %q", cur)
		}
	}))
	trs, unknown, err := c.ListTriggers(ctx, "PWF")
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 2 || trs[1].IncidentTypes[0] != "sec" || !*trs[1].IsDisabled || trs[1].Workflow.ID != "PWF" {
		t.Errorf("decoded %+v", trs)
	}
	if fmt.Sprint(cursors) != "[ c2]" {
		t.Errorf("cursors %q", cursors)
	}
	if !reflect.DeepEqual(unknown, []string{"alpha_field", "zeta_field"}) {
		t.Errorf("unknown %v, want aggregated+sorted [alpha_field zeta_field]", unknown)
	}
}

func TestListTriggersStopsOnNullOrEmptyCursor(t *testing.T) {
	for _, tail := range []string{`"next_cursor":null`, `"next_cursor":""`, ``} {
		calls := 0
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			sep := ""
			if tail != "" {
				sep = ","
			}
			fmt.Fprintf(w, `{"triggers":[{"id":"T","trigger_type":"manual"}]%s%s}`, sep, tail)
		}))
		trs, unknown, err := c.ListTriggers(ctx, "W")
		if err != nil || len(trs) != 1 || len(unknown) != 0 || calls != 1 {
			t.Errorf("tail %q: %d triggers, unknown %v, calls %d, err %v", tail, len(trs), unknown, calls, err)
		}
	}
}

func TestCheckAction(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/incident_workflows/actions/pagerduty.com:slack:send:1":
			fmt.Fprint(w, `{"action":{"id":"pagerduty.com:slack:send:1"}}`)
		case "/incident_workflows/actions/pagerduty.com:roles:assign:1":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"message":"This global action is not yet approved for usage."}}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"message":"action not found"}}`)
		}
	}))
	if err := c.CheckAction(ctx, "pagerduty.com:slack:send:1"); err != nil {
		t.Errorf("known action: %v", err)
	}
	if err := c.CheckAction(ctx, "pagerduty.com:roles:assign:1"); !IsStatus(err, 403) {
		t.Errorf("unapproved action should be 403, got %v", err)
	}
	if err := c.CheckAction(ctx, "pagerduty.com:made-up:x:1"); !IsStatus(err, 404) {
		t.Errorf("unknown action should be 404, got %v", err)
	}
}

// A realistic GET payload containing every read-only field the live API returns.
const liveWorkflow = `{"incident_workflow":{
  "id":"P0MJPLR","name":"Sec","summary":"Sec","description":"","type":"incident_workflow","is_enabled":false,
  "team":{"id":"PT","type":"team","summary":"SRE","self":"s","html_url":"h"},
  "created_at":"2026","published_date":"2026","self":"s","html_url":"https://acme.pagerduty.com/x","triggers":[{"id":"t"}],
  "steps":[{"id":"S1","type":"step","name":"loop","description":"d","action_configuration":{
    "action_id":"pagerduty.com:logic:incident-workflows-loop-until:3","description":"d",
    "inputs":[{"name":"n","value":"v","parameter_type":"text"}],
    "outputs":[{"name":"o","reference_name":"o","parameter_type":"text"}],
    "inline_steps_inputs":[{"name":"Actions","value":{"steps":[{"id":"S2","name":"inner","action_configuration":{"action_id":"x:1","inputs":[],"outputs":[]}}]}}]
  }}]}}`

func TestGetWorkflowKnownFieldsNotReported(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/incident_workflows/P0MJPLR" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, liveWorkflow)
	}))
	w, unknown, err := c.GetWorkflow(ctx, "P0MJPLR")
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Errorf("known read-only fields reported as unknown: %v", unknown)
	}
	if w.Team.Label() != "SRE" || *w.IsEnabled || w.Steps[0].ActionConfiguration.InlineStepsInputs[0].Value.Steps[0].Name != "inner" {
		t.Errorf("decode wrong: %+v", w)
	}
}

func TestGetWorkflowReportsUnknownTopLevelAndNested(t *testing.T) {
	payload := strings.Replace(liveWorkflow, `"id":"P0MJPLR",`, `"id":"P0MJPLR","new_field":1,`, 1)
	payload = strings.Replace(payload, `"name":"inner",`, `"name":"inner","nested_new":true,`, 1)
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }))
	_, unknown, err := c.GetWorkflow(ctx, "P0MJPLR")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(unknown)
	if !reflect.DeepEqual(unknown, []string{"nested_new", "new_field"}) {
		t.Errorf("unknown %v", unknown)
	}
}

func TestGetWorkflowEscapesID(t *testing.T) {
	var got string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.EscapedPath()
		fmt.Fprint(w, `{"incident_workflow":{"name":"x","steps":[]}}`)
	}))
	_, _, _ = c.GetWorkflow(ctx, "a/b")
	if got != "/incident_workflows/a%2Fb" {
		t.Errorf("path %q", got)
	}
}

func TestUnknownFieldsAndStripKey(t *testing.T) {
	type small struct {
		A int `json:"a"`
		N *struct {
			B int `json:"b"`
		} `json:"n"`
	}
	got := UnknownFields([]byte(`{"a":1,"x":1,"n":{"b":2,"y":3},"z":[1]}`), &small{})
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"x", "y", "z"}) {
		t.Errorf("got %v", got)
	}
	if got := UnknownFields([]byte(`{"a":1}`), &small{}); len(got) != 0 {
		t.Errorf("clean payload reported %v", got)
	}
	if got := UnknownFields([]byte(`not json`), &small{}); len(got) != 0 {
		t.Errorf("invalid JSON should report nothing, got %v", got)
	}

	out := stripKey([]byte(`{"k":1,"a":{"k":2,"b":[{"k":3,"c":4}]}}`), "k")
	if string(out) != `{"a":{"b":[{"c":4}]}}` {
		t.Errorf("stripKey %s", out)
	}
	if string(stripKey([]byte(`nope`), "k")) != "nope" {
		t.Error("stripKey should return invalid input unchanged")
	}
}

func TestWritesAreWrapped(t *testing.T) {
	type call struct {
		method, path string
		body         map[string]any
	}
	var calls []call
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{r.Method, r.URL.Path, decodeBody(t, r)})
		switch {
		case strings.HasSuffix(r.URL.Path, "/services"):
			w.WriteHeader(201)
			fmt.Fprint(w, `{"trigger":{"id":"T1"}}`)
		case strings.HasPrefix(r.URL.Path, "/incident_workflows/triggers"):
			w.WriteHeader(201)
			fmt.Fprint(w, `{"trigger":{"id":"T1","trigger_type":"manual"}}`)
		default:
			fmt.Fprint(w, `{"incident_workflow":{"id":"PNEW","name":"n","steps":[]}}`)
		}
	}))
	created, err := c.CreateWorkflow(ctx, Workflow{Name: "n", Steps: []Step{}})
	if err != nil || created.ID != "PNEW" {
		t.Fatalf("create %v %v", created, err)
	}
	if _, err := c.UpdateWorkflow(ctx, "PX", Workflow{Name: "n", Steps: []Step{}}); err != nil {
		t.Fatal(err)
	}
	tr, err := c.CreateTrigger(ctx, Trigger{TriggerType: "manual", Workflow: &WorkflowRef{ID: "PNEW"}})
	if err != nil || tr.ID != "T1" {
		t.Fatalf("trigger %v %v", tr, err)
	}
	if err := c.AddTriggerService(ctx, "T1", "PSVC"); err != nil {
		t.Fatal(err)
	}

	want := []struct{ method, path, key string }{
		{"POST", "/incident_workflows", "incident_workflow"},
		{"PUT", "/incident_workflows/PX", "incident_workflow"},
		{"POST", "/incident_workflows/triggers", "trigger"},
		{"POST", "/incident_workflows/triggers/T1/services", "service"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls %+v", calls)
	}
	for i, w := range want {
		c := calls[i]
		if c.method != w.method || c.path != w.path {
			t.Errorf("call %d: %s %s, want %s %s", i, c.method, c.path, w.method, w.path)
		}
		if _, ok := c.body[w.key]; !ok || len(c.body) != 1 {
			t.Errorf("call %d body %v should be wrapped in only %q", i, c.body, w.key)
		}
	}
	if svc := calls[3].body["service"].(map[string]any); svc["id"] != "PSVC" {
		t.Errorf("service body %v", svc)
	}
	// The trigger's workflow ref must not carry "type" (the live API rejects it).
	wf := calls[2].body["trigger"].(map[string]any)["workflow"].(map[string]any)
	if _, has := wf["type"]; has || wf["id"] != "PNEW" {
		t.Errorf("trigger workflow ref %v", wf)
	}
	// A written workflow must not carry read-only fields.
	sent := calls[0].body["incident_workflow"].(map[string]any)
	for _, k := range []string{"id", "self", "html_url", "created_at", "summary", "published_date", "triggers", "type"} {
		if _, has := sent[k]; has {
			t.Errorf("create body carries read-only %q: %v", k, sent)
		}
	}
}

func TestAccountHost(t *testing.T) {
	t.Run("from first workflow", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"incident_workflows":[{"html_url":"https://acme.pagerduty.com/incident-workflows/x"}]}`)
		}))
		if h := c.AccountHost(ctx); h != "acme.pagerduty.com" {
			t.Errorf("got %q", h)
		}
	})
	t.Run("falls through empty and erroring endpoints", func(t *testing.T) {
		var paths []string
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			switch r.URL.Path {
			case "/incident_workflows":
				fmt.Fprint(w, `{"incident_workflows":[]}`)
			case "/services":
				w.WriteHeader(403)
			case "/escalation_policies":
				fmt.Fprint(w, `{"escalation_policies":[{"html_url":"https://acme.eu.pagerduty.com/ep/1"}]}`)
			default:
				t.Errorf("should have stopped before %s", r.URL.Path)
			}
		}))
		if h := c.AccountHost(ctx); h != "acme.eu.pagerduty.com" {
			t.Errorf("got %q (paths %v)", h, paths)
		}
	})
	t.Run("nothing found", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{}`)
		}))
		if h := c.AccountHost(ctx); h != "" {
			t.Errorf("got %q", h)
		}
	})
	t.Run("items without html_url", func(t *testing.T) {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"incident_workflows":[{"html_url":""}],"services":[{"html_url":"not a url"}],"users":[{"html_url":"https://x.pagerduty.com/u"}]}`)
		}))
		if h := c.AccountHost(ctx); h != "x.pagerduty.com" {
			t.Errorf("got %q", h)
		}
	})
}

func TestPing(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/incident_workflows" || r.URL.Query().Get("limit") != "1" {
			t.Errorf("ping %s", r.URL)
		}
		w.WriteHeader(401)
	}))
	if err := c.Ping(ctx); !IsStatus(err, 401) {
		t.Errorf("got %v", err)
	}
}

func TestReferenceLabel(t *testing.T) {
	var nilRef *Reference
	if nilRef.Label() != "" {
		t.Error("nil Label should be empty")
	}
	if (&Reference{Name: "n", Summary: "s"}).Label() != "n" {
		t.Error("Name should win")
	}
	if (&Reference{Summary: "s"}).Label() != "s" {
		t.Error("Summary fallback")
	}
	if (&Reference{ID: "P1"}).Label() != "" {
		t.Error("ID is not a label")
	}
}
