package pd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type Reference struct {
	ID      string `json:"id"`
	Type    string `json:"type,omitempty"`
	Summary string `json:"summary,omitempty"`
	Name    string `json:"name,omitempty"`
	Self    string `json:"self,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// Label is the human name of a referenced object, if PagerDuty gave one.
func (r *Reference) Label() string {
	if r == nil {
		return ""
	}
	if r.Name != "" {
		return r.Name
	}
	return r.Summary
}

// Workflow is an incident workflow. Read-only fields are kept so a strict
// decode can spot genuinely unknown fields (see UnknownFields).
type Workflow struct {
	ID          string     `json:"id,omitempty"`
	Type        string     `json:"type,omitempty"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Team        *Reference `json:"team,omitempty"`
	IsEnabled   *bool      `json:"is_enabled,omitempty"`
	Steps       []Step     `json:"steps"`

	// Read-only, returned by GET and never sent back.
	Summary       string          `json:"summary,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	PublishedDate string          `json:"published_date,omitempty"`
	Self          string          `json:"self,omitempty"`
	HTMLURL       string          `json:"html_url,omitempty"`
	Triggers      json.RawMessage `json:"triggers,omitempty"`
}

type Step struct {
	ID                  string              `json:"id,omitempty"`
	Type                string              `json:"type,omitempty"`
	Name                string              `json:"name"`
	Description         string              `json:"description,omitempty"`
	ActionConfiguration ActionConfiguration `json:"action_configuration"`
}

type ActionConfiguration struct {
	ActionID          string             `json:"action_id"`
	Description       string             `json:"description,omitempty"`
	Inputs            []Input            `json:"inputs"`
	InlineStepsInputs []InlineStepsInput `json:"inline_steps_inputs,omitempty"`
	Outputs           json.RawMessage    `json:"outputs,omitempty"`
}

type Input struct {
	Name          string `json:"name"`
	Value         any    `json:"value"`
	ParameterType string `json:"parameter_type,omitempty"`
}

// InlineStepsInput carries the nested steps of branching/looping actions.
type InlineStepsInput struct {
	Name  string      `json:"name"`
	Value InlineSteps `json:"value"`
}

type InlineSteps struct {
	Steps []Step `json:"steps"`
}

type Trigger struct {
	ID                        string       `json:"id,omitempty"`
	Type                      string       `json:"type,omitempty"`
	TriggerType               string       `json:"trigger_type"`
	TriggerTypeName           string       `json:"trigger_type_name,omitempty"`
	Condition                 *string      `json:"condition,omitempty"`
	IncidentTypes             []string     `json:"incident_types,omitempty"`
	IsDisabled                *bool        `json:"is_disabled,omitempty"`
	Workflow                  *WorkflowRef `json:"workflow,omitempty"`
	WorkflowID                string       `json:"workflow_id,omitempty"`
	WorkflowName              string       `json:"workflow_name,omitempty"`
	Services                  []Reference  `json:"services,omitempty"`
	IsSubscribedToAllServices bool         `json:"is_subscribed_to_all_services"`
	Permissions               *struct {
		Restricted bool   `json:"restricted"`
		TeamID     string `json:"team_id,omitempty"`
	} `json:"permissions,omitempty"`
	TriggerURL string `json:"trigger_url,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	Self       string `json:"self,omitempty"`
}

type WorkflowRef struct {
	ID          string `json:"id"`
	Type        string `json:"type,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Published   *bool  `json:"published,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Self        string `json:"self,omitempty"`
	HTMLURL     string `json:"html_url,omitempty"`
}

type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListWorkflows returns every incident workflow (without steps), with team.
func (c *Client) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	var all []Workflow
	q := url.Values{"include[]": {"team"}}
	err := c.paginateOffset(ctx, "/incident_workflows", q, func(raw json.RawMessage) (bool, error) {
		var p struct {
			Items []Workflow `json:"incident_workflows"`
			More  bool       `json:"more"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return false, err
		}
		all = append(all, p.Items...)
		return p.More, nil
	})
	sort.SliceStable(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
	return all, err
}

// GetWorkflow fetches one workflow with its steps. unknown lists any fields
// the API returned that this tool does not understand (and so cannot export).
func (c *Client) GetWorkflow(ctx context.Context, id string) (w Workflow, unknown []string, err error) {
	var raw struct {
		W json.RawMessage `json:"incident_workflow"`
	}
	if err = c.do(ctx, http.MethodGet, "/incident_workflows/"+url.PathEscape(id), nil, nil, &raw); err != nil {
		return w, nil, err
	}
	if err = json.Unmarshal(raw.W, &w); err != nil {
		return w, nil, fmt.Errorf("decoding workflow %s: %w", id, err)
	}
	return w, UnknownFields(raw.W, &Workflow{}), nil
}

// UnknownFields reports fields in data that a strict decode into v rejects.
// It is a best-effort guard against silently losing data if the API grows.
func UnknownFields(data []byte, v any) []string {
	var found []string
	for i := 0; i < 20; i++ {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err := dec.Decode(v)
		if err == nil {
			return found
		}
		msg := err.Error()
		const p = `json: unknown field "`
		idx := strings.Index(msg, p)
		if idx < 0 {
			return found
		}
		field := strings.TrimSuffix(msg[idx+len(p):], `"`)
		found = append(found, field)
		// Strip that key everywhere and retry to find the next one.
		data = stripKey(data, field)
	}
	return found
}

func stripKey(data []byte, key string) []byte {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return data
	}
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			delete(t, key)
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	out, _ := json.Marshal(v)
	return out
}

