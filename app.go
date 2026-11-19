package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/justynroberts/workflow-utility/internal/pd"
)

type App struct {
	Out, Err io.Writer
	In       *os.File
	reader   *bufio.Reader

	// interactive overrides terminal detection in tests.
	interactive *bool
}

func (a *App) say(format string, args ...any) { fmt.Fprintf(a.Err, format+"\n", args...) }

func (a *App) isInteractive() bool {
	if a.interactive != nil {
		return *a.interactive
	}
	return term.IsTerminal(int(a.In.Fd()))
}

func (a *App) readLine(prompt string) (string, error) {
	fmt.Fprint(a.Err, prompt)
	if a.reader == nil {
		a.reader = bufio.NewReader(a.In)
	}
	s, err := a.reader.ReadString('\n')
	if err != nil && s == "" {
		return "", fmt.Errorf("no answer given")
	}
	return strings.TrimSpace(s), nil
}

func (a *App) readSecret(prompt string) (string, error) {
	fmt.Fprint(a.Err, prompt)
	b, err := term.ReadPassword(int(a.In.Fd()))
	fmt.Fprintln(a.Err)
	return strings.TrimSpace(string(b)), err
}

// connFlags are the "which PagerDuty account" options shared by commands.
type connFlags struct {
	token, region string
}

func addConnFlags(fs *flag.FlagSet) *connFlags {
	c := &connFlags{}
	fs.StringVar(&c.token, "token", "", "PagerDuty REST API key (default: $PAGERDUTY_TOKEN, else you are asked)")
	fs.StringVar(&c.region, "region", "", "us or eu (default: detected automatically)")
	return c
}

// Target is a connected PagerDuty account.
type Target struct {
	Client *pd.Client
	Host   string // e.g. acme.pagerduty.com; "" if it could not be worked out
	Region string
}

// Subdomain is the account name used for confirmation and folder names: the
// host without ".pagerduty.com", so "acme" and the EU account "acme.eu" differ.
func (t *Target) Subdomain() string {
	return strings.TrimSuffix(t.Host, ".pagerduty.com")
}

func (t *Target) Label() string {
	if t.Host != "" {
		return fmt.Sprintf("%s [%s]", t.Host, strings.ToUpper(t.Region))
	}
	return fmt.Sprintf("PagerDuty account [%s] (name unknown: it has no workflows or services yet)", strings.ToUpper(t.Region))
}

// connect gets a key (flag, env var, or prompt), finds its region, checks it
// works and identifies the account. purpose finishes the sentence
// "API key for the account to ..." in the prompt.
func (a *App) connect(ctx context.Context, cf *connFlags, purpose string) (*Target, error) {
	token := strings.TrimSpace(cf.token)
	if token == "" {
		if env := strings.TrimSpace(os.Getenv("PAGERDUTY_TOKEN")); env != "" {
			token = env
			a.say("Using the API key from $PAGERDUTY_TOKEN.")
		}
	}
	if token == "" {
		if !a.isInteractive() {
			return nil, fmt.Errorf("no PagerDuty API key: pass --token KEY or set PAGERDUTY_TOKEN")
		}
		var err error
		if token, err = a.readSecret(fmt.Sprintf("API key for the account to %s (input hidden): ", purpose)); err != nil {
			return nil, err
		}
		if token == "" {
			return nil, fmt.Errorf("no API key entered")
		}
	}

	regions := []string{"us", "eu"}
	if cf.region != "" {
		if _, err := pd.BaseURLForRegion(cf.region); err != nil {
			return nil, err
		}
		regions = []string{strings.ToLower(cf.region)}
	}
	if os.Getenv("PDWF_API_URL") != "" {
		regions = regions[:1]
	}

	var firstErr error
	for _, r := range regions {
		base, _ := pd.BaseURLForRegion(r)
		c := makeClient(base, token)
		err := c.Ping(ctx)
		if err == nil {
			t := &Target{Client: c, Region: r, Host: c.AccountHost(ctx)}
			// The US API also answers for EU accounts; use the EU API for them so
			// data stays in-region, and label the region correctly.
			if strings.HasSuffix(t.Host, ".eu.pagerduty.com") && r != "eu" && cf.region == "" && os.Getenv("PDWF_API_URL") == "" {
				eu := makeClient(pd.EUBaseURL, token)
				if eu.Ping(ctx) == nil {
					t.Client, t.Region = eu, "eu"
				}
			}
			a.say("Connected to %s", t.Label())
			return t, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		// Only a rejected key is worth retrying in the other region.
		if !pd.IsStatus(err, 401) {
			return nil, err
		}
	}
	return nil, firstErr
}

// makeClient is the only place clients are built, so PDWF_API_URL (used to
// point at a mock PagerDuty) applies to every command.
func makeClient(base, token string) *pd.Client {
	if u := os.Getenv("PDWF_API_URL"); u != "" {
		base = u
	}
	return pd.New(base, token)
}

func (a *App) cmdCheck(ctx context.Context, args []string) error {
	fs := newFlagSet("check", "check [--token KEY]")
	cf := addConnFlags(fs)
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	if _, err := a.connect(ctx, cf, "check"); err != nil {
		return err
	}
	a.say("🟢 Key works and can read incident workflows.")
	return nil
}
