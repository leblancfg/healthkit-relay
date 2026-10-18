#!/usr/bin/env bash
# End-to-end check of a running healthkit-relay, the way the iPhone app uses it:
#
#   1. wait for /healthz;
#   2. mint a one-time setup code with the server's own `pair` command;
#   3. exchange it at POST /v1/pair for a device token (and check the code works only once);
#   4. upload a synthetic batch with that token and check it is stored, then deduplicated;
#   5. optionally restart the server and check the token, the records and the
#      de-duplication index all survived.
#
# Every install path runs this (Docker Compose, release binary with systemd, NixOS module).
# Configuration, from the environment:
#
#   HKR_URL          base URL of the server (default http://127.0.0.1:8080)
#   HKR_PAIR_CMD     shell command that runs `healthkit-relay pair` against it (required),
#                    e.g. 'docker compose exec -T relay healthkit-relay pair'
#   HKR_RESTART_CMD  shell command that restarts the server (optional: no restart check)
#   HKR_WAIT         seconds to wait for /healthz (default 60)
#
# Needs bash, curl and jq. Exits non-zero on the first failed check.
set -euo pipefail

url=${HKR_URL:-http://127.0.0.1:8080}
url=${url%/}
wait_s=${HKR_WAIT:-60}
: "${HKR_PAIR_CMD:?set HKR_PAIR_CMD to a command running healthkit-relay pair}"

step() { printf '==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
check() { # check <description> <actual> <expected>
  [ "$2" = "$3" ] || fail "$1: got '$2', want '$3'"
  printf '    ok: %s (%s)\n' "$1" "$2"
}

wait_healthy() {
  local i
  for ((i = 0; i < wait_s * 2; i++)); do
    if [ "$(curl -fsS --max-time 2 "$url/healthz" 2>/dev/null)" = ok ]; then return 0; fi
    sleep 0.5
  done
  fail "$url/healthz did not answer ok within ${wait_s}s"
}

# api <method> <path> <token|-> [body]: prints the body, then the status code on its own line.
api() {
  local auth=()
  [ "$3" != - ] && auth=(-H "Authorization: Bearer $3")
  curl -sS --max-time 15 -X "$1" "${auth[@]}" -H 'Content-Type: application/json' \
    ${4:+--data-binary "$4"} -w '\n%{http_code}' "$url$2"
}
body() { sed '$d' <<<"$1"; }
code() { tail -n1 <<<"$1"; }

uuid() { # a random UUID, upper case like HealthKit's
  od -An -N16 -tx1 /dev/urandom | tr -d ' \n' |
    sed -E 's/^(.{8})(.{4})(.{4})(.{4})(.{12})$/\1-\2-\3-\4-\5/' | tr a-f A-F
}

step "waiting for $url/healthz"
wait_healthy
check healthz "$(curl -fsS "$url/healthz")" ok

step "minting a setup code with: $HKR_PAIR_CMD"
pair_out=$(bash -c "$HKR_PAIR_CMD") || fail "pair command failed: $pair_out"
setup_code=$(grep -oE '/setup\?code=[A-Z2-7]+' <<<"$pair_out" | head -n1 | cut -d= -f2 || true)
[ -n "$setup_code" ] || fail "no setup code in the pair output:
$pair_out"
grep -q 'healthrelay://' <<<"$pair_out" || grep -q 'Scan with' <<<"$pair_out" ||
  fail "pair output has no QR code prompt"
printf '    ok: setup code %s…\n' "${setup_code:0:4}"

step "pairing like the app: POST /v1/pair"
r=$(api POST /v1/pair - "{\"code\":\"$setup_code\",\"name\":\"Smoke test\",\"model\":\"CI\"}")
check "pair status" "$(code "$r")" 200
token=$(body "$r" | jq -er .token) || fail "no token in $(body "$r")"
device_id=$(body "$r" | jq -er .device.id)
check "token length" "$(( ${#token} >= 32 ))" 1
r=$(api POST /v1/pair - "{\"code\":\"$setup_code\",\"name\":\"Again\"}")
check "same code again is refused" "$(code "$r")" 403

step "status with the device token"
r=$(api GET /v1/status "$token")
check "status" "$(code "$r")" 200
check "status names the device" "$(body "$r" | jq -r .device.id)" "$device_id"
before=$(body "$r" | jq -er .records)

r=$(api POST /v1/samples - '{"samples":[]}')
check "upload without a token is refused" "$(code "$r")" 401
r=$(api POST /v1/samples "not-a-real-token-0123456789abcdef" '{"samples":[]}')
check "upload with a wrong token is refused" "$(code "$r")" 401

step "uploading a synthetic batch (wire format 2)"
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
day=$(date -u +%Y-%m-%d)
hr=$(uuid) sleep_uuid=$(uuid) gone=$(uuid)
batch=$(jq -cn --arg now "$now" --arg day "$day" --arg hr "$hr" --arg sl "$sleep_uuid" --arg gone "$gone" '{
  version: 2,
  device: {model: "CI", system_version: "0", app_version: "smoke-test"},
  samples: [
    {uuid: $hr, type: "HKQuantityTypeIdentifierHeartRate", kind: "quantity",
     start: $now, end: $now, value: 62, unit: "count/min", source: {name: "smoke-test"}},
    {uuid: $sl, type: "HKCategoryTypeIdentifierSleepAnalysis", kind: "category",
     start: $now, end: $now, value: 4, value_label: "asleepDeep", source: {name: "smoke-test"}}
  ],
  records: [
    {kind: "activity_summary", key: ("smoke-" + $hr), start: ($day + "T00:00:00Z"),
     values: {active_energy: {value: 512, unit: "kcal"}}}
  ],
  deletions: [{uuid: $gone, type: "HKQuantityTypeIdentifierStepCount"}]
}')
r=$(api POST /v1/samples "$token" "$batch")
check "upload" "$(code "$r")" 200
resp=$(body "$r")
check "received" "$(jq -r .received <<<"$resp")" 4
check "stored" "$(jq -r .stored <<<"$resp")" 4
check "duplicates" "$(jq -r .duplicates <<<"$resp")" 0
check "records" "$(jq -r .records <<<"$resp")" 1

r=$(api POST /v1/samples "$token" "$batch")
check "same batch again: stored" "$(body "$r" | jq -r .stored)" 0
check "same batch again: duplicates" "$(body "$r" | jq -r .duplicates)" 4

step "querying what is stored"
r=$(api GET /v1/status "$token")
after=$(body "$r" | jq -er .records)
check "records added to the archive" "$((after - before))" 4
metrics=$(curl -fsS "$url/metrics")
grep -qx "healthkit_relay_archive_records $after" <<<"$metrics" ||
  fail "/metrics does not report healthkit_relay_archive_records $after"
grep -q '^healthkit_relay_devices [1-9]' <<<"$metrics" || fail "/metrics reports no paired device"
printf '    ok: /metrics reports %s records\n' "$after"

if [ -n "${HKR_RESTART_CMD:-}" ]; then
  step "restarting with: $HKR_RESTART_CMD"
  bash -c "$HKR_RESTART_CMD"
  sleep 1
  wait_healthy
  r=$(api GET /v1/status "$token")
  check "device token still valid after restart" "$(code "$r")" 200
  check "records after restart" "$(body "$r" | jq -r .records)" "$after"
  r=$(api POST /v1/samples "$token" "$batch")
  check "same batch after restart: stored" "$(body "$r" | jq -r .stored)" 0
  check "same batch after restart: duplicates" "$(body "$r" | jq -r .duplicates)" 4
fi

step "smoke test passed"
