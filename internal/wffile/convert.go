package wffile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/justynroberts/workflow-utility/internal/pd"
)

// FromAPI converts an API workflow and its triggers into a file.
// serviceNames maps service ID to name; unknown IDs are written as-is.
func FromAPI(w pd.Workflow, triggers []pd.Trigger, teamName string, serviceNames map[string]string) *File {
	f := &File{
		Format: Format,
		Workflow: Workflow{
			Name:        w.Name,
			Description: w.Description,
			Enabled:     w.IsEnabled,
			Steps:       stepsFromAPI(w.Steps),
		},
	}
	if w.Team != nil && w.Team.ID != "" {
		f.Workflow.Team = teamName
		if f.Workflow.Team == "" {
			f.Workflow.Team = w.Team.ID
		}
	}
	for _, t := range triggers {
		ft := Trigger{Type: t.TriggerType, AllServices: t.IsSubscribedToAllServices, IncidentTypes: t.IncidentTypes}
		if t.IsDisabled != nil {
			ft.Disabled = *t.IsDisabled
		}
		if t.Condition != nil {
			ft.Condition = *t.Condition
		}
		if !t.IsSubscribedToAllServices {
			for _, s := range t.Services {
				name := serviceNames[s.ID]
				if name == "" {
					name = s.Label()
				}
				if name == "" {
					name = s.ID
				}
				ft.Services = append(ft.Services, name)
			}
			sort.Strings(ft.Services)
		}
		f.Triggers = append(f.Triggers, ft)
	}
	return f
}

func stepsFromAPI(in []pd.Step) []Step {
	out := make([]Step, 0, len(in))
	for _, s := range in {
		fs := Step{
			Name:              s.Name,
			Description:       s.Description,
			Action:            s.ActionConfiguration.ActionID,
			ActionDescription: s.ActionConfiguration.Description,
		}
		for _, in := range s.ActionConfiguration.Inputs {
			fs.Inputs = append(fs.Inputs, KV{in.Name, in.Value})
		}
		for _, b := range s.ActionConfiguration.InlineStepsInputs {
			fs.InlineSteps = append(fs.InlineSteps, InlineBlock{b.Name, stepsFromAPI(b.Value.Steps)})
		}
		out = append(out, fs)
	}
	return out
}

// Resolver turns names in a file into IDs in the target account.
type Resolver struct {
	Teams    []pd.Named
	Services []pd.Named
}

// resolve finds exactly one match by ID, then by case-insensitive name.
func resolve(kind, want string, items []pd.Named) (string, error) {
	for _, it := range items {
		if it.ID == want {
			return it.ID, nil
		}
	}
	var hits []pd.Named
	for _, it := range items {
		if strings.EqualFold(strings.TrimSpace(it.Name), strings.TrimSpace(want)) {
			hits = append(hits, it)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].ID, nil
	case 0:
		msg := fmt.Sprintf("%s %q does not exist in the target account", kind, want)
		if s := suggest(want, items); s != "" {
			msg += fmt.Sprintf(" (did you mean %s?)", s)
		}
		return "", fmt.Errorf("%s", msg)
	default:
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.ID
		}
		return "", fmt.Errorf("%s name %q matches %d %ss (%s); put the ID in the file instead", kind, want, len(hits), kind, strings.Join(ids, ", "))
	}
}

func suggest(want string, items []pd.Named) string {
	w := strings.ToLower(want)
	var s []string
	for _, it := range items {
		n := strings.ToLower(it.Name)
		if strings.Contains(n, w) || strings.Contains(w, n) {
			s = append(s, fmt.Sprintf("%q", it.Name))
		}
		if len(s) == 3 {
			break
		}
	}
	return strings.Join(s, " or ")
}

// ToAPI builds the API workflow and triggers (without workflow IDs) for a
// file, resolving every name. It returns all resolution errors at once.
func (r *Resolver) ToAPI(f *File) (pd.Workflow, []pd.Trigger, []error) {
	var errs []error
	w := pd.Workflow{
		Name:        f.Workflow.Name,
		Description: f.Workflow.Description,
		IsEnabled:   f.Workflow.Enabled,
		Steps:       stepsToAPI(f.Workflow.Steps),
	}
	if f.Workflow.Team != "" {
		id, err := resolve("team", f.Workflow.Team, r.Teams)
		if err != nil {
			errs = append(errs, err)
		} else {
			w.Team = &pd.Reference{ID: id, Type: "team_reference"}
		}
	}
	var triggers []pd.Trigger
	for i, t := range f.Triggers {
		at := pd.Trigger{TriggerType: t.Type, IsSubscribedToAllServices: t.AllServices, IncidentTypes: t.IncidentTypes}
		if t.Disabled {
			d := true
			at.IsDisabled = &d
		}
		if t.Condition != "" {
			c := t.Condition
			at.Condition = &c
		}
		for _, s := range t.Services {
			id, err := resolve("service", s, r.Services)
			if err != nil {
				errs = append(errs, fmt.Errorf("triggers[%d]: %w", i+1, err))
				continue
			}
			at.Services = append(at.Services, pd.Reference{ID: id, Type: "service_reference"})
		}
		triggers = append(triggers, at)
	}
	return w, triggers, errs
}

func stepsToAPI(in []Step) []pd.Step {
	out := make([]pd.Step, 0, len(in))
	for _, s := range in {
		ps := pd.Step{
			Name:        s.Name,
			Description: s.Description,
			ActionConfiguration: pd.ActionConfiguration{
				ActionID:    s.Action,
				Description: s.ActionDescription,
				Inputs:      []pd.Input{},
			},
		}
		for _, kv := range s.Inputs {
			// PagerDuty returns unset optional inputs as "" but rejects "" on write.
			if kv.Value == nil || kv.Value == "" {
				continue
			}
			ps.ActionConfiguration.Inputs = append(ps.ActionConfiguration.Inputs, pd.Input{Name: kv.Key, Value: kv.Value})
		}
		for _, b := range s.InlineSteps {
			ps.ActionConfiguration.InlineStepsInputs = append(ps.ActionConfiguration.InlineStepsInputs,
				pd.InlineStepsInput{Name: b.Name, Value: pd.InlineSteps{Steps: stepsToAPI(b.Steps)}})
		}
		out = append(out, ps)
	}
	return out
}

// TriggerKey identifies a trigger for "is this already there?" checks.
func TriggerKey(t pd.Trigger) string {
	c := ""
	if t.Condition != nil {
		c = strings.TrimSpace(*t.Condition)
	}
	types := append([]string(nil), t.IncidentTypes...)
	sort.Strings(types)
	return t.TriggerType + "\x00" + c + "\x00" + strings.Join(types, ",")
}
