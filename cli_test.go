package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justynroberts/workflow-utility/internal/pd"
)

func TestParseFlagsAnywhere(t *testing.T) {
	cases := []struct {
		args     []string
		pos      []string
		token    string
		dry      bool
		wantFail bool
	}{
		{[]string{"a", "--token", "k", "b"}, []string{"a", "b"}, "k", false, false},
		{[]string{"--dry-run", "a", "--token=k"}, []string{"a"}, "k", true, false},
		{[]string{"a", "--", "--token", "x"}, []string{"a", "--token", "x"}, "", false, false},
		{[]string{"My Workflow", "--dry-run"}, []string{"My Workflow"}, "", true, false},
		{[]string{"--nope"}, nil, "", false, true},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		tok := fs.String("token", "", "")
		dry := fs.Bool("dry-run", false, "")
		pos, err := parseFlags(fs, c.args)
		if c.wantFail {
			if err == nil {
				t.Errorf("%v: expected an error", c.args)
			}
			continue
		}
		if err != nil || strings.Join(pos, "|") != strings.Join(c.pos, "|") || *tok != c.token || *dry != c.dry {
			t.Errorf("%v: pos %q token %q dry %v err %v", c.args, pos, *tok, *dry, err)
		}
	}
}

func TestSelectWorkflows(t *testing.T) {
	all := []pd.Workflow{
		{ID: "P1", Name: "Major Incident Workflow"},
		{ID: "P2", Name: "🔻 Major Incident Workflow"},
		{ID: "P3", Name: "Rebuild"},
		{ID: "P4", Name: "rebuild"},
		{ID: "P5", Name: "Deploy"},
	}
	got, err := selectWorkflows(all, []string{"major incident workflow", "P5", "P5"})
	if err != nil || len(got) != 2 || got[0].ID != "P1" || got[1].ID != "P5" {
		t.Errorf("case-insensitive name + ID + duplicate: %v %v", got, err)
	}
	if _, err := selectWorkflows(all, []string{"Rebuild"}); err == nil || !strings.Contains(err.Error(), "P3, P4") {
		t.Errorf("ambiguous name should list both IDs: %v", err)
	}
	if _, err := selectWorkflows(all, []string{"Major"}); err == nil || !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "P2") {
		t.Errorf("partial name should suggest: %v", err)
	}
	if _, err := selectWorkflows(all, []string{"nothing", "also nothing"}); err == nil || strings.Count(err.Error(), "no workflow") != 2 {
		t.Errorf("every bad selector should be reported: %v", err)
	}
	if got, _ := selectWorkflows(all, nil); len(got) != len(all) {
		t.Errorf("no selectors = all")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"🔐 Security Incident Workflow": "security-incident-workflow",
		"Rollback Main Application ":   "rollback-main-application",
		"SLA - SET TIMER (P1)":         "sla-set-timer-p1",
		"🔻":                            "workflow",
		strings.Repeat("abc ", 30):     strings.TrimRight(strings.Repeat("abc-", 15), "-"),
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollectFiles(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	_ = os.Mkdir(empty, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "b.yaml"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a.YML"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644)
	got, err := collectFiles([]string{dir})
	if err != nil || len(got) != 2 || !strings.HasSuffix(got[0], "a.YML") {
		t.Errorf("folder: %v %v", got, err)
	}
	if _, err := collectFiles([]string{empty}); err == nil || !strings.Contains(err.Error(), "no .yaml") {
		t.Errorf("empty folder: %v", err)
	}
	if _, err := collectFiles([]string{filepath.Join(dir, "missing.yaml")}); err == nil {
		t.Error("missing path should fail")
	}
}

func TestExportNeverOverwritesWithoutForce(t *testing.T) {
	_, dir := setup(t)
	out := filepath.Join(dir, "out")
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	p := filepath.Join(out, "major-incident.yaml")
	_ = os.WriteFile(p, []byte("my edits"), 0o644)
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err == nil || !strings.Contains(err.Error(), "already exist") {
		t.Fatalf("second export should refuse: %v\n%s", err, log)
	}
	if b, _ := os.ReadFile(p); string(b) != "my edits" {
		t.Fatal("file was overwritten without --force")
	}
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out, "--force"); err != nil {
		t.Fatalf("--force: %v\n%s", err, log)
	}
	if b, _ := os.ReadFile(p); string(b) == "my edits" {
		t.Fatal("--force did not overwrite")
	}
}

func TestDefaultExportFolderIsNewEachTime(t *testing.T) {
	_, dir := setup(t)
	t.Chdir(dir)
	log, err := pdwf(t, "export", "--token", "src-key")
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "pd-workflows-acme-*", "major-incident.yaml"))
	if len(matches) != 1 {
		t.Fatalf("expected a pd-workflows-acme-<timestamp> folder, got %v\n%s", matches, log)
	}
}

