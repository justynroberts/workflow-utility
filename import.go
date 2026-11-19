package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justynroberts/workflow-utility/internal/pd"
	"github.com/justynroberts/workflow-utility/internal/wffile"
)

// collectFiles expands files and folders into a sorted list of .yaml/.yml files.
func collectFiles(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("cannot open %s: %w", p, err)
		}
		if !st.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
				out = append(out, filepath.Join(p, e.Name()))
				n++
			}
		}
		if n == 0 {
			return nil, fmt.Errorf("folder %s has no .yaml files in it", p)
		}
	}
	sort.Strings(out)
	return out, nil
}

type loaded struct {
	path string
	file *wffile.File
}

// loadAll parses and validates every file, reporting every problem at once.
func (a *App) loadAll(paths []string) ([]loaded, bool) {
	files, err := collectFiles(paths)
	if err != nil {
		a.say("🔴 %s", err)
		return nil, false
	}
	ok := true
	var out []loaded
	byName := map[string]string{}
	for _, p := range files {
		f, err := wffile.Load(p)
		if err != nil {
			a.say("🔴 %s", err)
			ok = false
			continue
		}
		if errs := wffile.Validate(f); len(errs) > 0 {
			a.say("🔴 %s has problems:\n%s", p, wffile.JoinErrors(errs))
			ok = false
			continue
		}
		key := strings.ToLower(f.Workflow.Name)
		if prev, dup := byName[key]; dup {
			a.say("🔴 %s and %s both define workflow %q; import one or rename it", prev, p, f.Workflow.Name)
			ok = false
			continue
		}
		byName[key] = p
		out = append(out, loaded{p, f})
	}
	return out, ok
}

func (a *App) cmdValidate(args []string) error {
	fs := newFlagSet("validate", "validate FILE-or-FOLDER ...")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{"give a file or folder to check, e.g. pdwf validate pd-workflows-acme-20261119-120000"}
	}
	items, ok := a.loadAll(pos)
	for _, it := range items {
		a.say("🟢 %s: %q, %s, %s", it.path, it.file.Workflow.Name, plural(countSteps(it.file.Workflow.Steps), "step"), plural(len(it.file.Triggers), "trigger"))
		for _, w := range wffile.Warnings(it.file) {
			a.say("   🟡 %s", w)
		}
	}
	if !ok {
		return errSilent
	}
	a.say("All files look good. (Team and service names are checked against the account at import time.)")
	return nil
}

func countSteps(s []wffile.Step) int {
	n := len(s)
	for _, st := range s {
		for _, b := range st.InlineSteps {
			n += countSteps(b.Steps)
		}
	}
	return n
}

// decideEnabled works out the on/off state to send, and a label for the plan.
//
//	default    new workflows start OFF; updated ones keep their current state
//	--enable   use the file's enabled value (the state it had in the source)
//	--disable  everything ends up OFF
func decideEnabled(action string, inFile, current *bool, enable, disable bool) (*bool, string) {
	on, off := true, false
	switch {
	case disable:
		return &off, "OFF"
	case enable:
		if inFile == nil {
			if action == "update" {
				return current, onOff(current, "unchanged")
			}
			return nil, "PagerDuty default"
		}
		if current != nil && *current == *inFile {
			return inFile, onOff(inFile, "") + " (unchanged)"
		}
		return inFile, onOff(inFile, "")
	case action == "create":
		return &off, "OFF (new)"
	default:
		if current == nil {
			return nil, "unchanged"
		}
		if *current {
			return &on, "ON (unchanged)"
		}
		return &off, "OFF (unchanged)"
	}
}

func onOff(b *bool, unknown string) string {
	switch {
	case b == nil:
		return unknown
	case *b:
		return "ON"
	default:
		return "OFF"
	}
}

// autoRuns reports whether a trigger starts the workflow without a human.
func autoRuns(triggers []pd.Trigger) bool {
	for _, t := range triggers {
		if t.TriggerType != "manual" {
			return true
		}
	}
	return false
}

