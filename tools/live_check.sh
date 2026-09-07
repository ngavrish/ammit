#!/usr/bin/env bash
# The server, alive, answering. Not a unit test: the binary is built, started
# against a scratch database, given two runs through the Python client, and
# then asked the questions this repository claims it can answer.
#
# It is here because the three things it checks - the comparison, the search
# and the record - are each a claim about what happens end to end, and every
# one of them passes in isolation while being wrong in the wiring.
#
# Run it: bash tools/live_check.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
port="${AMMIT_LIVE_PORT:-8399}"
work="$(mktemp -d)"
base="http://127.0.0.1:${port}"
pid=""

cleanup() {
  if [ -n "$pid" ]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

# want NAME PATTERN FILE - the answer must contain this.
want() {
  if grep -qE "$2" "$3"; then
    echo "  ok   $1"
  else
    echo "  FAIL $1"
    echo "       wanted: $2"
    echo "       in $3:"
    sed -n '1,60p' "$3" | sed 's/^/       /'
    exit 1
  fi
}

# deny NAME PATTERN FILE - the answer must not contain this.
deny() {
  if grep -qE "$2" "$3"; then
    echo "  FAIL $1"
    echo "       did not want: $2"
    echo "       in $3:"
    sed -n '1,60p' "$3" | sed 's/^/       /'
    exit 1
  fi
  echo "  ok   $1"
}

echo "== building"
( cd "$root/server" && go build -o "$work/ammit" . )

echo "== starting on :$port with a scratch database"
AMMIT_DB="$work/data/ammit.db" \
AMMIT_DOCS="$work/data/documents" \
AMMIT_CONFIG="$work/no-such-limits.yml" \
AMMIT_PORT="$port" \
AMMIT_TICK=3600 \
  "$work/ammit" >"$work/server.log" 2>&1 &
pid=$!

curl -sS --retry 40 --retry-delay 1 --retry-connrefused -o "$work/health.json" \
  "$base/health"
want "the server answers /health" '"ok":true' "$work/health.json"

# Named first and fast: a build without these answers 404 to everything below,
# and "the fixture timed out" is not a sentence that says which endpoint is
# missing.
curl -sS -o "$work/probe-compare.json" "$base/compare"
want "this build has /compare" 'a and b are required' "$work/probe-compare.json"
curl -sS -o "$work/probe-search.json" "$base/search"
want "this build has /search" 'q is required' "$work/probe-search.json"
curl -sS -o "$work/probe-record.json" "$base/record"
want "this build publishes /record" '"page":"RECORD.md"' "$work/probe-record.json"

echo "== two runs, through the Python client"
AMMIT_URL="$base" PYTHONPATH="$root/src" python3 "$root/tools/live_fixture.py"

echo "== GET /compare, as a table"
curl -sS -H 'Accept: text/plain' -o "$work/compare.txt" \
  "$base/compare?a=live-a-1&b=live-b-1"
cat "$work/compare.txt"
# Run A searched twice, read three files and wrote one; run B searched three
# times, read two and wrote one. Every number below is that, subtracted.
want "the table has a row per phase"      '^implementing +a ' "$work/compare.txt"
want "run a's implementing calls"         '^implementing +a +2 +3 +1 ' "$work/compare.txt"
want "run b's implementing calls"         '^ +b +3 +2 +1 ' "$work/compare.txt"
want "the difference, absolute"           '^ +. +\+1 +-1 +0 ' "$work/compare.txt"
want "the difference, as a share"         '\+50\.0 +-33\.3 ' "$work/compare.txt"
want "a totals row"                       '^TOTAL +a ' "$work/compare.txt"

echo "== GET /compare, as JSON"
curl -sS -o "$work/compare.json" "$base/compare?a=live-a-1&b=live-b-1"
want "json rows are keyed by phase"       '"key":"implementing"' "$work/compare.json"
want "json carries the totals row"        '"key":"TOTAL"' "$work/compare.json"
want "\$0.40 against \$0.60 is +\$0.20"     '"usd":\{"abs":0.2,"pct":50\}' "$work/compare.json"
want "run a's tokens, from the bill"      '"tokens_in":100000' "$work/compare.json"
want "the idle gap was measured"          '"idle_seconds":[1-9]' "$work/compare.json"

echo "== GET /compare?by=agent"
curl -sS -H 'Accept: text/plain' -o "$work/compare-agent.txt" \
  "$base/compare?a=live-a-1&b=live-b-1&by=agent"
want "by agent, keyed by who"             '^coder +a ' "$work/compare-agent.txt"
want "by agent, the tester too"           '^tester +a ' "$work/compare-agent.txt"
deny "by agent is not by phase"           '^implementing ' "$work/compare-agent.txt"

echo "== GET /search"
curl -sS -o "$work/search-doc.json" "$base/search?q=chartreuse"
want "a word inside a document body"      '"source":"document"' "$work/search-doc.json"
want "the snippet carries the match"      'chartreuse' "$work/search-doc.json"
want "and the id that fetches the whole"  '"fetch":"/documents\?id=' "$work/search-doc.json"

curl -sS -o "$work/search-event.json" "$base/search?q=AttributeError"
want "a word inside an event's text"      '"source":"event"' "$work/search-event.json"
want "the run it was said in"             '"run":"live-a-1"' "$work/search-event.json"
want "and the other run said it too"      '"run":"live-b-1"' "$work/search-event.json"

curl -sS -o "$work/search-filtered.json" "$base/search?q=AttributeError&run=live-a-1"
want "the run filter keeps its own"       '"run":"live-a-1"' "$work/search-filtered.json"
deny "the run filter excludes the other"  'live-b-1' "$work/search-filtered.json"

curl -sS -o "$work/search-kind.json" "$base/search?q=pytest&kind=call"
want "a command an agent ran"             '"kind":"call"' "$work/search-kind.json"

echo "== POST /events with a field that is not on the page"
curl -sS -o "$work/dropped.json" -X POST "$base/events" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"log","run":"live-a-1","phase":"testing","text":"a line",
       "weight_of_a_feather":42,"heart":"light"}'
cat "$work/dropped.json"; echo
want "the reply names what it dropped"    '"dropped":\["heart","weight_of_a_feather"\]' \
  "$work/dropped.json"

curl -sS -o "$work/row.json" --get "$base/query" \
  --data-urlencode "sql=SELECT payload FROM events WHERE json_extract(payload,'\$.text')='a line'"
cat "$work/row.json"; echo
want "the row is there"                   'text.{1,4}:.{1,4}a line' "$work/row.json"
deny "and the extra field is not in it"   'weight_of_a_feather|heart' "$work/row.json"

curl -sS -o "$work/record.json" "$base/record"
want "/record publishes the envelope"     '"envelope":\["kind","at","run"' "$work/record.json"
want "/record counts what it turned away" '"log.weight_of_a_feather":1' "$work/record.json"

echo
echo "live check: everything above passed"