func TestImportExistingSkipWritesNothing(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "out")
	_, _ = pdwf(t, "export", "--token", "src-key", "--out", out)
	if log, err := pdwf(t, "import", "--token", "dst-key", "--yes", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	before := len(fake.writes)
	log, err := pdwf(t, "import", "--token", "dst-key", "--yes", "--existing", "skip", out)
	if err != nil || !strings.Contains(log, "SKIP") || len(fake.writes) != before {
		t.Fatalf("--existing skip wrote %d times (%v)\n%s", len(fake.writes)-before, err, log)
	}
}

func TestImportRefusesDuplicateNames(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "out")
	_, _ = pdwf(t, "export", "--token", "src-key", "--out", out)
	data, _ := os.ReadFile(filepath.Join(out, "major-incident.yaml"))
	_ = os.WriteFile(filepath.Join(out, "copy.yaml"), data, 0o644)
	if log, err := pdwf(t, "import", "--token", "dst-key", "--yes", out); err == nil || !strings.Contains(log, "both define workflow") {
		t.Fatalf("two files, same workflow name: %v\n%s", err, log)
	}
	_ = os.Remove(filepath.Join(out, "copy.yaml"))

	dst := fake.accounts["dst-key"]
	dst.workflows = []map[string]any{{"id": "PA", "name": "🔻 Major Incident"}, {"id": "PB", "name": "🔻 major incident"}}
	log, err := pdwf(t, "import", "--token", "dst-key", "--yes", out)
	if err == nil || !strings.Contains(log, "already has 2 workflows named") {
		t.Fatalf("target with duplicate names: %v\n%s", err, log)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("wrote despite errors: %v", fake.writes)
	}
}

func TestImportPartialFailureReportsAndExits1(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "out")
	_, _ = pdwf(t, "export", "--token", "src-key", "--out", out)
	// A second workflow that uses an action the target refuses at write time.
	src := fake.accounts["src-key"]
	src.workflows = append(src.workflows, map[string]any{"id": "PK8S", "name": "K8s flow", "steps": []any{
		map[string]any{"name": "List pods", "action_configuration": map[string]any{"action_id": "pagerduty.com:kubernetes-clusters:list-objects:2", "inputs": []any{}}},
	}})
	src.actions = append(src.actions, "pagerduty.com:kubernetes-clusters:list-objects:2")
	_, _ = pdwf(t, "export", "--token", "src-key", "--out", out, "--force")
	dst := fake.accounts["dst-key"]
	dst.actions = append(dst.actions, "pagerduty.com:kubernetes-clusters:list-objects:2") // lookup says OK...
	dst.writeRejects = []string{"pagerduty.com:kubernetes-clusters:list-objects:2"}       // ...create refuses

	log, err := pdwf(t, "import", "--token", "dst-key", "--yes", out)
	if err == nil {
		t.Fatalf("expected exit 1 when a workflow fails\n%s", log)
	}
	if len(dst.workflows) != 1 || !strings.Contains(log, "1 workflow of 2 failed") || !strings.Contains(log, "nothing was changed") {
		t.Fatalf("expected the other workflow imported and a clear message\n%s", log)
	}
}