type planItem struct {
	state    string // on/off label for the plan
	src      loaded
	action   string // create, update, skip
	existing *pd.Workflow
	workflow pd.Workflow
	triggers []pd.Trigger
}

func (a *App) cmdImport(ctx context.Context, args []string) error {
	fs := newFlagSet("import", "import [--token KEY] [--dry-run] [--yes] FILE-or-FOLDER ...")
	cf := addConnFlags(fs)
	dryRun := fs.Bool("dry-run", false, "show what would happen, change nothing")
	yes := fs.Bool("yes", false, "do not ask for confirmation (for scripts)")
	onExisting := fs.String("existing", "update", "when a workflow with the same name exists: update or skip")
	noTriggers := fs.Bool("no-triggers", false, "do not create triggers")
	enable := fs.Bool("enable", false, "set each workflow on/off as in its file (the source account's state)")
	disable := fs.Bool("disable", false, "turn every imported workflow off, including existing ones being updated")
	backupDir := fs.String("backup-dir", "", "where to save copies of workflows before updating them (default: a new timestamped folder)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{"give the exported file(s) or folder to import, e.g. pdwf import pd-workflows-acme-20261119-120000"}
	}
	if *onExisting != "update" && *onExisting != "skip" {
		return usageError{"--existing must be update or skip"}
	}
	if *enable && *disable {
		return usageError{"use --enable or --disable, not both"}
	}

	// 1. Files first: no point connecting if they are broken.
	items, ok := a.loadAll(pos)
	if !ok {
		return fmt.Errorf("nothing imported; fix the files above and re-run (pdwf validate checks them offline)")
	}

	// 2. Look at the target account and resolve every name before writing anything.
	t, err := a.connect(ctx, cf, "import INTO")
	if err != nil {
		return err
	}
	same := 0
	for _, it := range items {
		if src := it.file.Source; src != nil && t.Host != "" && strings.EqualFold(src.Account, t.Host) {
			same++
		}
	}
	if same > 0 {
		a.say("🟡 Note: %s of %d were exported from this same account (%s).", plural(same, "file"), len(items), t.Host)
	}
	existing, err := t.Client.ListWorkflows(ctx)
	if err != nil {
		return err
	}
	byName := map[string][]pd.Workflow{}
	for _, w := range existing {
		k := strings.ToLower(strings.TrimSpace(w.Name))
		byName[k] = append(byName[k], w)
	}
	res := &wffile.Resolver{}
	needTeams, needServices := false, false
	for _, it := range items {
		needTeams = needTeams || it.file.Workflow.Team != ""
		for _, tr := range it.file.Triggers {
			needServices = needServices || len(tr.Services) > 0
		}
	}
	if needTeams {
		if res.Teams, err = t.Client.ListTeams(ctx); err != nil {
			return fmt.Errorf("reading teams: %w", err)
		}
	}
	if needServices && !*noTriggers {
		if res.Services, err = t.Client.ListServices(ctx); err != nil {
			return fmt.Errorf("reading services: %w", err)
		}
	}

	var plan []planItem
	var problems []string
	for _, it := range items {
		pi := planItem{src: it}
		f := *it.file
		if *noTriggers {
			f.Triggers = nil
		}
		w, trs, errs := res.ToAPI(&f)
		for _, e := range errs {
			problems = append(problems, fmt.Sprintf("%s: %s", it.path, e))
		}
		pi.workflow, pi.triggers = w, trs
		matches := byName[strings.ToLower(strings.TrimSpace(it.file.Workflow.Name))]
		switch {
		case len(matches) > 1:
			problems = append(problems, fmt.Sprintf("%s: the target already has %d workflows named %q; rename one in PagerDuty or in the file", it.path, len(matches), it.file.Workflow.Name))
		case len(matches) == 1:
			m := matches[0]
			pi.existing = &m
			pi.action = *onExisting
		default:
			pi.action = "create"
		}
		if pi.action != "skip" {
			var current *bool
			if pi.action == "update" {
				cur, _, err := t.Client.GetWorkflow(ctx, pi.existing.ID)
				if err != nil {
					return fmt.Errorf("reading current %q: %w", pi.existing.Name, err)
				}
				current = cur.IsEnabled
			}
			pi.workflow.IsEnabled, pi.state = decideEnabled(pi.action, it.file.Workflow.Enabled, current, *enable, *disable)
		}
		plan = append(plan, pi)
	}
	if len(problems) > 0 {
		a.say("🔴 Nothing imported. These must be fixed first:")
		for _, p := range problems {
			a.say("  - %s", p)
		}
		return errSilent
	}

	// 3. Show the plan.
	counts := map[string]int{}
	a.say("\nPlan for %s:", t.Label())
	for _, pi := range plan {
		counts[pi.action]++
		detail := fmt.Sprintf("%s, %s", plural(len(pi.workflow.Steps), "step"), plural(len(pi.triggers), "trigger"))
		switch pi.action {
		case "create":
			a.say("  + CREATE  %-40q %s  -> %s", pi.workflow.Name, detail, pi.state)
		case "update":
			a.say("  ~ UPDATE  %-40q %s  -> %s  (replaces the steps of %s; existing triggers are kept)", pi.workflow.Name, detail, pi.state, pi.existing.ID)
		case "skip":
			a.say("  = SKIP    %-40q already exists (%s)", pi.workflow.Name, pi.existing.ID)
		}
		if pi.action != "skip" && pi.workflow.IsEnabled != nil && *pi.workflow.IsEnabled && !strings.Contains(pi.state, "unchanged") && autoRuns(pi.triggers) {
			a.say("      🟡 will be ON with an automatic trigger: it will start running on matching incidents in %s straight away", t.Host)
		}
		for _, w := range wffile.Warnings(pi.src.file) {
			a.say("      🟡 %s", w)
		}
	}
	a.say("\n%d to create, %d to update, %d to skip. Nothing is ever deleted.", counts["create"], counts["update"], counts["skip"])
	switch {
	case *disable:
		a.say("On/off: --disable, so every imported workflow will be OFF.")
	case *enable:
		a.say("On/off: --enable, so each workflow is set as in its file.")
	default:
		a.say("On/off: new workflows start OFF, updated ones keep their current state. Use --enable to copy the on/off state from the files, or --disable to turn all of them off.")
	}
	if counts["create"]+counts["update"] == 0 {
		a.say("Nothing to do.")
		return nil
	}
	if *dryRun {
		a.say("Dry run: no changes made. Run again without --dry-run to apply.")
		return nil
	}

	// 4. Confirm by typing the account name, which guards against the wrong key.
	if !*yes {
		if !a.isInteractive() {
			return fmt.Errorf("not running in a terminal, so cannot ask for confirmation; add --yes to apply, or --dry-run to preview")
		}
		want := t.Subdomain()
		prompt := fmt.Sprintf("Type the account name %q to apply these changes to %s: ", want, t.Host)
		if want == "" {
			want = "yes"
			prompt = fmt.Sprintf("Type \"yes\" to apply these changes to %s: ", t.Label())
		}
		ans, err := a.readLine(prompt)
		if err != nil || !strings.EqualFold(strings.TrimSpace(ans), want) {
			a.say("Cancelled. Nothing changed.")
			return nil
		}
	}

	// 5. Back up anything we are about to overwrite.
	if counts["update"] > 0 {
		dir, err := a.backup(ctx, t, plan, *backupDir)
		if err != nil {
			return fmt.Errorf("could not save backups, so nothing was changed: %w", err)
		}
		a.say("Backed up %s to %s/ (re-import from there to undo).", plural(counts["update"], "workflow"), dir)
	}

	// 6. Apply. One failure does not stop the others.
	failed := 0
	for _, pi := range plan {
		if pi.action == "skip" {
			continue
		}
		if err := a.apply(ctx, t, pi); err != nil {
			failed++
			a.say("🔴 %q: %s", pi.workflow.Name, err)
		}
	}
	if failed > 0 {
		a.say("\n%s of %d failed; any others were applied. Fix the problem and re-run the same command; already-imported workflows will just be updated.", plural(failed, "workflow"), counts["create"]+counts["update"])
		return errSilent
	}
	a.say("\n🟢 Done.")
	return nil
}