func (c *Client) CreateWorkflow(ctx context.Context, w Workflow) (Workflow, error) {
	var out struct {
		W Workflow `json:"incident_workflow"`
	}
	err := c.do(ctx, http.MethodPost, "/incident_workflows", nil, map[string]any{"incident_workflow": w}, &out)
	return out.W, err
}

func (c *Client) UpdateWorkflow(ctx context.Context, id string, w Workflow) (Workflow, error) {
	var out struct {
		W Workflow `json:"incident_workflow"`
	}
	err := c.do(ctx, http.MethodPut, "/incident_workflows/"+url.PathEscape(id), nil, map[string]any{"incident_workflow": w}, &out)
	return out.W, err
}

// ListTriggers returns all triggers attached to a workflow (cursor-paginated),
// plus any trigger fields this tool does not understand.
func (c *Client) ListTriggers(ctx context.Context, workflowID string) ([]Trigger, []string, error) {
	var all []Trigger
	unknown := map[string]bool{}
	q := url.Values{"workflow_id": {workflowID}, "limit": {strconv.Itoa(pageLimit)}}
	for {
		var p struct {
			Triggers   []json.RawMessage `json:"triggers"`
			NextCursor *string           `json:"next_cursor"`
			More       *bool             `json:"more"`
		}
		if err := c.do(ctx, http.MethodGet, "/incident_workflows/triggers", q, nil, &p); err != nil {
			return nil, nil, err
		}
		for _, raw := range p.Triggers {
			var t Trigger
			if err := json.Unmarshal(raw, &t); err != nil {
				return nil, nil, fmt.Errorf("decoding trigger: %w", err)
			}
			all = append(all, t)
			for _, f := range UnknownFields(raw, &Trigger{}) {
				unknown[f] = true
			}
		}
		if p.NextCursor == nil || *p.NextCursor == "" || (p.More != nil && !*p.More) {
			break
		}
		q.Set("cursor", *p.NextCursor)
	}
	var u []string
	for f := range unknown {
		u = append(u, f)
	}
	sort.Strings(u)
	return all, u, nil
}

func (c *Client) CreateTrigger(ctx context.Context, t Trigger) (Trigger, error) {
	var out struct {
		T Trigger `json:"trigger"`
	}
	err := c.do(ctx, http.MethodPost, "/incident_workflows/triggers", nil, map[string]any{"trigger": t}, &out)
	return out.T, err
}

// CheckAction asks whether the account can use an action. It returns nil if
// PagerDuty knows the action, or an *APIError (404 unknown, 403 not approved
// for this account). Note: the full action list (/incident_workflows/actions)
// omits older versions that still work, so it must not be used for this.
// A nil result is not a guarantee: some actions pass this lookup and are
// still refused when the workflow is created (seen with Kubernetes actions on
// an EU account), and import reports those at write time.
func (c *Client) CheckAction(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodGet, "/incident_workflows/actions/"+url.PathEscape(id), nil, nil, nil)
}

// AddTriggerService subscribes an existing trigger to one more service.
func (c *Client) AddTriggerService(ctx context.Context, triggerID, serviceID string) error {
	body := map[string]any{"service": map[string]string{"id": serviceID}}
	return c.do(ctx, http.MethodPost, "/incident_workflows/triggers/"+url.PathEscape(triggerID)+"/services", nil, body, nil)
}

func (c *Client) listNamed(ctx context.Context, path, key string) ([]Named, error) {
	var all []Named
	err := c.paginateOffset(ctx, path, nil, func(raw json.RawMessage) (bool, error) {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(raw, &p); err != nil {
			return false, err
		}
		var items []Named
		if err := json.Unmarshal(p[key], &items); err != nil {
			return false, err
		}
		all = append(all, items...)
		var more bool
		_ = json.Unmarshal(p["more"], &more)
		return more, nil
	})
	return all, err
}

func (c *Client) ListTeams(ctx context.Context) ([]Named, error) {
	return c.listNamed(ctx, "/teams", "teams")
}

func (c *Client) ListServices(ctx context.Context) ([]Named, error) {
	return c.listNamed(ctx, "/services", "services")
}

// Ping checks the token works by asking for a single workflow.
func (c *Client) Ping(ctx context.Context) error {
	q := url.Values{"limit": {"1"}}
	return c.do(ctx, http.MethodGet, "/incident_workflows", q, nil, nil)
}

// AccountHost works out which account a key belongs to (e.g.
// "acme.pagerduty.com") from the html_url of any object it can see.
// Returns "" if the account is empty or the key cannot list anything.
func (c *Client) AccountHost(ctx context.Context) string {
	q := url.Values{"limit": {"1"}}
	for _, p := range []struct{ path, key string }{
		{"/incident_workflows", "incident_workflows"},
		{"/services", "services"},
		{"/escalation_policies", "escalation_policies"},
		{"/users", "users"},
	} {
		var page map[string]json.RawMessage
		if c.do(ctx, http.MethodGet, p.path, q, nil, &page) != nil {
			continue
		}
		var items []struct {
			HTMLURL string `json:"html_url"`
		}
		if json.Unmarshal(page[p.key], &items) != nil {
			continue
		}
		for _, it := range items {
			if u, err := url.Parse(it.HTMLURL); err == nil && u.Host != "" {
				return u.Host
			}
		}
	}
	return ""
}
