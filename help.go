package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// banner is shown only on the overview help and "pdwf version".
var banner = strings.Join([]string{
	"             _               __",
	" _ __     __| |__      __  / _|",
	"| '_ \\   / _` |\\ \\ /\\ / / | |_",
	"| |_) | | (_| | \\ V  V /  |  _|",
	"| .__/   \\__,_|  \\_/\\_/   |_|",
	"|_|      PagerDuty ──▶ YAML ──▶ PagerDuty",
	"",
}, "\n")

const overview = `pdwf - copy PagerDuty Incident Workflows in and out of readable files

pdwf exports Incident Workflows (their steps and triggers) from a PagerDuty
account into YAML files you can read, edit, keep in git and send to someone,
and imports those files back - into the same account or a different one.

COMMANDS
  list       Show the workflows in an account as a numbered table
  export     Save workflows to YAML files (read-only, changes nothing)
  validate   Check exported files for mistakes, offline
  import     Create/update workflows from files (shows a plan, asks first)
  check      Test an API key and show which account it belongs to
  help       Show help for a command or topic
  version    Print the version

HELP TOPICS
  pdwf help <command>   detailed help for one command, e.g. "pdwf help import"
  pdwf help keys        API keys, regions, and using two accounts
  pdwf help format      what is in an exported file and what you can edit

TYPICAL USE: copy workflows from account A to account B
  pdwf list                                   # paste account A's key when asked
  pdwf export "Major Incident Workflow"       # by name (or ID, or no name = all)
  pdwf import pd-workflows-acme-20261119-1200 --dry-run   # paste account B's key
  pdwf import pd-workflows-acme-20261119-1200             # type B's name to apply

SAFETY
  - export, list, validate and check never change anything in PagerDuty.
  - import validates every file and looks up every team and service BEFORE it
    writes anything. If something is wrong, nothing is changed.
  - import prints which account it is connected to and exactly what it will do,
    then asks you to type that account's name (e.g. "acme") to go ahead.
  - workflows that are about to be updated are backed up to a folder first.
  - imported workflows are created switched OFF unless you pass --enable.
  - nothing is ever deleted: not workflows, not triggers, not services.

EXIT CODES
  0 success   1 something failed (the message says what)   2 wrong usage
`

const keysHelp = `API KEYS, REGIONS AND TWO ACCOUNTS

Which key
  Use a PagerDuty REST API key: in PagerDuty go to Integrations > API Access
  Keys > Create New API Key. A read-only key is enough for list/export/check;
  import needs a full-access (read-write) key. Integration/Events keys (the
  32-character ones on a service) will NOT work.

How pdwf gets the key (first one found wins)
  1. --token KEY on the command line
  2. the PAGERDUTY_TOKEN environment variable
  3. otherwise it asks you, with hidden input

  Each command connects to exactly one account, so exporting with one key and
  importing with another is just: run export, paste key A; run import, paste
  key B. Careful with PAGERDUTY_TOKEN: if it is set, every command uses it,
  including import. pdwf always prints "Connected to <account>" so you can see
  which account a key belongs to.

Regions
  US and EU accounts use different API addresses. pdwf tries US first and, if
  the key is rejected, tries EU - you do not need to do anything. To force one,
  pass --region us or --region eu.

Checking a key
  pdwf check --token KEY      prints the account name, e.g. acme.pagerduty.com
`

const formatHelp = `THE EXPORT FILE FORMAT (format: pdwf/v1)

Each workflow is one .yaml file. The top of each file has a comment saying how
to check and import it. You can edit any of it by hand; run "pdwf validate"
afterwards to catch mistakes.

  format: pdwf/v1                  # required, tells pdwf what this file is
  exported_at: ...                 # information only
  source:                          # information only (where it came from)
    account: acme.pagerduty.com
  workflow:
    name: Major Incident           # import matches existing workflows by NAME
    description: ...
    team: SRE                      # team NAME; must exist in the target account
    enabled: true                  # import ignores this unless you pass --enable
    steps:
      - name: Post update          # shown in the PagerDuty UI
        action: pagerduty.com:incident-workflows:send-status-update:1
        inputs:                    # the action's settings, "Name: value"
          Message: |-
            Multi-line values
            look like this
      - name: Main loop
        action: pagerduty.com:logic:incident-workflows-loop-until:3
        inputs: { ... }
        inline_steps:              # steps nested inside loops / branches
          Actions:
            - name: ...
  triggers:
    - type: manual                 # manual | conditional | incident_type
      all_services: true
    - type: conditional
      condition: incident.priority matches 'P1'
      services: [Checkout API]     # service NAMES; must exist in the target
    - type: incident_type
      incident_types: [security_incident]

Things to know
  - Teams and services are written by name and looked up by name on import, so
    files move between accounts. If a name is not unique, put the ID instead.
  - Step inputs are copied exactly. Some of them hold account-specific IDs
    (users, schedules, escalation policies, roles, status pages, Slack or
    Linear connections). pdwf warns about values that look like IDs; when
    importing into a different account, change them to that account's IDs.
  - Empty inputs (Label: "") are kept in the file for you to fill in, but are
    not sent to PagerDuty, which treats them as unset.
  - Keys are checked strictly: a typo such as "stpes:" is an error, not
    silently ignored.
`