func TestBackupUndoesAnUpdate(t *testing.T) {
	fake, dir := setup(t)
	out := filepath.Join(dir, "out")
	_, _ = pdwf(t, "export", "--token", "src-key", "--out", out)
	_, _ = pdwf(t, "import", "--token", "dst-key", "--yes", out)
	dst := fake.accounts["dst-key"]
	original := fmt.Sprint(dst.workflows[0]["steps"])

	p := filepath.Join(out, "major-incident.yaml")
	data, _ := os.ReadFile(p)
	_ = os.WriteFile(p, []byte(strings.Replace(string(data), "P1 path", "CHANGED", 1)), 0o644)
	backups := filepath.Join(dir, "backups")
	if log, err := pdwf(t, "import", "--token", "dst-key", "--yes", "--backup-dir", backups, p); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if !strings.Contains(fmt.Sprint(dst.workflows[0]["steps"]), "CHANGED") {
		t.Fatal("update did not apply")
	}
	if log, err := pdwf(t, "import", "--token", "dst-key", "--yes", "--backup-dir", filepath.Join(dir, "b2"), backups); err != nil {
		t.Fatalf("re-importing the backup: %v\n%s", err, log)
	}
	if got := fmt.Sprint(dst.workflows[0]["steps"]); got != original {
		t.Fatalf("backup did not restore the original steps:\n got  %s\n want %s", got, original)
	}
}

func TestTokenPrecedence(t *testing.T) {
	fake, _ := setup(t)
	t.Setenv("PAGERDUTY_TOKEN", "dst-key")
	log, err := pdwf(t, "check")
	if err != nil || !strings.Contains(log, "acme-staging") || !strings.Contains(log, "PAGERDUTY_TOKEN") {
		t.Fatalf("env var should be used and announced: %v\n%s", err, log)
	}
	log, err = pdwf(t, "check", "--token", "src-key")
	if err != nil || !strings.Contains(log, "Connected to acme.pagerduty.com") {
		t.Fatalf("--token should win over the env var: %v\n%s", err, log)
	}
	_ = fake
}

// regionServers points pd's US and EU base URLs at two fakes and disables the
// PDWF_API_URL override, so connect's real region logic runs.
func regionServers(t *testing.T, us, eu http.Handler) {
	t.Helper()
	usSrv, euSrv := httptest.NewServer(us), httptest.NewServer(eu)
	t.Cleanup(usSrv.Close)
	t.Cleanup(euSrv.Close)
	oldUS, oldEU := pd.USBaseURL, pd.EUBaseURL
	pd.USBaseURL, pd.EUBaseURL = usSrv.URL, euSrv.URL
	t.Cleanup(func() { pd.USBaseURL, pd.EUBaseURL = oldUS, oldEU })
	t.Setenv("PDWF_API_URL", "")
	t.Setenv("PAGERDUTY_TOKEN", "")
}

func accountHandler(host string, hits *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.Header.Get("Authorization") != "Token token=good" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"Unauthorized"}}`)
			return
		}
		fmt.Fprintf(w, `{"incident_workflows":[{"id":"P1","name":"x","html_url":"https://%s/incident-workflows/P1"}],"more":false}`, host)
	}
}

func TestRegionFallbackToEU(t *testing.T) {
	var usHits, euHits int32
	us := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&usHits, 1)
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"Unauthorized"}}`)
	})
	regionServers(t, us, accountHandler("acme.eu.pagerduty.com", &euHits))
	log, err := pdwf(t, "check", "--token", "good")
	if err != nil || !strings.Contains(log, "acme.eu.pagerduty.com [EU]") || euHits == 0 {
		t.Fatalf("expected fallback to EU: %v (us %d, eu %d)\n%s", err, usHits, euHits, log)
	}
}

func TestUSAPIAnsweringForEUAccountSwitchesToEU(t *testing.T) {
	var usHits, euHits int32
	regionServers(t, accountHandler("acme.eu.pagerduty.com", &usHits), accountHandler("acme.eu.pagerduty.com", &euHits))
	log, err := pdwf(t, "list", "--token", "good")
	if err != nil || !strings.Contains(log, "[EU]") {
		t.Fatalf("expected EU label: %v\n%s", err, log)
	}
	if euHits < 2 { // ping + the actual list must go to the EU API
		t.Fatalf("expected the work to go to the EU API, got us=%d eu=%d", usHits, euHits)
	}
}

