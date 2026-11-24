#!/usr/bin/env bash
# Deploys rules/ to an Aruma project with the same HTTP calls `firebase deploy` makes, for a machine
# without firebase-tools: the Firestore rules and their single-field index, the Realtime Database
# rules and the Storage rules. With --anonymous it also turns on guest (anonymous) sign-in, the
# switch on the dashboard's Sign-in methods page. e2e/run.sh deploys with this script.
#
#   ARUMA_URL=https://aruma.example.com ARUMA_TOKEN=aruma_pat_... tool/deploy_rules.sh <project> [--anonymous]
#
# ARUMA_TOKEN is a CLI token (dashboard, Account, CLI tokens). It needs curl and python3.
set -euo pipefail

[[ $# -ge 1 && -n ${ARUMA_URL:-} && -n ${ARUMA_TOKEN:-} ]] || {
  echo "usage: ARUMA_URL=<url> ARUMA_TOKEN=<CLI token> $0 <project id> [--anonymous]" >&2
  exit 2
}
project=$1
rules=$(cd "$(dirname "$0")/../rules" && pwd)
base=${ARUMA_URL%/}
auth=(-H "Authorization: Bearer $ARUMA_TOKEN" -H "Content-Type: application/json")

# Firestore: a ruleset, then the cloud.firestore release (firebase deploy --only firestore:rules).
ruleset=$(python3 -c 'import json, sys; print(json.dumps({"source": {"files": [{"name": "firestore.rules", "content": open(sys.argv[1]).read()}]}}))' "$rules/firestore.rules" \
  | curl -fsS -X POST "${auth[@]}" --data-binary @- "$base/v1/projects/$project/rulesets" \
  | python3 -c 'import json, sys; print(json.load(sys.stdin)["name"])')
python3 -c 'import json, sys; print(json.dumps({"name": "projects/%s/releases/cloud.firestore" % sys.argv[1], "rulesetName": sys.argv[2]}))' "$project" "$ruleset" \
  | curl -fsS -X POST "${auth[@]}" --data-binary @- "$base/v1/projects/$project/releases" >/dev/null
echo "firestore.rules: released ($ruleset)"

# Firestore's field overrides (firebase deploy --only firestore:indexes, one PATCH per field). "My
# teams" is a collection-group query on members.uid: it needs that field's COLLECTION_GROUP index,
# which is off by default, in Aruma as in Firebase.
python3 -c 'import json, sys
for o in json.load(open(sys.argv[1]))["fieldOverrides"]:
    idx = [{"queryScope": i["queryScope"], "fields": [{"fieldPath": o["fieldPath"], **{k: v for k, v in i.items() if k != "queryScope"}}]} for i in o["indexes"]]
    print(o["collectionGroup"], o["fieldPath"], json.dumps({"indexConfig": {"indexes": idx}}))' "$rules/firestore.indexes.json" \
  | while read -r group field body; do
      curl -fsS -X PATCH "${auth[@]}" --data-binary "$body" \
        "$base/v1/projects/$project/databases/(default)/collectionGroups/$group/fields/$field?updateMask=indexConfig" >/dev/null
      echo "firestore.indexes.json: $group.$field"
    done

# Realtime Database: PUT /.settings/rules.json on the project's default database (firebase deploy
# --only database), in the development form of its address (?ns=).
curl -fsS -X PUT -H "Authorization: Bearer $ARUMA_TOKEN" --data-binary "@$rules/database.rules.json" \
  "$base/.settings/rules.json?ns=$project-default-rtdb" >/dev/null
echo "database.rules.json: published"

# Cloud Storage: the rules of the project's bucket (the dashboard's Publish).
python3 -c 'import json, sys; print(json.dumps({"source": open(sys.argv[1]).read()}))' "$rules/storage.rules" \
  | curl -fsS -X PUT "${auth[@]}" --data-binary @- "$base/console/v1/projects/$project/storage/rules" >/dev/null
echo "storage.rules: published"

if [[ ${2:-} == --anonymous ]]; then
  curl -fsS -X PATCH "${auth[@]}" --data-binary '{"anonymousEnabled": true}' \
    "$base/console/v1/projects/$project/auth/config" >/dev/null
  echo "guest (anonymous) sign-in: on"
fi
