# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`pdwf` is a Go CLI that exports PagerDuty Incident Workflows (plus their triggers) to human-editable YAML and imports them into the same or another PagerDuty account. The user wants it to be "idiot proof", so keep that bar: validate everything and resolve all names before the first write, show a plan, require confirmation, back up before overwriting, never delete, and make every error say what to do next.

## Commands

```bash
make build                      # ./bin/pdwf (stamps version from git)
make test                       # go test ./...  — uses an in-process fake PagerDuty, no network
./bin/pdwf import FILE --dry-run --token "$PAGERDUTY_API_KEY"   # live read-only check; that variable holds the user's key
make lint                       # gofmt check + go vet
go test -run TestExportThenImport -v .   # single test; -v prints a sample export file and import log
```

## Layout

- `main.go`: command dispatch and `parseFlags`. That helper lets flags appear after positional args (`pdwf export "Name" --token KEY`); use it instead of `fs.Parse`.
- `help.go`: all user-facing help (overview, per-command, the `keys` and `format` topics) plus the banner. `pdwf X -h` prints `commandHelp[X]` followed by the flag list. The user asked for detailed help, so keep it current when you add flags.
- `app.go`: `App` (I/O, prompts) and `connect()`. The key comes from `--token`, then `PAGERDUTY_TOKEN`, then a hidden prompt. There are deliberately **no saved environments**: the user doesn't want them, and every command works on whichever account its key belongs to. `connect` tries US then EU when the key gets a 401, and identifies the account host from any object's `html_url`. Import confirmation is typing that subdomain. `makeClient` is the **only** place clients are built, and it honours `PDWF_API_URL` (the mock override). Never build a `pd.Client` anywhere else, or tests will hit the real API.
- `export.go`, `import.go`: one file per command group. `import` runs strictly in phases: load+validate files → read target → resolve names → plan → confirm → backup → apply.
- `internal/pd`: a thin REST v2 client. Workflow and team/service lists use offset pagination; triggers use cursor pagination (`next_cursor`). 429 and 5xx responses are retried using `ratelimit-reset`. `APIError.Hint()` holds the plain-English fixes for each status code.
- `internal/wffile`: the YAML format (`format: pdwf/v1`) and API conversion. Files reference teams and services by **name**, resolved per target account by `Resolver`. Step inputs and `inline_steps` use ordered maps with custom YAML marshalling. `Load` uses `KnownFields(true)`, so typos are errors.

## Things that are easy to break

- `pd` structs keep read-only fields (`id`, `self`, `outputs`, `parameter_type`, …) so that `pd.UnknownFields` can strict-decode GET responses and warn when PagerDuty adds fields that would otherwise be dropped silently. When you add a field the API returns, add it to the struct, and to `wffile` too if it should survive export.
- Writes must not send those read-only fields. Conversion builds fresh `pd` structs from the file, so the omitempty fields stay empty.
- API shapes were confirmed against a live account on 2026-11-19 (export of 31 workflows; create, update and trigger creation of one renamed, disabled copy; round trip identical apart from dropped empty inputs). Behaviour the docs don't mention:
  - Unset optional inputs come back as `""`, but POST/PUT reject `""`. `stepsToAPI` drops them.
  - `POST /incident_workflows/triggers` rejects `workflow.type`; send `{id}` only.
  - `incident_type` triggers carry `incident_types: [...]`. Every trigger has `is_disabled`.
  - Error messages are HTML-escaped (`&#x5b;`); `parseAPIError` unescapes them.
  - yaml.v3 escapes emoji as `\U0001F510`; `wffile.unescapeUnicode` turns them back.
  - The US API also answers for EU accounts. `connect` switches to the EU API when the account host ends `.eu.pagerduty.com`. The user's US and EU accounts share the subdomain `pdt-justyn`, which is why the confirmation word is the host minus `.pagerduty.com` (`pdt-justyn` vs `pdt-justyn.eu`) and must stay that way.
  - When a workflow is OFF, PagerDuty marks its triggers `is_disabled: true` itself. A re-export of an OFF workflow therefore shows `disabled: true` on triggers even though pdwf never sent it.
- Cross-account import was tested US → EU on 2026-11-19 with one disabled copy. PagerDuty accepts the source account's IDs in step inputs without checking them, so a cross-account import "succeeds" and the steps fail only when they run. That's why the ID/UUID warnings exist.
- The fake server in `main_test.go` is the spec the tests assume. If real API behaviour differs, fix the fake first, then the code.
- Output conventions: status goes to stderr through `a.say`, data (tables) to stdout. Emoji are used only as 🟢/🟡/🔴 line markers.
