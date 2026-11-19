package pd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder captures backoff sleeps instead of waiting.
type recorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (r *recorder) sleep(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sleeps = append(r.sleeps, d)
}

func newTestClient(t *testing.T, h http.Handler) (*Client, *recorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "  secret-key  ")
	rec := &recorder{}
	c.Sleep = rec.sleep
	return c, rec
}

func TestNewTrimsTokenAndBaseURL(t *testing.T) {
	c := New("https://example.com/", " tok \n")
	if c.BaseURL != "https://example.com" || c.Token != "tok" {
		t.Fatalf("got base %q token %q", c.BaseURL, c.Token)
	}
}

func TestBaseURLForRegion(t *testing.T) {
	for in, want := range map[string]string{"": USBaseURL, "us": USBaseURL, " US ": USBaseURL, "eu": EUBaseURL, "EU": EUBaseURL} {
		got, err := BaseURLForRegion(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"uk", "apac", "us-east"} {
		if _, err := BaseURLForRegion(bad); err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("%q: want error naming it, got %v", bad, err)
		}
	}
}

func TestDoHeaders(t *testing.T) {
	var got []http.Header
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		fmt.Fprint(w, `{}`)
	}))
	if err := c.do(context.Background(), "GET", "/x", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.do(context.Background(), "POST", "/x", nil, map[string]int{"a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	for i, h := range got {
		if h.Get("Authorization") != "Token token=secret-key" {
			t.Errorf("req %d Authorization = %q", i, h.Get("Authorization"))
		}
		if h.Get("Accept") != "application/vnd.pagerduty+json;version=2" {
			t.Errorf("req %d Accept = %q", i, h.Get("Accept"))
		}
	}
	if ct := got[0].Get("Content-Type"); ct != "" {
		t.Errorf("GET without body should not set Content-Type, got %q", ct)
	}
	if ct := got[1].Get("Content-Type"); ct != "application/json" {
		t.Errorf("POST Content-Type = %q", ct)
	}
}

func TestDoRetries429ThenSucceedsAndResendsBody(t *testing.T) {
	var bodies []string
	n := 0
	c, rec := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		n++
		switch n {
		case 1:
			w.Header().Set("ratelimit-reset", "7")
			w.WriteHeader(429)
		case 2:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	var out struct{ OK bool }
	if err := c.do(context.Background(), "POST", "/p", nil, map[string]string{"k": "v"}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || n != 3 {
		t.Fatalf("out=%v calls=%d", out, n)
	}
	for i, b := range bodies {
		if b != `{"k":"v"}` {
			t.Errorf("attempt %d body %q", i, b)
		}
	}
	want := []time.Duration{7 * time.Second, 3 * time.Second}
	if fmt.Sprint(rec.sleeps) != fmt.Sprint(want) {
		t.Errorf("sleeps %v, want %v", rec.sleeps, want)
	}
}

func TestDoRetries5xxWithExponentialBackoff(t *testing.T) {
	n := 0
	c, rec := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n <= 2 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	if err := c.do(context.Background(), "GET", "/p", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if fmt.Sprint(rec.sleeps) != fmt.Sprint(want) {
		t.Errorf("sleeps %v, want %v", rec.sleeps, want)
	}
}

func TestDoDoesNotRetry4xx(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409} {
		n := 0
		c, rec := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"message":"nope"}}`)
		}))
		err := c.do(context.Background(), "GET", "/p", nil, nil, nil)
		if !IsStatus(err, code) || n != 1 || len(rec.sleeps) != 0 {
			t.Errorf("%d: err=%v calls=%d sleeps=%v", code, err, n, rec.sleeps)
		}
	}
}

func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	n := 0
	c, rec := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(429)
	}))
	c.MaxRetries = 2
	err := c.do(context.Background(), "GET", "/p", nil, nil, nil)
	if !IsStatus(err, 429) {
		t.Fatalf("want 429 APIError, got %v", err)
	}
	if n != 3 || len(rec.sleeps) != 2 {
		t.Errorf("calls=%d sleeps=%d, want 3 and 2", n, len(rec.sleeps))
	}
}

func TestBackoff(t *testing.T) {
	hdr := func(k, v string) *http.Response {
		return &http.Response{Header: http.Header{http.CanonicalHeaderKey(k): {v}}}
	}
	cases := []struct {
		attempt int
		resp    *http.Response
		want    time.Duration
	}{
		{0, nil, 500 * time.Millisecond},
		{3, nil, 4 * time.Second},
		{10, nil, 20 * time.Second},                         // capped
		{0, hdr("ratelimit-reset", "12"), 12 * time.Second}, // honoured
		{0, hdr("Retry-After", "5"), 5 * time.Second},       // honoured
		{1, hdr("ratelimit-reset", "999"), time.Second},     // absurd value ignored
		{1, hdr("Retry-After", "Wed, 21 Oct"), time.Second}, // HTTP-date form ignored
		{2, &http.Response{Header: http.Header{}}, 2 * time.Second},
	}
	for _, c := range cases {
		if got := backoff(c.attempt, c.resp); got != c.want {
			t.Errorf("attempt %d: got %v want %v", c.attempt, got, c.want)
		}
	}
}

func TestDoNetworkErrorRetriesThenFails(t *testing.T) {
	c := New("http://127.0.0.1:1", "k") // nothing listens on port 1
	rec := &recorder{}
	c.Sleep = rec.sleep
	c.MaxRetries = 2
	err := c.do(context.Background(), "GET", "/p", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "could not reach PagerDuty") {
		t.Fatalf("got %v", err)
	}
	if len(rec.sleeps) != 2 {
		t.Errorf("sleeps %d, want 2", len(rec.sleeps))
	}
}

func TestDoBadJSONResponse(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not json`)
	}))
	var out map[string]any
	err := c.do(context.Background(), "GET", "/p", nil, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "decoding response from GET /p") {
		t.Fatalf("got %v", err)
	}
}

