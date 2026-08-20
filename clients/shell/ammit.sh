#!/bin/sh
# Report from a shell script, or from any language that can run one.
#
# Every language has curl. This is the client for the ones that do not have a
# client — and for the shell steps inside a pipeline, which is where a hang is
# just as likely and just as invisible.
#
#   . ammit.sh
#   ammit_run_start "APF-1934"
#   ammit_phase_start implementing
#   ammit_turn implement "still going"
#   ammit_spend implement 0.42
#   ammit_phase_end implementing
#   ammit_run_finish PASS "31 scenarios"
#
# AMMIT_URL points it somewhere; AMMIT_DISABLE=1 silences it. Nothing here fails
# a script: reporting must not be why a run stops.

AMMIT_URL="${AMMIT_URL:-http://ammit:8099}"
AMMIT_RUN="${AMMIT_RUN:-}"

_ammit_post() {
  [ "${AMMIT_DISABLE:-0}" = "1" ] && return 0
  curl -sS -m 3 -X POST "$AMMIT_URL/events" \
       -H 'Content-Type: application/json' -d "$1" >/dev/null 2>&1 &
  return 0
}

_ammit_now() { date +%s; }

ammit_run_start() {
  AMMIT_RUN="${2:-$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')}"
  export AMMIT_RUN
  _ammit_post "{\"kind\":\"run_start\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"name\":\"$1\"}"
}

ammit_phase_start() {
  AMMIT_PHASE="$1"; export AMMIT_PHASE
  _ammit_post "{\"kind\":\"phase_start\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"phase\":\"$1\"}"
}

ammit_phase_end() {
  _ammit_post "{\"kind\":\"phase_end\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"phase\":\"$1\"}"
  AMMIT_PHASE=""
}

ammit_turn() {
  _ammit_post "{\"kind\":\"turn\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"agent\":\"$1\",\"phase\":\"${AMMIT_PHASE:-}\",\"note\":\"${2:-}\"}"
}

ammit_spend() {
  _ammit_post "{\"kind\":\"spend\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"agent\":\"$1\",\"phase\":\"${AMMIT_PHASE:-}\",\"usd\":${2:-0}}"
}

ammit_log() {
  _ammit_post "{\"kind\":\"log\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"agent\":\"$1\",\"phase\":\"${AMMIT_PHASE:-}\",\"text\":\"$(printf '%s' "${2:-}" | tr -d '"' | cut -c1-2000)\"}"
}

ammit_run_finish() {
  _ammit_post "{\"kind\":\"run_end\",\"at\":$(_ammit_now),\"run\":\"$AMMIT_RUN\",\"verdict\":\"${1:-}\",\"summary\":\"$(printf '%s' "${2:-}" | tr -d '"' | cut -c1-500)\"}"
}
