package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/justynroberts/workflow-utility/internal/pd"
	"github.com/justynroberts/workflow-utility/internal/wffile"
)

func (a *App) cmdList(ctx context.Context, args []string) error {
	fs := newFlagSet("list", "list [--token KEY] [SEARCH]")
	cf := addConnFlags(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	t, err := a.connect(ctx, cf, "list workflows from")
	if err != nil {
		return err
	}
	all, err := t.Client.ListWorkflows(ctx)
	if err != nil {
		return err
	}
	search := strings.ToLower(strings.Join(pos, " "))
	tw := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tNAME\tTEAM\tID")
	n := 0
	for _, w := range all {
		if search != "" && !strings.Contains(strings.ToLower(w.Name), search) && !strings.EqualFold(w.ID, search) {
			continue
		}
		n++
		team := w.Team.Label()
		if team == "" && w.Team != nil {
			team = w.Team.ID
		}
		if team == "" {
			team = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", n, w.Name, team, w.ID)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	a.say("\n%s. Export one with: pdwf export \"<name or ID>\"   (or no name for all)", plural(n, "workflow"))
	return nil
}

// selectWorkflows matches each selector to a workflow by ID or exact name
// (case-insensitive). Every selector must match exactly one workflow.
func selectWorkflows(all []pd.Workflow, selectors []string) ([]pd.Workflow, error) {
	if len(selectors) == 0 {
		return all, nil
	}
	var out []pd.Workflow
	seen := map[string]bool{}
	var problems []string
	for _, sel := range selectors {
		sel = strings.TrimSpace(sel)
		var hits []pd.Workflow
		for _, w := range all {
			if w.ID == sel {
				hits = []pd.Workflow{w}
				break
			}
			if strings.EqualFold(w.Name, sel) {
				hits = append(hits, w)
			}
		}
		switch len(hits) {
		case 0:
			msg := fmt.Sprintf("no workflow named or with ID %q", sel)
			var close []string
			for _, w := range all {
				if strings.Contains(strings.ToLower(w.Name), strings.ToLower(sel)) {
					close = append(close, fmt.Sprintf("%q (%s)", w.Name, w.ID))
				}
			}
			if len(close) > 0 {
				if len(close) > 5 {
					close = append(close[:5], "...")
				}
				msg += "; did you mean " + strings.Join(close, ", ")
			}
			problems = append(problems, msg)
		case 1:
			if !seen[hits[0].ID] {
				seen[hits[0].ID] = true
				out = append(out, hits[0])
			}
		default:
			ids := make([]string, len(hits))
			for i, h := range hits {
				ids[i] = h.ID
			}
			problems = append(problems, fmt.Sprintf("%d workflows are named %q; use the ID instead: %s", len(hits), sel, strings.Join(ids, ", ")))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n  "))
	}
	return out, nil
}

func (a *App) cmdExport(ctx context.Context, args []string) error {
	fs := newFlagSet("export", "export [--token KEY] [--out FOLDER] [NAME-or-ID ...]   (no names = export all)")
	cf := addConnFlags(fs)
	outDir := fs.String("out", "", "folder to write into (default: a new timestamped folder)")
	force := fs.Bool("force", false, "overwrite files that already exist in --out")
	noTriggers := fs.Bool("no-triggers", false, "do not export triggers")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	t, err := a.connect(ctx, cf, "export FROM")
	if err != nil {
		return err
	}
	all, err := t.Client.ListWorkflows(ctx)
	if err != nil {
		return err
	}
	chosen, err := selectWorkflows(all, pos)
	if err != nil {
		return fmt.Errorf("nothing exported:\n  %w\n  (see them all with: pdwf list)", err)
	}
	if len(chosen) == 0 {
		a.say("This account has no incident workflows. Nothing to export.")
		return nil
	}

	names := &nameCache{client: t.Client}
	type item struct {
		file *wffile.File
		name string
	}
	var items []item
	for _, w := range chosen {
		full, unknown, err := t.Client.GetWorkflow(ctx, w.ID)
		if err != nil {
			return fmt.Errorf("reading %q: %w", w.Name, err)
		}
		if len(unknown) > 0 {
			a.say("🟡 %q has fields this version of pdwf does not understand and will NOT export: %s", w.Name, strings.Join(unknown, ", "))
		}
		if full.Team == nil {
			full.Team = w.Team // the list call includes team; the get call may not
		}
		teamName := ""
		if full.Team != nil {
			teamName = full.Team.Label()
			if teamName == "" && w.Team != nil {
				teamName = w.Team.Label()
			}
			if teamName == "" {
				teamName = names.team(ctx, full.Team.ID)
			}
		}
		var triggers []pd.Trigger
		if !*noTriggers {
			var unknownT []string
			triggers, unknownT, err = t.Client.ListTriggers(ctx, w.ID)
			if err != nil {
				a.say("🟡 could not read triggers for %q, exporting it without them: %s", w.Name, err)
				triggers = nil
			}
			if len(unknownT) > 0 {
				a.say("🟡 triggers of %q have fields this version of pdwf does not understand and will NOT export: %s", w.Name, strings.Join(unknownT, ", "))
			}
		}
		svc := map[string]string{}
		for _, tr := range triggers {
			for _, s := range tr.Services {
				if l := s.Label(); l != "" {
					svc[s.ID] = l
				} else {
					svc[s.ID] = names.service(ctx, s.ID)
				}
			}
		}
		f := wffile.FromAPI(full, triggers, teamName, svc)
		f.ExportedAt = time.Now().UTC().Format(time.RFC3339)
		f.Source = &wffile.Source{Account: t.Host, Region: t.Region, WorkflowID: full.ID, URL: full.HTMLURL}
		items = append(items, item{f, full.Name})
	}

	dir := *outDir
	if dir == "" {
		label := t.Subdomain()
		if label == "" {
			label = "export"
		}
		dir = fmt.Sprintf("pd-workflows-%s-%s", slug(label), time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	used := map[string]bool{}
	var paths []string
	var clashes []string
	for _, it := range items {
		base := slug(it.name)
		fn := base + ".yaml"
		for i := 2; used[fn]; i++ {
			fn = fmt.Sprintf("%s-%d.yaml", base, i)
		}
		used[fn] = true
		p := filepath.Join(dir, fn)
		if _, err := os.Stat(p); err == nil && !*force {
			clashes = append(clashes, p)
		}
		paths = append(paths, p)
	}
	if len(clashes) > 0 {
		return fmt.Errorf("these files already exist, nothing written (add --force to overwrite, or pick another --out):\n  %s", strings.Join(clashes, "\n  "))
	}
	for i, it := range items {
		data, err := wffile.Marshal(it.file, paths[i])
		if err != nil {
			return err
		}
		if err := os.WriteFile(paths[i], data, 0o644); err != nil {
			return err
		}
		a.say("  wrote %s  (%s, %s)", paths[i], plural(len(it.file.Workflow.Steps), "step"), plural(len(it.file.Triggers), "trigger"))
	}
	a.say("\n🟢 Exported %s to %s/", plural(len(items), "workflow"), dir)
	a.say("Next: pdwf import %s   (asks for the target account's key, shows a plan, changes nothing until you confirm)", quoteArg(dir))
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		s = "workflow"
	}
	return s
}

// nameCache lazily loads team/service names only if the API didn't give them.
type nameCache struct {
	client   *pd.Client
	teams    map[string]string
	services map[string]string
}

func load(ctx context.Context, f func(context.Context) ([]pd.Named, error)) map[string]string {
	m := map[string]string{}
	items, err := f(ctx)
	if err != nil {
		return m
	}
	for _, it := range items {
		m[it.ID] = it.Name
	}
	return m
}

func (n *nameCache) team(ctx context.Context, id string) string {
	if n.teams == nil {
		n.teams = load(ctx, n.client.ListTeams)
	}
	return n.teams[id]
}

func (n *nameCache) service(ctx context.Context, id string) string {
	if n.services == nil {
		n.services = load(ctx, n.client.ListServices)
	}
	return n.services[id]
}
