// pdwf exports PagerDuty incident workflows to readable YAML files and
// imports them back, into the same or a different PagerDuty account.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if errors.Is(err, errHelpShown) {
		return
	}
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(os.Stderr, "error: %s\n\nRun \"pdwf help\" for usage.\n", err)
			os.Exit(2)
		}
		if !errors.Is(err, errSilent) {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
		}
		os.Exit(1)
	}
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// errSilent means the details were already printed.
var errSilent = errors.New("failed")

// errHelpShown means -h was given and help was printed: exit 0.
var errHelpShown = errors.New("help shown")

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	app := &App{Out: stdout, Err: stderr, In: os.Stdin}
	return app.dispatch(ctx, args)
}

func (app *App) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(app.Out, banner+"\n"+overview)
		return nil
	}
	cmd, rest := args[0], args[1:]
	stdout := app.Out
	helpOutput = app.Out
	switch cmd {
	case "help", "-h", "--help":
		return app.cmdHelp(rest)
	case "version", "--version":
		fmt.Fprint(stdout, banner)
		fmt.Fprintln(stdout, "\npdwf", version)
		return nil
	case "check", "test":
		return app.cmdCheck(ctx, rest)
	case "list", "ls":
		return app.cmdList(ctx, rest)
	case "export", "pull":
		return app.cmdExport(ctx, rest)
	case "import", "push":
		return app.cmdImport(ctx, rest)
	case "validate", "lint":
		return app.cmdValidate(rest)
	default:
		return usageError{fmt.Sprintf("unknown command %q", cmd)}
	}
}

// parseFlags lets flags appear before, after or between positional args
// (Go's flag package normally stops at the first positional arg).
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fs.SetOutput(helpOutput)
				fs.Usage()
				return nil, errHelpShown
			}
			return nil, usageError{err.Error()}
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// helpOutput is where "pdwf <command> -h" prints; dispatch points it at App.Out.
var helpOutput io.Writer = os.Stdout

func newFlagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		if !printCommandHelp(fs.Output(), name) {
			fmt.Fprintf(fs.Output(), "usage: pdwf %s\n", synopsis)
		}
		fmt.Fprintln(fs.Output(), "\nAll options:")
		fs.PrintDefaults()
	}
	return fs
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t'\"$") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
