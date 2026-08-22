#!/bin/sh
# Both halves of one service. Grafana in the background because it is the panel;
# ammit in the foreground because it is the thing — when it exits the container
# exits, and the restart policy means what it says.
set -eu

/run.sh >/tmp/grafana.log 2>&1 &
grafana_pid=$!

# A watchdog that outlives its own charts is fine; charts that outlive the
# watchdog are a page drawing a service that is not there.
trap 'kill "$grafana_pid" 2>/dev/null || true' INT TERM EXIT

exec ammit
