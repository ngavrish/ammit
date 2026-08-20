"""Build the dashboard.

Twenty-eight panels of near-identical JSON is not a thing to edit by hand, and a
dashboard nobody can regenerate is a dashboard that drifts from what the server
records. This writes dashboards/ammit.json; the file is committed, so a fresh
install needs neither python nor this script.

    python3 deploy/grafana/dashboard.py

Grafana can still edit the panels in the interface and keep the change — the
file is where a new install starts, not a ceiling.
"""

import json
import pathlib

DS = {"type": "frser-sqlite-datasource", "uid": "ammit"}

# Every limit line, drawn the way a level is drawn on a trading screen: red,
# dashed, held flat until somebody moves it.
LIMIT_LOOK = {
    "matcher": {"id": "byRegexp", "options": "/^(timeouts|limits|queue|retention|sample)\\./"},
    "properties": [
        {"id": "color", "value": {"mode": "fixed", "fixedColor": "red"}},
        {"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 8]}},
        {"id": "custom.lineWidth", "value": 2},
        {"id": "custom.fillOpacity", "value": 0},
        {"id": "custom.lineInterpolation", "value": "stepAfter"},
        {"id": "custom.showPoints", "value": "never"},
    ],
}

panels, Y = [], [0]


def one(sql):
    return " ".join(sql.split())


def q(sql, kind="time series", time_cols=("time",)):
    # Anything named here is handed to the plugin as unix *seconds* and turned
    # into milliseconds by it. Multiplying by 1000 in the SQL as well puts every
    # point in 1903, where the panel is empty and nothing says why.
    return {"queryType": kind, "timeColumns": list(time_cols),
            "rawQueryText": one(sql), "queryText": one(sql)}


def limit_of(name):
    # The window is 12 hours and a limit set last week is still the limit. Take
    # every change inside the window, plus the one in force when it opened, or
    # the line vanishes exactly when it matters.
    return q(f"SELECT at AS time, name AS metric, value FROM limits "
             f"WHERE name = '{name}' AND at*1000 <= $__to "
             f"AND (at*1000 >= $__from OR at = (SELECT max(at) FROM limits "
             f"WHERE name = '{name}' AND at*1000 < $__from)) ORDER BY at")


def row(title):
    panels.append({"type": "row", "title": title, "collapsed": False,
                   "gridPos": {"h": 1, "w": 24, "x": 0, "y": Y[0]}, "panels": []})
    Y[0] += 1


def ts(title, desc, unit, targets, x, w=12, h=8, points=False):
    # Full width, one to a line. Half-width panels put twelve hours of a run into
    # six hundred pixels, where a spike and a plateau look the same.
    x, w = 0, 24
    panels.append({
        "type": "timeseries", "title": title, "description": desc,
        # Framed inside ammit's own page, a grid of cards reads as a second
        # product. Transparent panels let the page's background carry through.
        "transparent": True,
        "gridPos": {"h": h, "w": w, "x": x, "y": Y[0]}, "datasource": DS,
        "fieldConfig": {"defaults": {
            # The plugin returns every series as a field called "value" carrying
            # the metric as a label, so Grafana shows `value {metric="..."}` and
            # the override below — anchored on the limit's own name — never
            # matches. Name the series after the label and both the legend and
            # the dashed limit line come right.
            "displayName": "${__field.labels.metric}",
            "unit": unit, "min": 0,
            "custom": {"lineWidth": 2, "fillOpacity": 6, "spanNulls": True,
                       "showPoints": "always" if points else "never",
                       "pointSize": 5}},
            "overrides": [LIMIT_LOOK]},
        "options": {"legend": {"displayMode": "list", "placement": "bottom",
                               "showLegend": True},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
        "targets": targets})
    if x + w >= 24:
        Y[0] += h


def tbl(title, desc, sql, x, time_cols, w=12, h=9):
    x, w = 0, 24
    panels.append({"type": "table", "title": title, "description": desc,
                   "transparent": True,
                   "gridPos": {"h": h, "w": w, "x": x, "y": Y[0]}, "datasource": DS,
                   "fieldConfig": {"defaults": {}, "overrides": []},
                   "targets": [q(sql, "table", time_cols)]})
    if x + w >= 24:
        Y[0] += h


# --------------------------------------------------------------- the run
row("The run")

ts("Cost per run against limits.usd_per_run",
   "Each line is one run's spend as it accrued. The dashed line is the limit in "
   "force at that moment: edit it on the ammit page and the line bends here.",
   "currencyUSD", [
    q("""SELECT * FROM (SELECT e.at AS time, r.name AS metric,
         sum(json_extract(e.payload,'$.usd')) OVER (PARTITION BY e.run ORDER BY e.at) AS value
         FROM events e JOIN runs r ON r.run = e.run WHERE e.run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND e.kind = 'spend' ORDER BY e.at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("limits.usd_per_run")], 0)

ts("Turns per run against limits.turns_per_run", "One per tool call, which is what the pipeline reports as a turn: the heartbeat that proves a session is still moving. Not the number of exchanges with the model.",
   "short", [
    q("""SELECT * FROM (SELECT e.at AS time, r.name AS metric,
         count(*) OVER (PARTITION BY e.run ORDER BY e.at) AS value
         FROM events e JOIN runs r ON r.run = e.run WHERE e.run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND e.kind = 'turn' ORDER BY e.at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("limits.turns_per_run")], 12)

ts("How long a run has been going, against timeouts.run",
   "A line that reaches the dashed one is a run ammit stopped; the annotation there "
   "says what it ran to stop it.", "s", [
    q("""SELECT CAST(e.at/60 AS INTEGER)*60 AS time, r.name AS metric,
         max(e.at - r.started) AS value FROM events e JOIN runs r ON r.run = e.run
         WHERE r.started IS NOT NULL GROUP BY 1, 2 ORDER BY 1"""),
    limit_of("timeouts.run")], 0)

ts("Idle time piling up, per run",
   "Every gap over two minutes, added up as the run went. A staircase that climbs as "
   "fast as the run is a run that is mostly waiting: 377 of 636 minutes, once.", "s", [
    q("""SELECT * FROM (SELECT at AS time, name AS metric,
         sum(idle) OVER (PARTITION BY run ORDER BY at) AS value FROM (
           SELECT e.at AS at, e.run AS run, r.name AS name,
             CASE WHEN e.at - lag(e.at) OVER (PARTITION BY e.run ORDER BY e.at) > 120
                  THEN e.at - lag(e.at) OVER (PARTITION BY e.run ORDER BY e.at)
                  ELSE 0 END AS idle
           FROM events e JOIN runs r ON r.run = e.run WHERE e.run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND e.run IS NOT NULL)
         ORDER BY 1) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

# ----------------------------------------------------- requests to a model
row("Requests to the model")

ts("How long one request takes, by agent, against timeouts.request",
   "One point per wait for the model: ask, and get an answer back. Inside the run "
   "this is invisible — a call that never returns has no error and no retry — so it "
   "is timed out here, where something can act on it.", "s", [
    q("""SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         json_extract(payload,'$.seconds') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'request_end' ORDER BY at"""),
    limit_of("timeouts.request")], 0, points=True)

ts("Requests that died, per minute",
   "A request that came back as an error rather than an answer, by agent. A row here "
   "and a gap on the chart beside it is a retry; a row here and nothing after it is a "
   "session that stopped.", "short", [
    q("""SELECT CAST(at/60 AS INTEGER)*60 AS time,
         coalesce(nullif(agent,''),'?') AS metric, count(*) AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'request_end'
         AND json_extract(payload,'$.ok') = 0 GROUP BY 1, 2 ORDER BY 1""")], 12)

ts("Request time by phase, against timeouts.request",
   "The same waits, grouped by what the run was doing. A design phase that waits "
   "twice as long per request as an implementing one is a prompt problem, not a "
   "network problem.", "s", [
    q("""SELECT at AS time, coalesce(nullif(phase,''),'no phase') AS metric,
         json_extract(payload,'$.seconds') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'request_end' ORDER BY at"""),
    limit_of("timeouts.request")], 0, points=True)

tbl("Requests that died, in full",
    "What the error was, whose request it was, and how long it had been waiting.",
    """SELECT at AS at, run, coalesce(nullif(agent,''),'?') AS agent,
       coalesce(nullif(phase,''),'') AS phase,
       ROUND(json_extract(payload,'$.seconds'),1) AS seconds,
       json_extract(payload,'$.error') AS error
       FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'request_end' AND json_extract(payload,'$.ok') = 0
       ORDER BY id DESC LIMIT 100""", 12, ["at"], h=8)

# -------------------------------------------------------------- by phase
row("By phase")

ts("Cost by phase", "What each phase is costing, as it accrues. \"What is the design "
   "costing us\" is this chart and no other.", "currencyUSD", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(phase,''),'no phase') AS metric,
         sum(json_extract(payload,'$.usd'))
           OVER (PARTITION BY coalesce(nullif(phase,''),'no phase') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("limits.usd_per_run")], 0)

ts("Turns by phase", "One per tool call — runsdb emits a turn from log() when the line is a tool line, so this counts tool calls, not exchanges with the model.", "short", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(phase,''),'no phase') AS metric,
         count(*) OVER (PARTITION BY coalesce(nullif(phase,''),'no phase') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'turn' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

ts("Phase length against timeouts.phase", "One point per finished phase.", "s", [
    q("""SELECT at AS time, coalesce(nullif(phase,''),'?') AS metric,
         json_extract(payload,'$.seconds') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'phase_end' ORDER BY at"""),
    limit_of("timeouts.phase")], 0, points=True)

ts("Idle inside a phase, against timeouts.turn",
   "The gap between one thing happening and the next, within a phase. Which phase the "
   "waiting happens in is the difference between a slow model and a stuck gate.", "s", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(phase,''),'no phase') AS metric,
         at - lag(at) OVER (PARTITION BY run, coalesce(phase,'') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND run IS NOT NULL
         AND kind IN ('turn','log','session_start','phase_start') ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("timeouts.turn")], 12)

ts("Memory while each phase ran, against limits.memory_mb",
   "Every sampled container added up, labelled with the phase that was open when the "
   "reading was taken. It is a claim about time, not about blame: several branches "
   "share a worker.", "mbytes", [
    q("""SELECT t AS time, phase AS metric, mb AS value FROM (
           SELECT s.at AS t, coalesce((SELECT p.phase FROM events p
             WHERE p.kind = 'phase_start' AND ifnull(p.phase,'') <> '' AND p.at <= s.at
             ORDER BY p.at DESC LIMIT 1),'no phase') AS phase,
             sum(json_extract(s.payload,'$.memory_mb')) AS mb
           FROM events s WHERE s.at*1000 BETWEEN $__from AND $__to AND s.kind = 'sample' GROUP BY s.at) ORDER BY 1"""),
    limit_of("limits.memory_mb")], 0)

ts("Time in a phase, minute by minute",
   "How long the open phase has been open. A climb that does not reset is a phase "
   "nothing is ending.", "s", [
    q("""SELECT CAST(e.at/60 AS INTEGER)*60 AS time,
         coalesce(nullif(e.phase,''),'no phase') AS metric,
         max(e.at - (SELECT p.at FROM events p WHERE p.kind = 'phase_start'
             AND p.run = e.run AND p.phase = e.phase AND p.at <= e.at
             ORDER BY p.at DESC LIMIT 1)) AS value
         FROM events e WHERE e.at*1000 BETWEEN $__from AND $__to AND e.run IS NOT NULL AND ifnull(e.phase,'') <> ''
         GROUP BY 1, 2 ORDER BY 1"""),
    limit_of("timeouts.phase")], 12)

# ------------------------------------------------------ by agent session
row("By agent session")

ts("Cost by agent", "Which agent is spending the money.", "currencyUSD", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         sum(json_extract(payload,'$.usd'))
           OVER (PARTITION BY coalesce(nullif(agent,''),'?') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("limits.usd_per_run")], 0)

ts("Turns by agent", "Which agent is taking the turns.", "short", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         count(*) OVER (PARTITION BY coalesce(nullif(agent,''),'?') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'turn' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

ts("Session length against timeouts.session", "One point per finished session, by agent.",
   "s", [
    q("""SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         json_extract(payload,'$.seconds') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'session_end' ORDER BY at"""),
    limit_of("timeouts.session")], 0, points=True)

ts("Silence between turns, against timeouts.turn",
   "The gap between one turn and the next, per agent.", "s", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         at - lag(at) OVER (PARTITION BY run, coalesce(agent,'') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind IN ('turn','session_start') AND run IS NOT NULL ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("timeouts.turn")], 12)

ts("Memory while each agent ran, against limits.memory_mb",
   "The same readings, labelled with the session that was open when they were taken.",
   "mbytes", [
    q("""SELECT t AS time, agent AS metric, mb AS value FROM (
           SELECT s.at AS t, coalesce((SELECT a.agent FROM events a
             WHERE a.kind = 'session_start' AND ifnull(a.agent,'') <> '' AND a.at <= s.at
             ORDER BY a.at DESC LIMIT 1),'nobody') AS agent,
             sum(json_extract(s.payload,'$.memory_mb')) AS mb
           FROM events s WHERE s.at*1000 BETWEEN $__from AND $__to AND s.kind = 'sample' GROUP BY s.at) ORDER BY 1"""),
    limit_of("limits.memory_mb")], 0)

ts("Time in a session, minute by minute",
   "How long the open session has been open, by agent.", "s", [
    q("""SELECT CAST(e.at/60 AS INTEGER)*60 AS time,
         coalesce(nullif(e.agent,''),'?') AS metric,
         max(e.at - (SELECT a.at FROM events a WHERE a.kind = 'session_start'
             AND a.run = e.run AND a.agent = e.agent AND a.at <= e.at
             ORDER BY a.at DESC LIMIT 1)) AS value
         FROM events e WHERE e.at*1000 BETWEEN $__from AND $__to AND e.run IS NOT NULL AND ifnull(e.agent,'') <> ''
         GROUP BY 1, 2 ORDER BY 1"""),
    limit_of("timeouts.session")], 12)

# --------------------------------------------------------------- machine
# ------------------------------------------------------------------ tokens
# What a run costs is settled by tokens; usd is that same number after a price
# list that changes. Cache read is charged at a tenth, so a run that looks
# enormous here and cheap on the cost chart is a run that is reusing context —
# which is the intended shape, not a fault.
row("Tokens")

ts("Tokens out, per run", "Generated tokens as they accrued. This is the number "
   "that moves the bill; cache read below is charged at a fraction of it.", "short", [
    q("""SELECT * FROM (SELECT at AS time, (SELECT r.name FROM runs r WHERE r.run = events.run) AS metric,
         sum(json_extract(payload,'$.tokens_out')) OVER (PARTITION BY (SELECT r.name FROM runs r WHERE r.run = events.run) ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 0)

ts("Tokens out, by phase", "Which phase is generating the volume.", "short", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(phase,''),'no phase') AS metric,
         sum(json_extract(payload,'$.tokens_out')) OVER (PARTITION BY coalesce(nullif(phase,''),'no phase') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

ts("Tokens out, by agent", "And which agent inside it.", "short", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         sum(json_extract(payload,'$.tokens_out')) OVER (PARTITION BY coalesce(nullif(agent,''),'?') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 0)

ts("Cache read, per run",
   "Context read back rather than re-sent. Large is good here: it is the same "
   "prompt not being paid for twice.", "short", [
    q("""SELECT * FROM (SELECT at AS time, (SELECT r.name FROM runs r WHERE r.run = events.run) AS metric,
         sum(json_extract(payload,'$.cache_read')) OVER (PARTITION BY (SELECT r.name FROM runs r WHERE r.run = events.run) ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

ts("Cache written, per run",
   "Context put into the cache. Written once, read many times — a run where this "
   "keeps climbing is a run whose prompt will not settle.", "short", [
    q("""SELECT * FROM (SELECT at AS time, (SELECT r.name FROM runs r WHERE r.run = events.run) AS metric,
         sum(json_extract(payload,'$.cache_write')) OVER (PARTITION BY (SELECT r.name FROM runs r WHERE r.run = events.run) ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 0)

tbl("Tokens and what they cost",
    "Every run in the window: what it generated, what it read back, and the bill.",
    """SELECT r.started AS started, r.name AS run, coalesce(r.verdict,'running') AS verdict,
       CAST(sum(json_extract(e.payload,'$.tokens_out')) AS INTEGER) AS tokens_out,
       CAST(sum(json_extract(e.payload,'$.cache_read')) AS INTEGER) AS cache_read,
       CAST(sum(json_extract(e.payload,'$.cache_write')) AS INTEGER) AS cache_write,
       ROUND(sum(json_extract(e.payload,'$.usd')), 2) AS usd
       FROM events e JOIN runs r ON r.run = e.run
       WHERE e.kind = 'spend' AND e.run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from))
       GROUP BY e.run ORDER BY r.started DESC LIMIT 50""", 12, ["started"])

row("The machine")

ts("Memory by container against limits.memory_mb",
   "Read from outside every sample.every seconds. A worker the kernel kills leaves no "
   "evidence of its own: the process that would have said so is the one that died.",
   "mbytes", [
    q("""SELECT at AS time, json_extract(payload,'$.container') AS metric,
         json_extract(payload,'$.memory_mb') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'sample' ORDER BY at"""),
    limit_of("limits.memory_mb")], 0)

ts("CPU by container", "Percent of one core, as the container client reports it.",
   "percent", [
    q("""SELECT at AS time, json_extract(payload,'$.container') AS metric,
         json_extract(payload,'$.cpu_pct') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'sample' ORDER BY at""")], 12)

ts("Processes by container",
   "A count that climbs and never falls is something not being reaped — the browser "
   "runs this pipeline leaves behind, most often.", "short", [
    q("""SELECT at AS time, json_extract(payload,'$.container') AS metric,
         json_extract(payload,'$.pids') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'sample' ORDER BY at""")], 0)

panels.append({
    "type": "state-timeline", "title": "Phases and branches",
    "description": "What each branch was doing, minute by minute.",
    "gridPos": {"h": 8, "w": 24, "x": 0, "y": Y[0]}, "datasource": DS,
    "transparent": True,
    "fieldConfig": {"defaults": {"custom": {"lineWidth": 0, "fillOpacity": 80}},
                    "overrides": []},
    "options": {"mergeValues": True, "showValue": "auto",
                "legend": {"displayMode": "list", "placement": "bottom",
                           "showLegend": True}},
    "targets": [q("""SELECT CAST(at/60 AS INTEGER)*60 AS time,
                     coalesce(nullif(phase,''), agent, '?') AS value
                     FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind IN ('turn','log','session_start')
                     GROUP BY 1 ORDER BY 1""", "table", ("time",))]})
Y[0] += 8

# ------------------------------------------------------ where time went
row("Where the time went")

tbl("Busy and idle, per run",
    "Elapsed against the part of it where nothing happened for more than two minutes.",
    """SELECT r.name AS run, coalesce(r.verdict,'running') AS verdict,
       CAST((coalesce(r.finished, strftime('%s','now')) - r.started)/60 AS INTEGER) AS elapsed_min,
       CAST(((coalesce(r.finished, strftime('%s','now')) - r.started) - coalesce(g.idle,0))/60
            AS INTEGER) AS busy_min,
       CAST(coalesce(g.idle,0)/60 AS INTEGER) AS idle_min,
       ROUND(100.0*coalesce(g.idle,0) /
             max(1.0, coalesce(r.finished, strftime('%s','now')) - r.started), 1) AS idle_pct,
       ROUND(coalesce(r.usd,0),2) AS usd
       FROM runs r LEFT JOIN (
         SELECT run, sum(gap) AS idle FROM (
           SELECT run, CASE WHEN at - lag(at) OVER (PARTITION BY run ORDER BY at) > 120
                            THEN at - lag(at) OVER (PARTITION BY run ORDER BY at)
                            ELSE 0 END AS gap
           FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND run IS NOT NULL) GROUP BY run) g ON g.run = r.run
       ORDER BY r.started DESC LIMIT 50""", 0, [])

tbl("The longest gaps",
    "Every stretch over five minutes where nothing was reported, newest first. What was "
    "open when it started is the first place to look.",
    """SELECT * FROM (SELECT at AS at, run, coalesce(nullif(phase,''),'') AS phase,
       coalesce(nullif(agent,''),'') AS agent, CAST(gap/60 AS INTEGER) AS minutes
       FROM (SELECT at, run, phase, agent,
             at - lag(at) OVER (PARTITION BY run ORDER BY at) AS gap
             FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND run IS NOT NULL)
       WHERE gap > 300 ORDER BY at DESC LIMIT 50) WHERE at*1000 BETWEEN $__from AND $__to""", 12, ["at"])

tbl("What ammit did",
    "Every limit crossed, what it did about it, and what the command said back.",
    """SELECT at AS at, scope, coalesce(subject,'') AS subject, rule,
       threshold, observed, action, coalesce(outcome,'') AS outcome
       FROM judgements ORDER BY id DESC LIMIT 200""", 0, ["at"])

tbl("Runs", "",
    """SELECT started AS started, name, coalesce(verdict,'running') AS verdict,
       CAST((coalesce(finished, strftime('%s','now')) - started)/60 AS INTEGER) AS minutes,
       ROUND(coalesce(usd,0),2) AS usd, coalesce(turns,0) AS turns,
       coalesce(summary,'') AS summary FROM runs ORDER BY started DESC LIMIT 50""",
    12, ["started"])

tbl("Limits, as they stand",
    "What every chart above is drawn against, and when it last changed.",
    """SELECT name, value, at AS changed FROM limits WHERE id IN
       (SELECT max(id) FROM limits GROUP BY name) ORDER BY name""", 0, ["changed"], w=24)


def ann(name, colour, sql):
    return {"name": name, "enable": True, "iconColor": colour, "datasource": DS,
            "target": q(sql, "table")}


dash = {
    "uid": "ammit", "title": "ammit", "tags": ["ammit"], "timezone": "browser",
    # A run may not exceed timeouts.run, four hours as it stands, so twelve hours
    # of window is three quarters of empty chart to the left of every line. Six
    # covers the longest run this is allowed to watch, with room either side.
    "refresh": "30s", "time": {"from": "now-6h", "to": "now"},
    "annotations": {"list": [
        ann("limits crossed", "red",
            """SELECT at AS time, rule || ': ' || coalesce(observed,'?') || ' vs '
               || coalesce(threshold,'?') || ' -> ' || action AS text
               FROM judgements WHERE action <> 'started' ORDER BY at"""),
        ann("runs", "blue",
            """SELECT started AS time, name || ' started' AS text FROM runs ORDER BY started"""),
        ann("limits changed", "yellow",
            """SELECT * FROM (SELECT at AS time, name || ' set to ' || value AS text FROM
               (SELECT at, name, value, lag(value) OVER (PARTITION BY name ORDER BY at)
                AS prev FROM limits) WHERE prev IS NULL OR prev <> value ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    ]},
    "panels": panels,
}

out = pathlib.Path(__file__).with_name("dashboards") / "ammit.json"
out.write_text(json.dumps(dash, indent=1) + "\n")
charts = [p for p in panels if p["type"] != "row"]
print(f"{out}: {len(panels) - len(charts)} rows, {len(charts)} panels")