// commandHelp is the long help shown by "pdwf help X" and "pdwf X -h".
var commandHelp = map[string]string{
	"list": `pdwf list [SEARCH] [--token KEY] [--region us|eu]

Shows every incident workflow in the account as a numbered table with its
name, team and ID. Use the name or the ID with "pdwf export".

SEARCH (optional) shows only workflows whose name contains it.

Examples
  pdwf list
  pdwf list kubernetes
  pdwf list --token KEY
`,
	"export": `pdwf export [NAME-or-ID ...] [--out FOLDER] [--force] [--no-triggers] [--token KEY]

Saves workflows, with their steps and triggers, as one YAML file each.
Read-only: nothing in PagerDuty is changed.

Which workflows
  No names    all workflows in the account
  NAME        exact name, not case-sensitive. Quote names with spaces.
  ID          the ID from "pdwf list", e.g. PLD17BR
  Several can be given. If a name does not match, nothing is written and pdwf
  suggests close matches. If two workflows share a name, use the ID.

Where files go
  By default into a NEW folder, pd-workflows-<account>-<date>-<time>, so an
  export never overwrites an earlier one. With --out FOLDER, existing files
  in that folder are not overwritten unless you add --force.

Warnings you may see
  "fields this version of pdwf does not understand" means PagerDuty returned
  data that pdwf does not know how to carry over; the rest is still exported.

Examples
  pdwf export
  pdwf export "Major Incident Workflow"
  pdwf export PLD17BR P0MJPLR --out ./workflows
`,
	"validate": `pdwf validate FILE-or-FOLDER ...

Checks exported files without connecting to PagerDuty: that each file is a pdwf
file, that keys are spelt correctly, that every step has a name and an action,
that triggers make sense, and that no two files define the same workflow name.
It also warns about input values that look like account-specific IDs.

Team and service names can only be checked against a real account; "pdwf import
--dry-run" does that.

Examples
  pdwf validate pd-workflows-acme-20261119-120000
  pdwf validate major-incident-workflow.yaml
`,
	"import": `pdwf import FILE-or-FOLDER ... [--dry-run] [--yes] [--existing update|skip]
            [--enable | --disable] [--no-triggers] [--backup-dir FOLDER] [--token KEY]

Creates or updates workflows in the account the key belongs to.

What happens, in order
  1. Every file is read and checked. Any problem: stop, nothing changed.
  2. The target account's workflows, teams and services are read, and every
     team/service name in the files is looked up. Any missing: stop.
  3. A plan is printed, one line per workflow, ending in its on/off state:
       + CREATE   no workflow with that name exists yet
       ~ UPDATE   a workflow with that name exists; its steps are replaced
       = SKIP     exists, and --existing skip was given
  4. You are asked to type the account name (e.g. "acme") to go ahead.
     Anything else cancels. --dry-run stops before this step.
  5. Each workflow about to be UPDATEd is saved to a backup folder first
     (pdwf-backup-<account>-<date>-<time>). Re-import from it to undo.
  6. Workflows are created/updated. Triggers in the file that the workflow
     does not have yet are added; services listed on a trigger are added to
     the existing trigger. Nothing is removed.

On or off (enabled)
  An imported workflow that is ON with a conditional or incident-type trigger
  starts running on real incidents in the target straight away. So by default:
    new workflows        are created OFF
    updated workflows    keep whatever on/off state they have now
  --enable    set each workflow ON/OFF as in its file (as it was in the source)
  --disable   turn every imported workflow OFF, including updated ones
  Turn workflows on in the PagerDuty UI once you have checked them.

Running the same import twice is safe: the second run updates in place and
reports existing triggers as "already there".

Options
  --dry-run          show the plan only, change nothing
  --yes              skip the typed confirmation (for scripts; careful)
  --existing skip    leave workflows that already exist alone
  --enable           copy each workflow's on/off state from its file
  --disable          turn every imported workflow off
  --no-triggers      import steps only
  --backup-dir DIR   where to put backups

Examples
  pdwf import pd-workflows-acme-20261119-120000 --dry-run
  pdwf import pd-workflows-acme-20261119-120000
  pdwf import major-incident-workflow.yaml --existing skip
`,
	"check": `pdwf check [--token KEY] [--region us|eu]

Tests an API key: says which account it belongs to (e.g. acme.pagerduty.com),
which region it is in, and whether it can read incident workflows. Nothing is
changed. Useful before an import to be sure you have the right key.
`,
}

var topicHelp = map[string]string{"keys": keysHelp, "key": keysHelp, "token": keysHelp, "format": formatHelp, "file": formatHelp, "yaml": formatHelp}

var aliases = map[string]string{"ls": "list", "pull": "export", "push": "import", "lint": "validate", "test": "check"}

func (app *App) cmdHelp(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(app.Out, banner+"\n"+overview)
		return nil
	}
	topic := strings.ToLower(args[0])
	if a, ok := aliases[topic]; ok {
		topic = a
	}
	if h, ok := commandHelp[topic]; ok {
		fmt.Fprint(app.Out, h)
		return nil
	}
	if h, ok := topicHelp[topic]; ok {
		fmt.Fprint(app.Out, h)
		return nil
	}
	var all []string
	for k := range commandHelp {
		all = append(all, k)
	}
	sort.Strings(all)
	return usageError{fmt.Sprintf("no help for %q. Try one of: %s, keys, format", args[0], strings.Join(all, ", "))}
}

// printCommandHelp is used for "pdwf <command> -h".
func printCommandHelp(w io.Writer, name string) bool {
	if h, ok := commandHelp[name]; ok {
		fmt.Fprint(w, h)
		return true
	}
	return false
}
