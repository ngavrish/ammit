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

# restart NAME CONFIG TICK LOG - the same binary on the same database again.
# A server that has already run is the only place some of these faults live: an
# index built by an older build, a marker over an index that is gone.
restart() {
  if [ -n "$pid" ]; then
    kill "$pid"
    wait "$pid" 2>/dev/null || true
    pid=""
  fi
  AMMIT_DB="$work/data/ammit.db" \
  AMMIT_DOCS="$work/data/documents" \
  AMMIT_CONFIG="$2" \
  AMMIT_PORT="$port" \
  AMMIT_TICK="$3" \
    "$work/ammit" >"$work/$4" 2>&1 &
  pid=$!
  curl -sS --retry 40 --retry-delay 1 --retry-connrefused -o "$work/$4.health" \
    "$base/health"
  want "$1" '"ok":true' "$work/$4.health"
}

echo "== every field every client sends, against RECORD.md"
# Before anything is built: the allowlist is a promise about senders that live
# outside this repository, and the only way to keep it is to read them. The
# runner is not always here (CI has the clients and nothing else), and the
# checker says so and passes rather than pretending it looked.
python3 "$root/tools/record_check.py"

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
want "the table says what new means"      'new. where a was zero' "$work/compare.txt"

echo "== GET /compare, as JSON"
curl -sS -o "$work/compare.json" "$base/compare?a=live-a-1&b=live-b-1"
want "json rows are keyed by phase"       '"key":"implementing"' "$work/compare.json"
want "json carries the totals row"        '"key":"TOTAL"' "$work/compare.json"
want "\$0.40 against \$0.60 is +\$0.20"     '"usd":\{"abs":0.2,"pct":50\}' "$work/compare.json"
want "run a's tokens, from the bill"      '"tokens_in":100000' "$work/compare.json"
want "the idle gap was measured"          '"idle_seconds":[1-9]' "$work/compare.json"
# The delta is the subtraction of the two numbers on the page. It was worked
# out from the unrounded seconds, so 300 against 0 published -299.9985.
want "300 seconds against none is -300"   '"idle_seconds":\{"abs":-300,"pct":-100\}' \
  "$work/compare.json"

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

# A query with no word in it is a question, not a fault in the engine.
curl -sS -o "$work/search-quotes.json" --get "$base/search" --data-urlencode 'q="""'
cat "$work/search-quotes.json"; echo
want "a query of quotes gets a sentence"  'no word in that query' "$work/search-quotes.json"
deny "and not the engine's own error"     'fts5: syntax error' "$work/search-quotes.json"

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
want "/record lists the runner's adhoc"   '"adhoc":\["allowed","head","reason"\]' \
  "$work/record.json"

echo "== POST /events with a phase body, which the runner sends on every phase"
curl -sS -o "$work/phase-end.json" -X POST "$base/events" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"phase_end","run":"live-a-1","phase":"planning","seconds":1.5,
       "text":"the planning phase said sarcophagus"}'
cat "$work/phase-end.json"; echo
deny "a phase body is not dropped"        '"dropped"' "$work/phase-end.json"

echo "== the same database, restarted, with the search index thrown away"
# What a live check is for. Every /search above ran against an index built row
# by row as the events landed, which is the one case the backfill is not in:
# a database that predates this index, or one whose index was lost, is
# searchable only if the backfill on start actually reaches the FTS table.
before="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["count"])' \
  "$work/search-kind.json")"
kill "$pid"
wait "$pid" 2>/dev/null || true
pid=""
python3 - "$work/data/ammit.db" <<'DROPSEARCH'
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
db.execute("DROP TABLE IF EXISTS search_fts")
db.execute("DROP TABLE IF EXISTS search_text")
db.commit()
kept = db.execute("select count(*) from events").fetchone()[0]
print(f"  dropped search_text and search_fts; {kept} events still in the record")
DROPSEARCH

restart "the server answers again" "$work/no-such-limits.yml" 3600 server2.log

curl -sS -o "$work/backfill-event.json" "$base/search?q=AttributeError"
want "the backfill reaches an event"      '"source":"event"' "$work/backfill-event.json"
curl -sS -o "$work/backfill-phase.json" "$base/search?q=sarcophagus"
want "and a phase body with it"           '"kind":"phase_end"' "$work/backfill-phase.json"
curl -sS -o "$work/backfill-doc.json" "$base/search?q=chartreuse"
want "and a document off the disk"        '"source":"document"' "$work/backfill-doc.json"

# What is searchable is one definition or it is two answers: a call's tool and
# the strings it was given were indexed as the event landed and left out of the
# backfill, so this query answered 3 on a live database and 0 on one built from
# the same events.
curl -sS -o "$work/backfill-call.json" "$base/search?q=pytest&kind=call"
after="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["count"])' \
  "$work/backfill-call.json")"
if [ "$before" = "$after" ]; then
  echo "  ok   a call's command, the same $after hit(s) before and after the restart"
else
  echo "  FAIL a call's command: $before hit(s) live, $after after the restart"
  exit 1
fi

