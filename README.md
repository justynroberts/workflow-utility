# pdwf

Copy PagerDuty **Incident Workflows** out of one account into readable YAML files, and back into the same or a different account.

## Install

```bash
make build        # ./bin/pdwf
make install      # or put it on your PATH
make release      # binaries for macOS / Linux / Windows in ./dist
```

## Use

```bash
pdwf list                                    # asks for the API key (hidden)
pdwf export                                  # all workflows -> ./pd-workflows-<account>-<timestamp>/
pdwf export "Major Incident Workflow" PLD17BR    # pick by name or ID
pdwf validate pd-workflows-acme-20261119-120000  # offline check
pdwf import pd-workflows-acme-20261119-120000 --dry-run   # paste the TARGET account's key
pdwf import pd-workflows-acme-20261119-120000
```

Each command asks for the key, so you can export with one account's key and import with another's. You can also pass `--token KEY` or set `PAGERDUTY_TOKEN`, but if that variable is set every command uses it, import included. pdwf always prints `Connected to <account>.pagerduty.com`. The US or EU region is detected automatically. Export needs a read key; import needs a read-write key.

`pdwf help`, `pdwf help <command>`, `pdwf help keys` and `pdwf help format` have the details.

## What import does

1. Parses every file strictly: a misspelt key is an error, not silently ignored.
2. Looks up the team and every service **by name** in the target account. If anything is missing it stops before writing anything.
3. Prints a plan (CREATE, UPDATE or SKIP per workflow) and flags input values that look like account-specific IDs (users, escalation policies).
4. Asks you to type the account name (the `acme` in `acme.pagerduty.com`) to confirm. `--dry-run` stops after the plan; `--yes` skips the prompt for scripts.
5. Saves a backup of each workflow it is about to update into `./pdwf-backup-<account>-<timestamp>/`.
6. Creates or updates the workflows. A workflow with the same name in the target is updated (`--existing skip` leaves it alone). Missing triggers are created, and missing services are added to existing triggers.

Nothing is ever deleted. Re-running the same import is safe: it updates rather than duplicates.

**On/off.** A workflow that is ON with a conditional or incident-type trigger runs on real incidents as soon as it lands. So by default new workflows are created **OFF**, and updated workflows keep their current state in the target. `--enable` copies the on/off state from the files (the source account's state). `--disable` turns every imported workflow off. The plan shows the resulting state on each line, and warns about any workflow that will be switched on with an automatic trigger.

## File format

```yaml
format: pdwf/v1
workflow:
  name: Major Incident
  team: SRE                      # team name in the target account
  steps:
    - name: Post update
      action: pagerduty.com:incident-workflows:send-status-update:1
      inputs:
        Message: Investigating
    - name: Branch
      action: pagerduty.com:logic:if-else:1
      inputs:
        Condition: incident.priority matches 'P1'
      inline_steps:              # nested steps of branch/loop actions
        Then:
          - name: Add note
            action: pagerduty.com:incident-workflows:add-note:1
            inputs: { Note: P1 path }
triggers:
  - type: conditional            # manual | conditional | incident_type
    condition: incident.priority matches 'P1'
    services: [Checkout API]     # or all_services: true
```

## Limits

- Step inputs that hold IDs (a user, a schedule, a role, a status page, a Slack or Linear connection) are copied as-is. Import warns about values that look like PagerDuty IDs or UUIDs, but you have to change them yourself when the target is a different account.
- Triggers are matched by type and condition. Import never removes a trigger or a trigger's service.
- Empty inputs (`Label: ""`) stay in the file but are not sent: PagerDuty returns them on read but rejects them on write.