func TestDoEncodesQuery(t *testing.T) {
	var raw string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		fmt.Fprint(w, `{}`)
	}))
	_ = c.do(context.Background(), "GET", "/p", map[string][]string{"a b": {"c&d"}}, nil, nil)
	if raw != "a+b=c%26d" {
		t.Errorf("query %q", raw)
	}
}

func TestParseAPIError(t *testing.T) {
	t.Run("html unescaped and duplicate detail removed", func(t *testing.T) {
		err := parseAPIError("POST", "/incident_workflows", 400, []byte(`{"error":{"message":"Bad &#x5b;x&#x5d;","errors":["Bad &#x5b;x&#x5d;","other &amp; more"]}}`))
		var ae *APIError
		if !errors.As(err, &ae) {
			t.Fatal(err)
		}
		if ae.Message != "Bad [x]" || len(ae.Details) != 1 || ae.Details[0] != "other & more" {
			t.Errorf("got %q %q", ae.Message, ae.Details)
		}
	})
	t.Run("single Invalid detail promoted", func(t *testing.T) {
		err := parseAPIError("POST", "/p", 400, []byte(`{"error":{"message":"Invalid Input Provided","errors":["Invalid body: \"steps&#x5b;0&#x5d;\" is not allowed to be empty"]}}`))
		ae := err.(*APIError)
		if ae.Message != `Invalid body: "steps[0]" is not allowed to be empty` || ae.Details != nil {
			t.Errorf("got %q %q", ae.Message, ae.Details)
		}
	})
	t.Run("several details kept", func(t *testing.T) {
		ae := parseAPIError("POST", "/p", 400, []byte(`{"error":{"message":"Invalid Input Provided","errors":["a","b"]}}`)).(*APIError)
		if ae.Message != "Invalid Input Provided" || len(ae.Details) != 2 {
			t.Errorf("got %q %q", ae.Message, ae.Details)
		}
	})
	t.Run("non-JSON body truncated", func(t *testing.T) {
		ae := parseAPIError("GET", "/p", 502, []byte(strings.Repeat("x", 1000))).(*APIError)
		if len(ae.Message) != 303 || !strings.HasSuffix(ae.Message, "...") {
			t.Errorf("len %d", len(ae.Message))
		}
	})
	t.Run("empty body", func(t *testing.T) {
		ae := parseAPIError("GET", "/p", 500, nil).(*APIError)
		if ae.Message != "" {
			t.Errorf("got %q", ae.Message)
		}
	})
}

func TestAPIErrorHintsAndError(t *testing.T) {
	cases := []struct {
		e    APIError
		want string
	}{
		{APIError{Status: 401, Path: "/incident_workflows"}, "REST API key"},
		{APIError{Status: 403, Path: "/x"}, "not allowed"},
		{APIError{Status: 402, Path: "/x"}, "plan does not include"},
		{APIError{Status: 404, Path: "/incident_workflows", Message: "Could not find actions"}, "will not accept the action"},
		{APIError{Status: 404, Path: "/incident_workflows/P1"}, "Check the region"},
		{APIError{Status: 400, Path: "/x"}, "rejected the content"},
	}
	for _, c := range cases {
		if h := c.e.Hint(); !strings.Contains(h, c.want) {
			t.Errorf("%d %s: hint %q missing %q", c.e.Status, c.e.Path, h, c.want)
		}
	}
	if h := (&APIError{Status: 404, Path: "/teams"}).Hint(); h != "" {
		t.Errorf("404 on /teams should have no hint, got %q", h)
	}
	if h := (&APIError{Status: 500}).Hint(); h != "" {
		t.Errorf("500 should have no hint, got %q", h)
	}
	e := &APIError{Status: 400, Method: "POST", Path: "/p", Message: "m", Details: []string{"d1", "d2"}}
	s := e.Error()
	for _, want := range []string{"PagerDuty returned 400 for POST /p: m", "\n  - d1", "\n  - d2", "\n  hint: "} {
		if !strings.Contains(s, want) {
			t.Errorf("Error() %q missing %q", s, want)
		}
	}
}

func TestIsStatus(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &APIError{Status: 404})
	if !IsStatus(err, 404) || IsStatus(err, 401) || IsStatus(errors.New("x"), 404) || IsStatus(nil, 404) {
		t.Fatal("IsStatus wrong")
	}
}

// decodeBody is shared by the workflow tests.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("decoding request body: %v", err)
	}
	return m
}
