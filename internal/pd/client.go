// Package pd is a small PagerDuty REST API v2 client covering just what
// pdwf needs: incident workflows, their triggers, teams and services.
package pd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	USBaseURL = "https://api.pagerduty.com"
	EUBaseURL = "https://api.eu.pagerduty.com"
	pageLimit = 100
)

type Client struct {
	BaseURL    string
	Token      string
	HTTP       *http.Client
	MaxRetries int
	// Sleep is swappable so tests don't wait on backoff.
	Sleep func(time.Duration)
}

// BaseURLForRegion maps a user-facing region name to an API base URL.
func BaseURLForRegion(region string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", "us":
		return USBaseURL, nil
	case "eu":
		return EUBaseURL, nil
	default:
		return "", fmt.Errorf("unknown region %q: use \"us\" or \"eu\"", region)
	}
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      strings.TrimSpace(token),
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		MaxRetries: 5,
		Sleep:      time.Sleep,
	}
}

// APIError is a non-2xx response, with a plain-English hint where we can give one.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
	Details []string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "PagerDuty returned %d for %s %s", e.Status, e.Method, e.Path)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	for _, d := range e.Details {
		fmt.Fprintf(&b, "\n  - %s", d)
	}
	if h := e.Hint(); h != "" {
		fmt.Fprintf(&b, "\n  hint: %s", h)
	}
	return b.String()
}

func (e *APIError) Hint() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "the API token was rejected. Use a REST API key (Integrations > API Access Keys), not an Events/integration key. If the account is hosted in the EU, add --region eu."
	case http.StatusForbidden:
		return "the token is valid but not allowed to do this. Use a read-write account API key, or a user token belonging to an Admin/Manager."
	case http.StatusPaymentRequired:
		return "this PagerDuty account's plan does not include Incident Workflows."
	case http.StatusNotFound:
		if strings.HasPrefix(e.Path, "/incident_workflows") {
			return "not found. Check the region (--region eu) and that the account has Incident Workflows."
		}
	case http.StatusBadRequest:
		return "PagerDuty rejected the content. The details above say which field; fix it in the file and re-run."
	}
	return ""
}

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Token token="+c.Token)
		req.Header.Set("Accept", "application/vnd.pagerduty+json;version=2")
		req.Header.Set("User-Agent", "pdwf")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			if attempt < c.MaxRetries && ctx.Err() == nil {
				c.Sleep(backoff(attempt, nil))
				continue
			}
			return fmt.Errorf("could not reach PagerDuty (%s): %w", c.BaseURL, err)
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading response: %w", readErr)
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if retryable && attempt < c.MaxRetries {
			c.Sleep(backoff(attempt, resp))
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return parseAPIError(method, path, resp.StatusCode, data)
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decoding response from %s %s: %w", method, path, err)
			}
		}
		return nil
	}
}

func backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		// PagerDuty sends ratelimit-reset (seconds); Retry-After is the generic form.
		for _, h := range []string{"ratelimit-reset", "Retry-After"} {
			if s, err := strconv.Atoi(resp.Header.Get(h)); err == nil && s > 0 && s <= 120 {
				return time.Duration(s) * time.Second
			}
		}
	}
	d := time.Duration(1<<attempt) * 500 * time.Millisecond
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d
}

func parseAPIError(method, path string, status int, data []byte) error {
	ae := &APIError{Status: status, Method: method, Path: path}
	var env struct {
		Error struct {
			Message string   `json:"message"`
			Errors  []string `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil && env.Error.Message != "" {
		ae.Message = html.UnescapeString(env.Error.Message)
		for _, d := range env.Error.Errors {
			if d = html.UnescapeString(d); d != ae.Message {
				ae.Details = append(ae.Details, d)
			}
		}
		// A single detail is usually the real message; show it instead.
		if len(ae.Details) == 1 && strings.HasPrefix(ae.Message, "Invalid") {
			ae.Message, ae.Details = ae.Details[0], nil
		}
	} else if s := strings.TrimSpace(string(data)); s != "" {
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		ae.Message = s
	}
	return ae
}

// paginateOffset walks a classic offset/limit/more listing. fetch decodes one
// page into its own slice and returns the page's "more" flag.
func (c *Client) paginateOffset(ctx context.Context, path string, query url.Values, page func(data json.RawMessage) (bool, error)) error {
	if query == nil {
		query = url.Values{}
	}
	for offset := 0; ; offset += pageLimit {
		query.Set("limit", strconv.Itoa(pageLimit))
		query.Set("offset", strconv.Itoa(offset))
		var raw json.RawMessage
		if err := c.do(ctx, http.MethodGet, path, query, nil, &raw); err != nil {
			return err
		}
		more, err := page(raw)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
}
