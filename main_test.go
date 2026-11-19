package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakePD is a tiny in-memory PagerDuty with one account per API token.
type fakePD struct {
	mu       sync.Mutex
	accounts map[string]*account
	writes   []string // "METHOD path" of every write, for assertions
}

type account struct {
	host    string
	actions []string
	// writeRejects are actions that pass the lookup but are refused on create,
	// like Kubernetes list-objects on the live EU account.
	writeRejects []string
	teams        []map[string]any
	services     []map[string]any
	workflows    []map[string]any
	triggers     []map[string]any
	nextID       int
}

func (a *account) id(prefix string) string {
	a.nextID++
	return fmt.Sprintf("%s%06d", prefix, a.nextID)
}

func (f *fakePD) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	acct := f.accounts[strings.TrimPrefix(r.Header.Get("Authorization"), "Token token=")]
	if acct == nil {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"Unauthorized","code":2006}}`)
		return
	}
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	if r.Method == "POST" || r.Method == "PUT" {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	p := r.URL.Path
	// Live API rule: empty input values are rejected on create and update.
	if _, isWF := body["incident_workflow"]; isWF {
		if raw, _ := json.Marshal(body); strings.Contains(string(raw), `"value":""`) {
			reply(400, map[string]any{"error": map[string]any{"message": "Invalid Input Provided", "errors": []string{"Invalid body: &#x5b;0&#x5d;.value is not allowed to be empty"}}})
			return
		}
		raw, _ := json.Marshal(body)
		for _, a := range acct.writeRejects {
			if strings.Contains(string(raw), `"`+a+`"`) {
				reply(404, map[string]any{"error": map[string]any{"message": "Could not find actions", "errors": []string{`Action "` + a + `" not found`}}})
				return
			}
		}
	}
	switch {
	case r.Method == "GET" && strings.HasPrefix(p, "/incident_workflows/actions/"):
		id := strings.TrimPrefix(p, "/incident_workflows/actions/")
		for _, a := range acct.actions {
			if a == id {
				reply(200, map[string]any{"action": map[string]any{"id": id}})
				return
			}
		}
		reply(404, map[string]any{"error": map[string]any{"message": "action '" + id + "' not found"}})
	case r.Method == "GET" && p == "/teams":
		reply(200, map[string]any{"teams": acct.teams, "more": false})
	case r.Method == "GET" && p == "/services":
		var list []map[string]any
		for _, s := range acct.services {
			list = append(list, map[string]any{"id": s["id"], "name": s["name"], "html_url": "https://" + acct.host + "/service-directory/" + s["id"].(string)})
		}
		reply(200, map[string]any{"services": list, "more": false})
	case r.Method == "GET" && p == "/incident_workflows":
		var list []map[string]any
		for _, wf := range acct.workflows {
			item := map[string]any{"id": wf["id"], "name": wf["name"], "type": "incident_workflow",
				"html_url": "https://" + acct.host + "/incident-workflows/" + wf["id"].(string)}
			if t, ok := wf["team"]; ok {
				item["team"] = t
			}
			list = append(list, item)
		}
		reply(200, map[string]any{"incident_workflows": list, "more": false, "limit": 100, "offset": 0})
	case r.Method == "POST" && p == "/incident_workflows":
		wf := body["incident_workflow"].(map[string]any)
		wf["id"] = acct.id("P")
		acct.workflows = append(acct.workflows, wf)
		reply(201, map[string]any{"incident_workflow": wf})
	case strings.HasPrefix(p, "/incident_workflows/triggers"):
		rest := strings.TrimPrefix(p, "/incident_workflows/triggers")
		switch {
		case r.Method == "GET" && rest == "":
			var out []map[string]any
			for _, t := range acct.triggers {
				if t["workflow"].(map[string]any)["id"] == r.URL.Query().Get("workflow_id") {
					out = append(out, t)
				}
			}
			reply(200, map[string]any{"triggers": out, "next_cursor": nil, "more": false})
		case r.Method == "POST" && rest == "":
			t := body["trigger"].(map[string]any)
			if _, has := t["workflow"].(map[string]any)["type"]; has {
				reply(400, map[string]any{"error": map[string]any{"message": "Invalid Input Provided", "errors": []string{`Invalid body: "trigger.workflow.type" is not allowed`}}})
				return
			}
			t["id"] = acct.id("T")
			acct.triggers = append(acct.triggers, t)
			reply(201, map[string]any{"trigger": t})
		case r.Method == "POST" && strings.HasSuffix(rest, "/services"):
			tid := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/services")
			for _, t := range acct.triggers {
				if t["id"] == tid {
					svcs, _ := t["services"].([]any)
					t["services"] = append(svcs, body["service"])
					reply(201, map[string]any{"trigger": t})
					return
				}
			}
			reply(404, map[string]any{"error": map[string]any{"message": "Not Found"}})
		default:
			reply(404, map[string]any{"error": map[string]any{"message": "Not Found"}})
		}
	case strings.HasPrefix(p, "/incident_workflows/"):
		id := strings.TrimPrefix(p, "/incident_workflows/")
		for i, wf := range acct.workflows {
			if wf["id"] != id {
				continue
			}
			if r.Method == "GET" {
				reply(200, map[string]any{"incident_workflow": wf})
				return
			}
			if r.Method == "PUT" {
				nw := body["incident_workflow"].(map[string]any)
				nw["id"] = id
				acct.workflows[i] = nw
				reply(200, map[string]any{"incident_workflow": nw})
				return
			}
		}
		reply(404, map[string]any{"error": map[string]any{"message": "Not Found"}})
	default:
		reply(404, map[string]any{"error": map[string]any{"message": "Not Found"}})
	}
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const sourceWorkflow = `{
  "id": "PWF0001", "type": "incident_workflow", "name": "🔻 Major Incident",
  "description": "Mobilise for P1s", "is_enabled": true, "self": "https://api/x", "html_url": "https://app/x",
  "created_at": "2026-01-01T00:00:00Z",
  "team": {"id": "PTEAMSRC", "type": "team_reference", "summary": "SRE"},
  "steps": [
    {"id": "PSTEP01", "type": "step", "name": "Post update",
     "action_configuration": {
       "action_id": "pagerduty.com:incident-workflows:send-status-update:1",
       "inputs": [
         {"name": "Message", "value": "Investigating\nsince {{current_date}}", "parameter_type": "text"},
         {"name": "Responder", "value": "PUSER01", "parameter_type": "user"},
         {"name": "Label", "value": "", "parameter_type": "multipleChoice"}
       ],
       "outputs": [{"name": "Result", "reference_name": "result", "parameter_type": "text"}]
     }},
    {"id": "PSTEP02", "type": "step", "name": "Branch",
     "action_configuration": {
       "action_id": "pagerduty.com:logic:if-else:1",
       "inputs": [{"name": "Condition", "value": "incident.priority matches 'P1'"}],
       "inline_steps_inputs": [
         {"name": "Then", "value": {"steps": [
           {"id": "PSTEP03", "name": "Add note", "action_configuration": {
             "action_id": "pagerduty.com:incident-workflows:add-note:1",
             "inputs": [{"name": "Note", "value": "P1 path"}]}}
         ]}}
       ]
     }}
  ]
}`

func newFake(t *testing.T) (*fakePD, *httptest.Server) {
	t.Helper()
	src := &account{
		host:     "acme.pagerduty.com",
		actions:  []string{"pagerduty.com:incident-workflows:send-status-update:1", "pagerduty.com:logic:if-else:1", "pagerduty.com:incident-workflows:add-note:1"},
		teams:    []map[string]any{{"id": "PTEAMSRC", "name": "SRE"}},
		services: []map[string]any{{"id": "PSVCSRC1", "name": "Checkout API"}},
		triggers: []map[string]any{{
			"id": "TSRC01", "type": "workflow_trigger", "trigger_type": "conditional",
			"condition":                     "incident.priority matches 'P1'",
			"workflow":                      map[string]any{"id": "PWF0001"},
			"is_subscribed_to_all_services": false,
			"services":                      []any{map[string]any{"id": "PSVCSRC1", "type": "service_reference", "summary": "Checkout API"}},
		}},
	}
	src.workflows = []map[string]any{mustJSON(t, sourceWorkflow)}
	dst := &account{
		host:     "acme-staging.pagerduty.com",
		actions:  []string{"pagerduty.com:incident-workflows:send-status-update:1", "pagerduty.com:logic:if-else:1", "pagerduty.com:incident-workflows:add-note:1"},
		teams:    []map[string]any{{"id": "PTEAMDST", "name": "sre"}}, // different ID, name case differs
		services: []map[string]any{{"id": "PSVCDST1", "name": "Checkout API"}, {"id": "PSVCDST2", "name": "Payments"}},
	}
	f := &fakePD{accounts: map[string]*account{"src-key": src, "dst-key": dst}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func setup(t *testing.T) (*fakePD, string) {
	t.Helper()
	f, srv := newFake(t)
	t.Setenv("PDWF_API_URL", srv.URL)
	t.Setenv("PAGERDUTY_TOKEN", "")
	return f, t.TempDir()
}

func pdwf(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return pdwfWithInput(t, nil, args...)
}

// pdwfWithInput runs pdwf; with input != nil it behaves as if run in a
// terminal and the input is what the user types.
func pdwfWithInput(t *testing.T, input []byte, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	interactive := input != nil
	in := os.Stdin
	if interactive {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(input)
		w.Close()
		in = r
		defer r.Close()
	}
	app := &App{Out: &buf, Err: &buf, In: in, interactive: &interactive}
	err := app.dispatch(context.Background(), args)
	return buf.String(), err
}

func TestExportThenImportIntoOtherAccount(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "export")

	log, err := pdwf(t, "export", "🔻 major incident", "--token", "src-key", "--out", out) // flag after arg, lower-case name
	if err != nil {
		t.Fatalf("export: %v\n%s", err, log)
	}
	data, err := os.ReadFile(filepath.Join(out, "major-incident.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	y := string(data)
	t.Logf("exported file:\n%s", y)
	for _, want := range []string{"format: pdwf/v1", "team: SRE", "Message: |-", "inline_steps:", "Then:", "- Checkout API", "type: conditional", "account: acme.pagerduty.com", `name: "🔻 Major Incident"`, `Label: ""`} {
		if !strings.Contains(y, want) {
			t.Errorf("export missing %q:\n%s", want, y)
		}
	}
	for _, bad := range []string{"PSTEP01", "parameter_type", "outputs", "PTEAMSRC", "PSVCSRC1"} {
		if strings.Contains(y, bad) {
			t.Errorf("export should not contain %q:\n%s", bad, y)
		}
	}

	// Dry run changes nothing.
	log, err = pdwf(t, "import", "--token", "dst-key", "--dry-run", out)
	if err != nil || !strings.Contains(log, "CREATE") {
		t.Fatalf("dry run: %v\n%s", err, log)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("dry run wrote: %v", fake.writes)
	}

	// Without a terminal and without --yes it refuses.
	if log, err = pdwf(t, "import", "--token", "dst-key", out); err == nil {
		t.Fatalf("expected refusal without --yes\n%s", log)
	}

	log, err = pdwf(t, "import", "--token", "dst-key", "--yes", out)
	t.Logf("import output:\n%s", log)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, log)
	}
	dst := fake.accounts["dst-key"]
	if len(dst.workflows) != 1 || len(dst.triggers) != 1 {
		t.Fatalf("want 1 workflow + 1 trigger, got %d + %d\n%s", len(dst.workflows), len(dst.triggers), log)
	}
	wf, _ := json.Marshal(dst.workflows[0])
	ws := string(wf)
	for _, want := range []string{`"PTEAMDST"`, `"inline_steps_inputs"`, `"P1 path"`, `"Investigating\nsince {{current_date}}"`} {
		if !strings.Contains(ws, want) {
			t.Errorf("created workflow missing %s: %s", want, ws)
		}
	}
	for _, bad := range []string{"PSTEP", "parameter_type", "outputs", "PTEAMSRC"} {
		if strings.Contains(ws, bad) {
			t.Errorf("created workflow should not contain %s: %s", bad, ws)
		}
	}
	tr, _ := json.Marshal(dst.triggers[0])
	if !strings.Contains(string(tr), "PSVCDST1") || !strings.Contains(string(tr), dst.workflows[0]["id"].(string)) {
		t.Errorf("trigger not remapped: %s", tr)
	}
	if !strings.Contains(log, `"PUSER01"`) {
		t.Errorf("expected a warning about the account-specific ID PUSER01:\n%s", log)
	}

	// Re-import: updates in place, backs up first, does not duplicate the trigger,
	// and adds a newly listed service to the existing trigger.
	p := filepath.Join(out, "major-incident.yaml")
	edited := strings.Replace(y, "- Checkout API", "- Checkout API\n      - Payments", 1)
	if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")
	log, err = pdwf(t, "import", "--token", "dst-key", "--yes", "--backup-dir", backups, p)
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, log)
	}
	if len(dst.workflows) != 1 || len(dst.triggers) != 1 {
		t.Fatalf("re-import duplicated things: %d workflows, %d triggers\n%s", len(dst.workflows), len(dst.triggers), log)
	}
	if _, ok := dst.workflows[0]["steps"]; !ok {
		t.Errorf("update did not store the workflow: %v", dst.workflows[0])
	}
	if !strings.Contains(log, "UPDATE") || !strings.Contains(log, "added 1 service") {
		t.Errorf("unexpected re-import log:\n%s", log)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 1 {
		t.Errorf("expected 1 backup file, got %d", len(entries))
	}
}

func TestImportRefusesUnknownTeamBeforeWriting(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "export")
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	p := filepath.Join(out, "major-incident.yaml")
	data, _ := os.ReadFile(p)
	_ = os.WriteFile(p, bytes.Replace(data, []byte("team: SRE"), []byte("team: Platform"), 1), 0o644)
	log, err := pdwf(t, "import", "--token", "dst-key", "--yes", out)
	if err == nil || !strings.Contains(log, `team "Platform" does not exist`) {
		t.Fatalf("expected team error, got %v\n%s", err, log)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("wrote despite errors: %v", fake.writes)
	}
}

func TestValidateCatchesTypos(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(p, []byte("format: pdwf/v1\nworkflow:\n  name: X\n  stepz: []\n"), 0o644)
	log, err := pdwf(t, "validate", p)
	if err == nil || !strings.Contains(log, "stepz") {
		t.Fatalf("expected typo error, got %v\n%s", err, log)
	}
}

func TestSelectorErrors(t *testing.T) {
	_, dir := setup(t)
	log, err := pdwf(t, "export", "--token", "src-key", "--out", dir, "Major")
	if err == nil || !strings.Contains(log+err.Error(), "did you mean") {
		t.Fatalf("expected suggestion, got %v\n%s", err, log)
	}
	_, err = pdwf(t, "list")
	if err == nil || !strings.Contains(err.Error(), "--token") {
		t.Fatalf("with no key and no terminal, list should explain how to give one: %v", err)
	}
}

func TestConfirmationNeedsAccountName(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "export")
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	// Typing "yes" (or the wrong account) is not enough.
	log, err := pdwfWithInput(t, []byte("yes\n"), "import", "--token", "dst-key", out)
	if err != nil || !strings.Contains(log, "Cancelled") || len(fake.writes) != 0 {
		t.Fatalf("expected cancel without writes, got %v, writes %v\n%s", err, fake.writes, log)
	}
	if !strings.Contains(log, `"acme-staging"`) {
		t.Errorf("prompt should name the target account:\n%s", log)
	}
	log, err = pdwfWithInput(t, []byte("acme-staging\n"), "import", "--token", "dst-key", out)
	if err != nil || len(fake.accounts["dst-key"].workflows) != 1 {
		t.Fatalf("expected import after typing the account name: %v\n%s", err, log)
	}
	// Importing back into the source account is flagged.
	log, _ = pdwf(t, "import", "--token", "src-key", "--dry-run", out)
	if !strings.Contains(log, "same account") {
		t.Errorf("expected same-account note:\n%s", log)
	}
}

func TestBadKeyExplainsItself(t *testing.T) {
	setup(t)
	_, err := pdwf(t, "check", "--token", "wrong")
	if err == nil || !strings.Contains(err.Error(), "REST API key") {
		t.Fatalf("expected a helpful 401, got %v", err)
	}
}

func TestDecideEnabled(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name            string
		action          string
		file, current   *bool
		enable, disable bool
		want            *bool
	}{
		{"new defaults off even if source was on", "create", &on, nil, false, false, &off},
		{"update keeps current on", "update", &off, &on, false, false, &on},
		{"update keeps current off", "update", &on, &off, false, false, &off},
		{"--enable copies file on", "create", &on, nil, true, false, &on},
		{"--enable copies file off over current on", "update", &off, &on, true, false, &off},
		{"--disable wins on update", "update", &on, &on, false, true, &off},
		{"--disable on create", "create", &on, nil, false, true, &off},
	}
	for _, c := range cases {
		got, _ := decideEnabled(c.action, c.file, c.current, c.enable, c.disable)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, *c.want)
		}
	}
}

func TestImportOnOffEndToEnd(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "export")
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	dst := fake.accounts["dst-key"]

	// Default: source was ON, the new copy is created OFF.
	log, err := pdwf(t, "import", "--token", "dst-key", "--yes", "--backup-dir", filepath.Join(dir, "b1"), out)
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if dst.workflows[0]["is_enabled"] != false || !strings.Contains(log, "OFF (new)") {
		t.Fatalf("new workflow should be OFF, got %v\n%s", dst.workflows[0]["is_enabled"], log)
	}

	// Someone turns it on in the target; a default re-import must not turn it off.
	dst.workflows[0]["is_enabled"] = true
	log, err = pdwf(t, "import", "--token", "dst-key", "--yes", "--backup-dir", filepath.Join(dir, "b2"), out)
	if err != nil || dst.workflows[0]["is_enabled"] != true || !strings.Contains(log, "ON (unchanged)") {
		t.Fatalf("update should keep ON, got %v (%v)\n%s", dst.workflows[0]["is_enabled"], err, log)
	}
	if strings.Contains(log, "start running on matching incidents") {
		t.Errorf("no warning expected for a workflow that is already ON and stays ON:\n%s", log)
	}

	// --disable turns it off.
	if log, err = pdwf(t, "import", "--token", "dst-key", "--yes", "--disable", "--backup-dir", filepath.Join(dir, "b3"), out); err != nil || dst.workflows[0]["is_enabled"] != false {
		t.Fatalf("--disable should turn OFF, got %v (%v)\n%s", dst.workflows[0]["is_enabled"], err, log)
	}
	// --enable copies the file's ON.
	if log, err = pdwf(t, "import", "--token", "dst-key", "--yes", "--enable", "--backup-dir", filepath.Join(dir, "b4"), out); err != nil || dst.workflows[0]["is_enabled"] != true {
		t.Fatalf("--enable should copy ON from the file, got %v (%v)\n%s", dst.workflows[0]["is_enabled"], err, log)
	}
	if !strings.Contains(log, "start running on matching incidents") {
		t.Errorf("switching OFF -> ON with an automatic trigger should warn:\n%s", log)
	}
	if _, err := pdwf(t, "import", "--token", "dst-key", "--enable", "--disable", out); err == nil {
		t.Fatal("--enable with --disable should be refused")
	}
}

func TestImportRefusesActionMissingInTarget(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "export")
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	// The target's catalogue lacks the nested add-note action (like EU lacking list-objects).
	fake.accounts["dst-key"].actions = fake.accounts["dst-key"].actions[:2]
	log, err := pdwf(t, "import", "--token", "dst-key", "--yes", out)
	if err == nil || !strings.Contains(log, "pagerduty.com:incident-workflows:add-note:1 does not exist") || !strings.Contains(log, "Add note") {
		t.Fatalf("expected missing-action error naming the step, got %v\n%s", err, log)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("wrote despite missing action: %v", fake.writes)
	}
}