func (a *App) apply(ctx context.Context, t *Target, pi planItem) error {
	var wfID string
	var current []pd.Trigger
	switch pi.action {
	case "create":
		created, err := t.Client.CreateWorkflow(ctx, pi.workflow)
		if err != nil {
			return err
		}
		wfID = created.ID
		a.say("🟢 created %q (%s)", pi.workflow.Name, wfID)
	case "update":
		if _, err := t.Client.UpdateWorkflow(ctx, pi.existing.ID, pi.workflow); err != nil {
			return err
		}
		wfID = pi.existing.ID
		a.say("🟢 updated %q (%s)", pi.workflow.Name, wfID)
		if len(pi.triggers) > 0 {
			var err error
			if current, _, err = t.Client.ListTriggers(ctx, wfID); err != nil {
				return fmt.Errorf("workflow updated, but could not read its triggers: %w", err)
			}
		}
	}

	have := map[string]pd.Trigger{}
	for _, tr := range current {
		have[wffile.TriggerKey(tr)] = tr
	}
	for _, tr := range pi.triggers {
		desc := tr.TriggerType
		if tr.Condition != nil {
			desc += " when " + *tr.Condition
		}
		if len(tr.IncidentTypes) > 0 {
			desc += " for " + strings.Join(tr.IncidentTypes, ", ")
		}
		if ex, ok := have[wffile.TriggerKey(tr)]; ok {
			added := 0
			if !ex.IsSubscribedToAllServices {
				on := map[string]bool{}
				for _, s := range ex.Services {
					on[s.ID] = true
				}
				for _, s := range tr.Services {
					if !on[s.ID] {
						if err := t.Client.AddTriggerService(ctx, ex.ID, s.ID); err != nil {
							return fmt.Errorf("adding service %s to trigger %s: %w", s.ID, ex.ID, err)
						}
						added++
					}
				}
			}
			if added > 0 {
				a.say("     trigger %s: already there, added %s", desc, plural(added, "service"))
			} else {
				a.say("     trigger %s: already there", desc)
			}
			continue
		}
		tr.Workflow = &pd.WorkflowRef{ID: wfID} // PagerDuty rejects "type" here
		if _, err := t.Client.CreateTrigger(ctx, tr); err != nil {
			return fmt.Errorf("workflow saved, but trigger (%s) failed: %w", desc, err)
		}
		a.say("     trigger %s: created", desc)
	}
	return nil
}

func (a *App) backup(ctx context.Context, t *Target, plan []planItem, dir string) (string, error) {
	if dir == "" {
		label := t.Subdomain()
		if label == "" {
			label = "target"
		}
		dir = fmt.Sprintf("pdwf-backup-%s-%s", slug(label), time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, pi := range plan {
		if pi.action != "update" {
			continue
		}
		w, _, err := t.Client.GetWorkflow(ctx, pi.existing.ID)
		if err != nil {
			return "", err
		}
		if w.Team == nil {
			w.Team = pi.existing.Team
		}
		triggers, _, _ := t.Client.ListTriggers(ctx, pi.existing.ID)
		svc := map[string]string{}
		for _, tr := range triggers {
			for _, s := range tr.Services {
				svc[s.ID] = s.Label()
			}
		}
		f := wffile.FromAPI(w, triggers, w.Team.Label(), svc)
		f.ExportedAt = time.Now().UTC().Format(time.RFC3339)
		f.Source = &wffile.Source{Account: t.Host, Region: t.Region, WorkflowID: w.ID, URL: w.HTMLURL}
		p := filepath.Join(dir, slug(w.Name)+"-"+w.ID+".yaml")
		data, err := wffile.Marshal(f, p)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}