echo "== an index built by the version that could not index a call"
# The state a deployment is actually in, rather than the one a dropped table
# leaves: the first version of this index filed a row only when the prose
# fields joined to something, so a call with no `why` got no row while a later
# log line did, and the high-water mark moved past both. Rebuilding the FTS
# side of that heals the prose and leaves the call unfindable for ever, which
# is what dropping search_text hides - it resets the mark to zero.
kill "$pid"
wait "$pid" 2>/dev/null || true
pid=""
python3 - "$work/data/ammit.db" <<'OLDINDEX'
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
db.execute("DELETE FROM search_text WHERE source='event' AND kind='call'")
db.execute("DROP TABLE IF EXISTS search_fts")
db.execute("UPDATE search_meta SET value='1' WHERE key='fts_generation'")
db.commit()
calls = db.execute("select count(*) from events where kind='call'").fetchone()[0]
mark = db.execute("select coalesce(max(ref),0) from search_text "
                  "where source='event'").fetchone()[0]
print(f"  {calls} call events with no index row, and the mark already at {mark}")
OLDINDEX

restart "the server answers a third time" "$work/no-such-limits.yml" 3600 server3.log
curl -sS -o "$work/old-index-call.json" "$base/search?q=pytest&kind=call"
again="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["count"])' \
  "$work/old-index-call.json")"
if [ "$before" = "$again" ]; then
  echo "  ok   the calls it skipped are read again ($again)"
else
  echo "  FAIL the calls it skipped: $before live, $again after the older index"
  exit 1
fi
curl -sS -o "$work/old-index-tool.json" "$base/search?q=Bash&kind=call"
want "and by the tool's name as well"     '"kind":"call"' "$work/old-index-tool.json"
want "the log says why it read them again" 'built by an older version' \
  "$work/server3.log"

echo "== a marker saying built, over an index that is gone"
# Dropping the FTS table is what repairing a corrupt one amounts to. The marker
# still says the index was built, so nothing would rebuild it and every search
# would answer zero for ever, silently.
kill "$pid"
wait "$pid" 2>/dev/null || true
pid=""
python3 - "$work/data/ammit.db" <<'DROPFTS'
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
db.execute("DROP TABLE IF EXISTS search_fts")
db.commit()
mark = db.execute("select value from search_meta where key='fts_generation'").fetchone()
kept = db.execute("select count(*) from search_text").fetchone()[0]
print(f"  search_fts dropped; the marker still says {mark[0]} over {kept} rows of text")
DROPFTS

restart "the server answers a fourth time" "$work/no-such-limits.yml" 3600 server4.log
curl -sS -o "$work/lost-index.json" "$base/search?q=AttributeError"
want "a lost index is rebuilt anyway"     '"source":"event"' "$work/lost-index.json"
want "and says so in one line"            'holds nothing against' "$work/server4.log"

echo "== a run archived out of the record leaves the index with it"
# archive() moves finished runs into a file of their own and its stated job is
# to leave the live database small. The index it did not touch outlived the
# events it mirrors: /search went on answering with rows whose event was gone,
# each with a fetch link that resolves to nothing.
kill "$pid"
wait "$pid" 2>/dev/null || true
pid=""
cat >"$work/limits.yml" <<YML
retention:
  days: 0.00001
  dir: $work/archive
YML

restart "the server answers a fifth time" "$work/limits.yml" 1 server5.log

curl -sS -o /dev/null -X POST "$base/events" -H 'Content-Type: application/json' \
  -d '{"kind":"log","run":"live-c-1","phase":"testing","level":"text",
       "text":"a line nobody will read again: zzzghost"}'
curl -sS -o /dev/null -X POST "$base/events" -H 'Content-Type: application/json' \
  -d '{"kind":"run_end","run":"live-c-1","verdict":"PASS",
       "summary":"the run that gets archived"}'
curl -sS -o "$work/ghost-live.json" "$base/search?q=zzzghost"
want "the word is findable while it is here" '"count":1' "$work/ghost-live.json"

# retention.days is a fraction of a day here, so the tick archives the run
# about a second after it ends. Waiting for the record rather than for a
# sleep: the assertion is about what archiving did, not about how fast.
for _ in $(seq 1 60); do
  curl -sS -o "$work/ghost-events.json" --get "$base/query" \
    --data-urlencode "sql=SELECT count(*) FROM events WHERE run='live-c-1'"
  if grep -q '\[\[0\]\]' "$work/ghost-events.json"; then
    break
  fi
  sleep 1
done
cat "$work/ghost-events.json"; echo
want "the run's events are archived"      'rows.:..0..' "$work/ghost-events.json"

curl -sS -o "$work/ghost-rows.json" --get "$base/query" \
  --data-urlencode "sql=SELECT count(*) FROM search_text WHERE run='live-c-1'"
cat "$work/ghost-rows.json"; echo
want "and its rows left search_text"      'rows.:..0..' "$work/ghost-rows.json"

curl -sS -o "$work/ghost-search.json" "$base/search?q=zzzghost"
cat "$work/ghost-search.json"; echo
want "and /search has nothing to hand back" '"count":0' "$work/ghost-search.json"

echo
echo "live check: everything above passed"
