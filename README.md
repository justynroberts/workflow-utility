# pdwf

Copy PagerDuty **Incident Workflows** between accounts using plain YAML files.
You export from one account, read or edit the files, and import into another
account (or back into the same one).

```
pdwf export   →   pd-workflows-acme-20261119-1300/*.yaml   →   pdwf import
 (account A)            (readable, editable files)             (account B)
```

## Get running (5 minutes)

**1. Install.** You need [Go 1.24+](https://go.dev/dl/) (`brew install go` on macOS).

```bash
gh repo clone justynroberts/workflow-utility   # or git clone
cd workflow-utility
make install                                   # builds and copies pdwf to ~/.local/bin
```

Open a **new terminal** and check it works from anywhere:

```bash
pdwf help
```

If you see `command not found: pdwf`, see [Problems](#problems).

**2. Get an API key** for each account. In PagerDuty go to **Integrations → API Access Keys → Create New API Key**. Read-only is enough to export; importing needs a **read-write** key. Don't use a service's Events/integration key.

**3. Check the key.** pdwf asks for it; your typing is hidden:

```bash
pdwf check
# API key for the account to check (input hidden): ********
# Connected to acme.pagerduty.com [US]
# 🟢 Key works and can read incident workflows.
```

US and EU accounts are detected automatically.

**4. Export** from the source account:

```bash
pdwf list                               # numbered list of workflows: # NAME TEAM ID
pdwf export "Major Incident Workflow"   # one, by name (quote names with spaces)
pdwf export PLD17BR P0MJPLR             # several, by ID
pdwf export                             # or everything
# 🟢 Exported 3 workflows to pd-workflows-acme-20261119-130000/
```

Every export goes into a **new** timestamped folder, so it never overwrites an earlier one.

**5. Preview the import** with the **target** account's key. This changes nothing:

```bash
pdwf import pd-workflows-acme-20261119-130000 --dry-run
# Connected to acme-staging.pagerduty.com [US]
# Plan for acme-staging.pagerduty.com [US]:
#   + CREATE  "Major Incident Workflow"   19 steps, 3 triggers  -> OFF (new)
#       🟡 step "Assign Lead" input "Assignee" is "PVHZ470", which looks like an ID from the source account...
```

**6. Import for real.** It asks you to type the target account's name before changing anything:

```bash
pdwf import pd-workflows-acme-20261119-130000
# Type the account name "acme-staging" to apply these changes to acme-staging.pagerduty.com: acme-staging
# 🟢 created "Major Incident Workflow" (P1ABC23)
```

**7. Finish in the PagerDuty UI.** New workflows arrive **switched OFF**. Fix any 🟡 IDs from step 5 (users, schedules, roles, status pages, Slack/Linear connections exist per account), then turn the workflow on.

That's it. `pdwf help <command>` has the details for each command.

## Day-to-day

| I want to… | Run |
|---|---|
| see what's in an account | `pdwf list` (or `pdwf list kubernetes` to filter) |
| export some workflows | `pdwf export "Name One" "Name Two" P0ABC12` |
| check files I edited | `pdwf validate <file-or-folder>` (offline) |
| import only some files from a folder | `pdwf import folder/a.yaml folder/b.yaml` |
| import but leave existing workflows alone | `pdwf import <folder> --existing skip` |
| keep the source's on/off state | `pdwf import <folder> --enable` |
| make sure everything lands OFF | `pdwf import <folder> --disable` |
| skip typing the key | `--token KEY`, or `export PAGERDUTY_TOKEN=KEY` |
| export K8s workflows created this week | `./scripts/export-k8s.sh` (`DAYS=30`, `EXCLUDE=testing`, `OUT=dir`) |

Careful with `PAGERDUTY_TOKEN`: if it's set, **every** command uses it, import included. pdwf always prints `Connected to <account>` so you can see which account a key belongs to.

## Safety

- `list`, `export`, `validate` and `check` never change anything in PagerDuty.
- `import` checks every file and looks up every team, service and action in the target **before** writing. If anything is wrong, nothing is changed.
- `import` shows a plan and requires you to type the target account's name. EU accounts have their own name, e.g. `acme.eu`, so a US and an EU account with the same subdomain can't be confused.
- New workflows are created **OFF**. Updated workflows keep their current on/off state.
- Before a workflow is updated, its current version is saved to `pdwf-backup-<account>-<time>/`. Re-import that folder to undo.
- Nothing is ever deleted: no workflows, no triggers, no services. Running the same import twice is safe.

## Problems

| You see | Fix |
|---|---|
| `command not found: pdwf` | Open a new terminal (or run `rehash`). Make sure `~/.local/bin` is on your PATH, or run `make install PREFIX=/opt/homebrew/bin`. |
| `command not found: pdwf export` (the whole phrase) | That gap is a non-breaking space (Option+Space on a Mac). Retype it as a normal space. |
| `401 … API token was rejected` | Use a REST API key from **API Access Keys**, not an integration key. |
| `403` on import | The key is read-only. Create a read-write key. |
| `team "X" does not exist in the target account` | Create that team in the target, or edit `team:` in the file (or remove the line). |
| `service "X" does not exist …` | Same for services, under `triggers:` → `services:`. |
| `action … does not exist` / `is refused by …` | The target account doesn't offer that action (EU offers fewer). Leave that file out, or replace those steps. |
| `Could not find actions` during import | Same cause, only detectable at write time. The workflow was **not** created. |
| a 🟡 warning about an ID | It works, but that step will fail when it runs until you change the ID to the target account's. |

## The file format

Each workflow is one YAML file, safe to edit by hand. `pdwf help format` explains it in full.

```yaml
format: pdwf/v1
workflow:
  name: Major Incident
  team: SRE                      # team NAME in the target account
  enabled: true                  # only used with --enable
  steps:
    - name: Post update
      action: pagerduty.com:incident-workflows:send-status-update:1
      inputs:
        Message: Investigating
    - name: Branch
      action: pagerduty.com:logic:if-else:1
      inputs:
        Condition: incident.priority matches 'P1'
      inline_steps:              # steps nested inside branches/loops
        Then:
          - name: Add note
            action: pagerduty.com:incident-workflows:add-note:1
            inputs: { Note: P1 path }
triggers:
  - type: conditional            # manual | conditional | incident_type
    condition: incident.priority matches 'P1'
    services: [Checkout API]     # service NAMES, or all_services: true
```

Step inputs are copied exactly, so **review files before sharing or committing them**. HTTP, webhook or script steps can contain URLs or tokens.

## Development

```bash
make build      # ./bin/pdwf
make test       # unit + end-to-end tests against a fake PagerDuty (no network)
make lint       # gofmt + go vet
make release    # binaries for macOS / Linux / Windows in ./dist
```