func TestForcedRegionIsRespected(t *testing.T) {
	var usHits, euHits int32
	regionServers(t, accountHandler("acme.eu.pagerduty.com", &usHits), accountHandler("acme.eu.pagerduty.com", &euHits))
	if log, err := pdwf(t, "check", "--token", "good", "--region", "us"); err != nil || euHits != 0 {
		t.Fatalf("--region us must not touch the EU API (eu hits %d): %v\n%s", euHits, err, log)
	}
}

func TestConfirmationWordDistinguishesUSAndEU(t *testing.T) {
	us := &Target{Host: "pdt-justyn.pagerduty.com"}
	eu := &Target{Host: "pdt-justyn.eu.pagerduty.com"}
	if us.Subdomain() == eu.Subdomain() || eu.Subdomain() != "pdt-justyn.eu" {
		t.Fatalf("US %q and EU %q confirmation words must differ", us.Subdomain(), eu.Subdomain())
	}
}

func TestHelpEverywhere(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"help", "import"}, {"help", "push"}, {"help", "keys"}, {"help", "format"}, {"version"}} {
		log, err := pdwf(t, args...)
		if err != nil || len(log) < 50 {
			t.Errorf("%v: %v (%d bytes)", args, err, len(log))
		}
	}
	for _, cmd := range []string{"list", "export", "import", "validate", "check"} {
		log, err := pdwf(t, cmd, "-h")
		if err != errHelpShown || !strings.Contains(log, "All options:") || !strings.Contains(log, "pdwf "+cmd) {
			t.Errorf("%s -h: %v\n%s", cmd, err, log)
		}
	}
	if _, err := pdwf(t, "help", "nope"); err == nil {
		t.Error("unknown help topic should be a usage error")
	}
	if _, err := pdwf(t, "frobnicate"); err == nil {
		t.Error("unknown command should be a usage error")
	}
}

func TestEveryFlagIsDocumentedInHelp(t *testing.T) {
	for _, cmd := range []string{"list", "export", "import", "validate", "check"} {
		log, _ := pdwf(t, cmd, "-h")
		body := log[:strings.Index(log, "All options:")]
		opts := log[strings.Index(log, "All options:"):]
		for _, line := range strings.Split(opts, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "-") {
				continue
			}
			name := strings.Fields(line)[0][1:]
			if name == "token" || name == "region" {
				continue // shared options, explained in "pdwf help keys"
			}
			if !strings.Contains(body, "--"+name) {
				t.Errorf("%s: flag --%s is not explained in the help text", cmd, name)
			}
		}
	}
}

func TestExportLooksUpNamesPagerDutyLeftOut(t *testing.T) {
	fake, dir := setup(t)
	src := fake.accounts["src-key"]
	// Strip the human names PagerDuty normally sends, forcing the lookups.
	src.workflows[0]["team"] = map[string]any{"id": "PTEAMSRC", "type": "team_reference"}
	src.triggers[0]["services"] = []any{map[string]any{"id": "PSVCSRC1", "type": "service_reference"}}
	out := filepath.Join(dir, "out")
	if log, err := pdwf(t, "export", "--token", "src-key", "--out", out); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	data, _ := os.ReadFile(filepath.Join(out, "major-incident.yaml"))
	if !strings.Contains(string(data), "team: SRE") || !strings.Contains(string(data), "- Checkout API") {
		t.Fatalf("names should be looked up via /teams and /services:\n%s", data)
	}
}

func TestValidateGoodAndBadFolders(t *testing.T) {
	_, dir := setup(t)
	out := filepath.Join(dir, "out")
	_, _ = pdwf(t, "export", "--token", "src-key", "--out", out)
	log, err := pdwf(t, "validate", out)
	if err != nil || !strings.Contains(log, "All files look good") || !strings.Contains(log, "3 steps") {
		t.Fatalf("good folder (2 top-level + 1 nested step): %v\n%s", err, log)
	}
	_ = os.WriteFile(filepath.Join(out, "broken.yaml"), []byte("format: pdwf/v1\nworkflow:\n  name: \"\"\n"), 0o644)
	if log, err := pdwf(t, "validate", out); err == nil || !strings.Contains(log, "workflow.name is empty") {
		t.Fatalf("bad file in folder: %v\n%s", err, log)
	}
	if _, err := pdwf(t, "validate"); err == nil {
		t.Fatal("validate with no path should be a usage error")
	}
}
