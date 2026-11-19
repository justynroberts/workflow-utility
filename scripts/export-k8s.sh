#!/usr/bin/env bash
# Export the most recently created Kubernetes / K8s incident workflows.
#
#   ./scripts/export-k8s.sh                 # k8s flows created in the last 7 days
#   DAYS=30 ./scripts/export-k8s.sh         # ...last 30 days
#   DAYS=0  ./scripts/export-k8s.sh         # all k8s flows, any age
#   EXCLUDE='testing' ./scripts/export-k8s.sh   # skip names matching this
#   OUT=./k8s-flows ./scripts/export-k8s.sh     # choose the output folder
#
# Key: $PAGERDUTY_TOKEN, else $PAGERDUTY_API_KEY, else you are asked (hidden).
# Read-only: this only lists and exports, it never changes PagerDuty.
set -euo pipefail

DAYS="${DAYS:-7}"
MATCH="${MATCH:-kube|k8s}"          # case-insensitive regex on the workflow name
EXCLUDE="${EXCLUDE:-}"              # optional case-insensitive regex to skip
PDWF="${PDWF:-$(dirname "$0")/../bin/pdwf}"

if [[ ! -x "$PDWF" ]]; then
  echo "pdwf not found at $PDWF - run 'make build' first" >&2
  exit 1
fi

TOKEN="${PAGERDUTY_TOKEN:-${PAGERDUTY_API_KEY:-}}"
if [[ -z "$TOKEN" ]]; then
  read -rsp "PagerDuty API key (input hidden): " TOKEN
  echo >&2
fi

# Pick the IDs. The US API also answers for EU accounts, so one URL is enough.
IDS=$(PD_TOKEN="$TOKEN" DAYS="$DAYS" MATCH="$MATCH" EXCLUDE="$EXCLUDE" python3 -I - <<'PY'
import json, os, re, sys, urllib.request, urllib.error
from datetime import datetime, timedelta, timezone

days = int(os.environ["DAYS"])
match = re.compile(os.environ["MATCH"], re.I)
exclude = re.compile(os.environ["EXCLUDE"], re.I) if os.environ["EXCLUDE"] else None
cutoff = datetime.now(timezone.utc) - timedelta(days=days)

flows, offset = [], 0
while True:
    req = urllib.request.Request(
        f"https://api.pagerduty.com/incident_workflows?limit=100&offset={offset}",
        headers={"Authorization": "Token token=" + os.environ["PD_TOKEN"],
                 "Accept": "application/vnd.pagerduty+json;version=2"})
    try:
        page = json.load(urllib.request.urlopen(req, timeout=60))
    except urllib.error.HTTPError as e:
        sys.exit(f"PagerDuty returned {e.code}: check the API key")
    flows += page["incident_workflows"]
    if not page.get("more"):
        break
    offset += 100

picked = []
for w in flows:
    created = datetime.fromisoformat(w["created_at"].replace("Z", "+00:00"))
    if not match.search(w["name"]):
        continue
    if exclude and exclude.search(w["name"]):
        continue
    if days > 0 and created < cutoff:
        continue
    picked.append((created, w))

for created, w in sorted(picked, key=lambda p: p[0], reverse=True):
    print(f"  {created:%Y-%m-%d}  {w['id']}  {w['name']}", file=sys.stderr)
    print(w["id"])
PY
)

if [[ -z "$IDS" ]]; then
  echo "No workflows matching /$MATCH/ created in the last $DAYS days." >&2
  exit 0
fi

COUNT=$(wc -l <<<"$IDS" | tr -d ' ')
echo "Exporting $COUNT workflow(s)..." >&2

args=(export --token "$TOKEN")
[[ -n "${OUT:-}" ]] && args+=(--out "$OUT")
# shellcheck disable=SC2086
"$PDWF" "${args[@]}" $IDS
