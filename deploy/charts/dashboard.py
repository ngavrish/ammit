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

# Each family keeps a hue and shifts within it, so "planfix" reads as planning
# gone back over rather than as something unrelated.
PHASE_COLOURS = [
    {"type": "value", "options": {
        "planning":      {"color": "#5794F2", "index": 0},
        "planfix":       {"color": "#3274D9", "index": 1},
        "planreview":    {"color": "#8AB8FF", "index": 2},
        "funcreqs":      {"color": "#CD7F32", "index": 3},
        "funcreqfix":    {"color": "#A85F1E", "index": 4},
        "funcreqreview": {"color": "#E5A56B", "index": 5},
        "claims":        {"color": "#B877D9", "index": 6},
        "audit":         {"color": "#F2CC0C", "index": 7},
        "implement":     {"color": "#4FA97C", "index": 8},
        "report":        {"color": "#FF9830", "index": 9},
        "?":             {"color": "#4A5568", "index": 10},
    }},
    {"type": "value", "options": {
        "#1":  {"color": "#001F3F", "index": 20},
        "#2":  {"color": "#CD7F32", "index": 21},
        "#3":  {"color": "#264653", "index": 22},
        "#4":  {"color": "#8AB8FF", "index": 23},
        "#5":  {"color": "#A85F1E", "index": 24},
        "#6":  {"color": "#4FA97C", "index": 25},
        "#7":  {"color": "#B877D9", "index": 26},
        "#8":  {"color": "#F2CC0C", "index": 27},
        "#9":  {"color": "#E06C5A", "index": 28},
        "#10": {"color": "#5794F2", "index": 29},
        "#11": {"color": "#E5A56B", "index": 30},
        "#12": {"color": "#3274D9", "index": 31},
    }},
    {"type": "regex", "options": {"pattern": "^plan",
     "result": {"color": "#5794F2", "index": 11}}},
    {"type": "regex", "options": {"pattern": "^funcreq",
     "result": {"color": "#CD7F32", "index": 12}}},
    {"type": "regex", "options": {"pattern": ".*",
     "result": {"color": "#A0AEC0", "index": 13}}},
]




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


def table(title, desc, sql, h=9):
    """A list, not a curve. Some questions have rows for answers: what ran more
    than once, and how many times."""
    panels.append({
        "type": "table", "title": title, "description": desc,
        "transparent": True,
        "gridPos": {"h": h, "w": 24, "x": 0, "y": Y[0]}, "datasource": DS,
        "options": {"showHeader": True, "sortBy": [{"displayName": "times",
                                                    "desc": True}]},
        "fieldConfig": {"defaults": {"custom": {"align": "left"}}, "overrides": []},
        "targets": [q(sql, kind="table", time_cols=[])],
    })
    Y[0] += h


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
   "session that stopped.", "requests/min", [
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

ts("Turns by phase", "One per tool call — runsdb emits a turn from log() when the line is a tool line, so this counts tool calls, not exchanges with the model.", "turns", [
    q("""SELECT max(at) AS time, coalesce(nullif(phase,''),'no phase') AS metric, count(*) AS value FROM events WHERE kind = 'turn' AND at*1000 BETWEEN $__from AND $__to GROUP BY 2 ORDER BY 3 DESC""")], 12)

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
    q("""SELECT time, metric, value FROM (SELECT e.at AS time, coalesce(nullif(e.phase,''),'?') AS metric, coalesce(lead(e.at) OVER (PARTITION BY e.run, e.phase ORDER BY e.at), r.finished, strftime('%s','now')) AS value, e.kind AS kind FROM events e JOIN runs r ON r.run = e.run WHERE e.kind IN ('phase_start','phase_end') AND ifnull(e.phase,'') <> '') WHERE kind = 'phase_start' AND value*1000 >= $__from AND time*1000 <= $__to ORDER BY time""")], 12)

# ------------------------------------------------------ by agent session
row("By agent session")

ts("Cost by agent", "Which agent is spending the money.", "currencyUSD", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         sum(json_extract(payload,'$.usd'))
           OVER (PARTITION BY coalesce(nullif(agent,''),'?') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to"""),
    limit_of("limits.usd_per_run")], 0)

ts("What one turn carried, against limits.turn_tokens",
   "Every turn as it was sent — the system prompt, whatever was inlined into it, "
   "and the conversation so far. The per-session average below is a summary that "
   "arrives once the session is over; this is the number while the next turn has "
   "not been paid for yet.", "tokens", [
    q("""SELECT at*1000 AS time, coalesce(nullif(agent,''),'?') AS metric,
         json_extract(payload,'$.context') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'turn' AND coalesce(json_extract(payload,'$.context'),0) > 0 ORDER BY at"""),
    limit_of("limits.turn_tokens")], 0)

ts("Output per turn, by agent",
   "What the model actually wrote. Read this beside the chart on the left: three "
   "hundred tokens read for every one written is a prompt problem, not a model "
   "having a hard think.", "tokens", [
    q("""SELECT at*1000 AS time, coalesce(nullif(agent,''),'?') AS metric,
         json_extract(payload,'$.tokens_out') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'turn' AND coalesce(json_extract(payload,'$.tokens_out'),0) > 0 ORDER BY at""")], 12)

ts("Turns by agent", "Which agent is taking the turns.", "turns", [
    q("""SELECT max(at) AS time, coalesce(nullif(agent,''),'?') AS metric, count(*) AS value FROM events WHERE kind = 'turn' AND at*1000 BETWEEN $__from AND $__to GROUP BY 2 ORDER BY 3 DESC""")], 12)

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
    q("""SELECT time, metric, value FROM (SELECT e.at AS time, coalesce(nullif(e.agent,''),'?') AS metric, coalesce(lead(e.at) OVER (PARTITION BY e.run, e.agent ORDER BY e.at), r.finished, strftime('%s','now')) AS value, e.kind AS kind FROM events e JOIN runs r ON r.run = e.run WHERE e.kind IN ('session_start','session_end') AND ifnull(e.agent,'') <> '') WHERE kind = 'session_start' AND value*1000 >= $__from AND time*1000 <= $__to ORDER BY time""")], 12)

# --------------------------------------------------------------- machine
# ------------------------------------------------------------------ tokens
# What a run costs is settled by tokens; usd is that same number after a price
# list that changes. Cache read is charged at a tenth, so a run that looks
# enormous here and cheap on the cost chart is a run that is reusing context —
# which is the intended shape, not a fault.
row("Tokens")

ts("Tokens out, per run", "Generated tokens as they accrued. This is the number "
   "that moves the bill; cache read below is charged at a fraction of it.", "tokens", [
    q("""SELECT * FROM (SELECT at AS time, (SELECT r.name FROM runs r WHERE r.run = events.run) AS metric,
         sum(json_extract(payload,'$.tokens_out')) OVER (PARTITION BY (SELECT r.name FROM runs r WHERE r.run = events.run) ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 0)

ts("Tokens out, by phase", "Which phase is generating the volume.", "tokens", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(phase,''),'no phase') AS metric,
         sum(json_extract(payload,'$.tokens_out')) OVER (PARTITION BY coalesce(nullif(phase,''),'no phase') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

ts("Tokens out, by agent", "And which agent inside it.", "tokens", [
    q("""SELECT * FROM (SELECT at AS time, coalesce(nullif(agent,''),'?') AS metric,
         sum(json_extract(payload,'$.tokens_out')) OVER (PARTITION BY coalesce(nullif(agent,''),'?') ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 0)

ts("Cache read, per run",
   "Context read back rather than re-sent. Large is good here: it is the same "
   "prompt not being paid for twice.", "tokens", [
    q("""SELECT * FROM (SELECT at AS time, (SELECT r.name FROM runs r WHERE r.run = events.run) AS metric,
         sum(json_extract(payload,'$.cache_read')) OVER (PARTITION BY (SELECT r.name FROM runs r WHERE r.run = events.run) ORDER BY at) AS value
         FROM events WHERE run NOT IN (SELECT run FROM runs WHERE started*1000 > $__to OR (finished IS NOT NULL AND finished*1000 < $__from)) AND kind = 'spend' ORDER BY at) WHERE time*1000 BETWEEN $__from AND $__to""")], 12)

ts("Cache written, per run",
   "Context put into the cache. Written once, read many times — a run where this "
   "keeps climbing is a run whose prompt will not settle.", "tokens", [
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
   "runs this pipeline leaves behind, most often.", "processes", [
    q("""SELECT at AS time, json_extract(payload,'$.container') AS metric,
         json_extract(payload,'$.pids') AS value
         FROM events WHERE at*1000 BETWEEN $__from AND $__to AND kind = 'sample' ORDER BY at""")], 0)

panels.append({
    "type": "state-timeline", "title": "Phases and branches",
    "description": "What each branch was doing, minute by minute.",
    "gridPos": {"h": 8, "w": 24, "x": 0, "y": Y[0]}, "datasource": DS,
    "transparent": True,
    # A colour mode does not help here: the palette colours by field name and
    # there is one field, "value", so every phase came out the same green. A
    # discrete string state takes its colour from a mapping and nothing else.
    # The regexes at the end catch phases added later: a fix belongs to the
    # family it fixes, and an unnamed one is still not green.
    "fieldConfig": {"defaults": {"custom": {"lineWidth": 0, "fillOpacity": 85},
                                 "mappings": PHASE_COLOURS},
                    "overrides": []},
    "options": {"mergeValues": True, "showValue": "auto",
                "legend": {"displayMode": "list", "placement": "bottom",
                           "showLegend": True}},
    # Two lanes, because a phase changing and a run changing look identical on
    # one. The run lane is numbered in the order they started, so the moment a
    # band changes there is a new run — which the thin annotation line was too
    # quiet to say.
    "targets": [q("""SELECT time, metric, value, label FROM (SELECT e.at AS time, '#' || n.seq || ' ' || coalesce(r.name,'') AS metric, coalesce(lead(e.at) OVER (PARTITION BY e.run, e.phase ORDER BY e.at), r.finished, strftime('%s','now')) AS value, coalesce(nullif(e.phase,''),'?') AS label, e.kind AS kind FROM events e JOIN runs r ON r.run = e.run JOIN (SELECT run, dense_rank() OVER (ORDER BY started) AS seq FROM runs) n ON n.run = e.run WHERE e.kind IN ('phase_start','phase_end') AND ifnull(e.phase,'') <> '') WHERE kind = 'phase_start' AND value*1000 >= $__from AND time*1000 <= $__to ORDER BY time""", "table", ("time",))]})
Y[0] += 8

# ------------------------------------------------------- gates and repair
row("Gates and repair")

ts("Findings per gate, each time it ran",
   "How much each check refuses, round by round. A gate that never finds "
   "anything is a tollbooth; one whose findings do not fall between rounds is "
   "asking for something the repair cannot give.", "findings", [
    q("""SELECT at*1000 AS time, phase AS metric, findings AS value
         FROM gates ORDER BY at""")], 0, points=True)

ts("How long a round takes, by gate",
   "The check plus the repair it caused. A loop that costs more than the fault "
   "is a loop worth removing.", "s", [
    q("""SELECT at*1000 AS time, phase AS metric, seconds AS value
         FROM gates WHERE seconds > 0 ORDER BY at""")], 12, points=True)

tbl("Rounds to green, per gate",
    "How many times each check had to run before it stopped refusing, and what "
    "the rounds cost. One round is a check doing its job; three is a repair that "
    "cannot hear what is being asked.",
    """SELECT phase, coalesce(nullif(branch,''),'—') AS branch,
       max(round) AS rounds, sum(findings) AS findings_total,
       ROUND(sum(seconds)/60.0,1) AS minutes,
       max(CASE WHEN verdict='green' THEN 'reached green' ELSE verdict END) AS ended
       FROM gates GROUP BY phase, branch ORDER BY minutes DESC LIMIT 60""",
    0, [], w=24)

# ------------------------------------------------------ where time went
row("Where the time went")

tbl("Busy and idle, per run",
    "Elapsed against the part of it where nothing happened for more than two minutes.",
    """SELECT r.started AS started, r.finished AS finished, r.name AS run,
       coalesce(r.verdict,'running') AS verdict,
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
       ORDER BY r.started DESC LIMIT 50""", 0, ["started", "finished"])

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
    """SELECT started AS started, finished AS finished, name,
       coalesce(verdict,'running') AS verdict,
       CAST((coalesce(finished, strftime('%s','now')) - started)/60 AS INTEGER) AS minutes,
       ROUND(coalesce(usd,0),2) AS usd, coalesce(turns,0) AS turns,
       coalesce(summary,'') AS summary FROM runs ORDER BY started DESC LIMIT 50""",
    12, ["started", "finished"])

tbl("Limits, as they stand",
    "What every chart above is drawn against, and when it last changed.",
    """SELECT name, value, at AS changed FROM limits WHERE id IN
       (SELECT max(id) FROM limits GROUP BY name) ORDER BY name""", 0, ["changed"], w=24)


def ann(name, colour, sql):
    return {"name": name, "enable": True, "iconColor": colour, "datasource": DS,
            "target": q(sql, "table")}


dash = {
    "uid": "ammit", "title": "ammit", "tags": ["ammit"], "timezone": "browser",
    # Whatever this says, most of the chart is empty most of the time: a fixed
    # window cannot match a record that grows. Three hours is the closest fixed
    # answer to a run as long as this pipeline actually takes. The charts tab
    # does not use it — it computes the range from the runs it can see.
    "refresh": "30s", "time": {"from": "now-3h", "to": "now"},
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

row("What was done twice")

table("The same call, again",
      "One row per distinct call that ran more than once in the window. This is "
      "the question the calls table was added for: a branch ran the same search "
      "for step phrases eleven times, and nobody knew until somebody counted by "
      "hand.",
      """SELECT agent, branch, kind, count(*) AS times, signature
         FROM calls WHERE at*1000 BETWEEN $__from AND $__to
         GROUP BY agent, branch, signature HAVING times > 1
         ORDER BY times DESC LIMIT 60""")

table("The same file, however it was reached",
      "Grouped by what was touched rather than how it was touched. Read, then "
      "cat, then three sed windows is five calls and one file — which is how a "
      "single step module came to be opened fifteen times in fourteen different "
      "ways.",
      """SELECT agent, branch, target, group_concat(DISTINCT kind) AS kinds,
                count(*) AS times, count(DISTINCT signature) AS ways
         FROM calls WHERE at*1000 BETWEEN $__from AND $__to
           AND ifnull(target,'') <> ''
         GROUP BY agent, branch, target HAVING times > 1
         ORDER BY times DESC LIMIT 60""")

table("What each phase spent its calls on",
      "Every call of the window by kind. Writing and running tests are the work; "
      "everything else is what it cost to get there.",
      """SELECT phase, kind, count(*) AS times
         FROM calls WHERE at*1000 BETWEEN $__from AND $__to
         GROUP BY phase, kind ORDER BY times DESC LIMIT 80""")

# The same panels, without Grafana's vocabulary.
#
# Grafana is six hundred and thirty-one megabytes to draw forty-three charts from
# one sqlite file for one page that already exists. What each panel actually is:
# a title, a sentence, a kind, and a query returning time/metric/value. That is
# what this writes, and what ammit renders.
KIND = {
    "How long one request takes, by agent, against timeouts.request": "scatter",
    "Requests that died, per minute": "columns",
    "Request time by phase, against timeouts.request": "scatter",
    "Cost by phase": "pie",
    "Turns by phase": "bars",
    "Phase length against timeouts.phase": "scatter",
    "Idle inside a phase, against timeouts.turn": "scatter",
    "Time in a phase, minute by minute": "timeline",
    "Cost by agent": "pie",
    "What one turn carried, against limits.turn_tokens": "scatter",
    "Output per turn, by agent": "scatter",
    "Turns by agent": "bars",
    "Session length against timeouts.session": "scatter",
    "Silence between turns, against timeouts.turn": "scatter",
    "Time in a session, minute by minute": "timeline",
    "Tokens out, by phase": "stacked",
    "Tokens out, by agent": "stacked",
    "Memory by container against limits.memory_mb": "stacked",
    "Phases and branches": "timeline",
    "Findings per gate, each time it ran": "columns",
    "How long a round takes, by gate": "columns",
}

# A sentence rewritten for the way the chart is drawn now.
ABOUT = {
    "Turns by phase":
        "Tool calls in the window, by the phase that made them.",
    "Time in a phase, minute by minute":
        "When each phase began and ended. A bar that reaches the right edge is still open.",
    "Turns by agent":
        "Tool calls in the window, by the agent that made them.",
    "Time in a session, minute by minute":
        "When each agent's session began and ended, one row per agent.",
    "Phases and branches":
        "One row per run, a bar per phase, in the order the runs started.",
}

# The short heading. The title stays the panel's name - hiding, kinds and
# additions are keyed by it - and the label is what the page prints.
LABEL = {
    "Cost per run against limits.usd_per_run": "Cost per run",
    "Turns per run against limits.turns_per_run": "Turns per run",
    "How long a run has been going, against timeouts.run": "Run duration",
    "Idle time piling up, per run": "Idle time per run",
    "How long one request takes, by agent, against timeouts.request": "Request time by agent",
    "Requests that died, per minute": "Failed requests per minute",
    "Request time by phase, against timeouts.request": "Request time by phase",
    "Requests that died, in full": "Failed requests",
    "Phase length against timeouts.phase": "Phase length",
    "Idle inside a phase, against timeouts.turn": "Idle within phase",
    "Memory while each phase ran, against limits.memory_mb": "Memory by phase",
    "Time in a phase, minute by minute": "Phase timeline",
    "What one turn carried, against limits.turn_tokens": "Context per turn",
    "Output per turn, by agent": "Output per turn",
    "Session length against timeouts.session": "Session length",
    "Silence between turns, against timeouts.turn": "Silence between turns",
    "Memory while each agent ran, against limits.memory_mb": "Memory by agent",
    "Time in a session, minute by minute": "Session timeline",
    "Tokens out, per run": "Tokens out per run",
    "Tokens out, by phase": "Tokens out by phase",
    "Tokens out, by agent": "Tokens out by agent",
    "Cache read, per run": "Cache read per run",
    "Cache written, per run": "Cache written per run",
    "Tokens and what they cost": "Tokens and cost",
    "Memory by container against limits.memory_mb": "Memory by container",
    "Phases and branches": "Phases by run",
    "Findings per gate, each time it ran": "Findings per gate",
    "How long a round takes, by gate": "Round duration by gate",
    "Rounds to green, per gate": "Rounds to green",
    "Busy and idle, per run": "Busy vs idle",
    "The longest gaps": "Longest gaps",
    "What ammit did": "Judgements",
    "Limits, as they stand": "Current limits",
    "The same call, again": "Repeated calls",
    "The same file, however it was reached": "Repeated file touches",
    "What each phase spent its calls on": "Calls by phase",
    "How runs end": "Verdicts",
    "Runs finished, by day": "Runs per day",
    "Spent, by day": "Spend per day",
    "What a run costs, by day": "Cost per run, by day",
    "Turns a run takes, by day": "Turns per run, by day",
    "Where the money goes, over all runs": "Cost by agent, all time",
    "What every limit has caught": "Limit hits",
    "Gates, over all runs": "Gates, all time",
    "Repeated calls, by kind": "Repeated calls by kind",
}

spec = {"panels": []}
for p in panels:
    if p["type"] == "row":
        spec["panels"].append({"kind": "row", "title": p["title"]})
        continue
    for t in p.get("targets", []):
        pass
    spec["panels"].append({
        # How each one is drawn. Grafana had one word for every line; the
        # page has a word for what the numbers are: points that are one event
        # each, columns that are a count per bucket, stacks that are a share
        # over time, bars that are one per run or per category, spans on a
        # clock, a pie of the end of every line.
        "kind": KIND.get(p["title"]) or {"timeseries": "series", "table": "table",
                 "state-timeline": "timeline"}.get(p["type"], "series"),
        "title": p["title"],
        # The first sentence: the page shows it under the title, the rest is
        # for whoever opens this file.
        "about": ABOUT.get(p["title"], p.get("description", "").split(". ")[0].rstrip(".")),
        "unit": ((p.get("fieldConfig") or {}).get("defaults") or {}).get("unit", ""),
        "height": (p.get("gridPos") or {}).get("h", 8),
        "queries": [q.get("rawQueryText", "") for q in p.get("targets", [])],
    })

for p in spec["panels"]:
    if p["title"] in LABEL:
        p["label"] = LABEL[p["title"]]

out = pathlib.Path(__file__).with_name("panels.json")
out.write_text(json.dumps(spec, indent=1, ensure_ascii=False) + "\n")
print(f"{out}: {len([x for x in spec['panels'] if x['kind'] != 'row'])} panels")

# And the same run, seen from far enough away that one of them is a dot.
#
# Every panel above answers a question about a window: what is happening now, and
# is it going wrong. None of them answer "is this getting better" — how many runs
# have finished, how many of those were any good, what a run costs on average and
# whether that average is moving. Those are one row per run rather than one point
# per second, and they do not belong on a chart of the last twelve hours.
#
# Written as a second set rather than by aggregating the first: a time series of
# tokens per turn, summed over all time, is a number that means nothing.
lifetime = {"panels": [
    {"kind": "row", "title": "Every run there has been"},

    {"kind": "table", "title": "How runs end", "height": 7, "top": True,
     "about": "Every verdict this pipeline has recorded, and what each cost. "
              "ABANDONED is not a verdict about the work: it is what a run is "
              "called when nothing reported on it for longer than a run is "
              "allowed to take.",
     "queries": [one("""
        SELECT coalesce(nullif(verdict,''),'(none)') AS verdict, count(*) AS runs,
               round(sum(usd),2) AS usd, sum(turns) AS turns,
               round(avg(coalesce(finished,started)-started)/60,1) AS avg_minutes
        FROM runs GROUP BY 1 ORDER BY runs DESC""")]},

    # The numbers the page is opened for, as tiles beside the verdicts. One
    # row per number: what it is, the number, and the unit it is read in.
    {"kind": "stats", "title": "All time, in numbers", "label": "All time", "height": 7, "top": True,
     "about": "Totals over every run there has been. Nothing per day or per run here; those are the charts below.",
     "queries": [one("SELECT 'runs' AS metric, count(*) AS value, 'runs' AS unit FROM runs UNION ALL SELECT 'spent', round(sum(usd),2), 'currencyUSD' FROM runs UNION ALL SELECT 'runs over the cost cap', sum(coalesce(usd,0) >= (SELECT value FROM limits WHERE name = 'limits.usd_per_run' ORDER BY id DESC LIMIT 1)), 'runs' FROM runs UNION ALL SELECT 'turns', sum(coalesce(turns,0)), 'turns' FROM runs UNION ALL SELECT 'tokens out', sum(coalesce(json_extract(payload,'$.tokens_out'),0)), 'tokens' FROM events WHERE kind = 'spend' UNION ALL SELECT 'cache read', sum(coalesce(json_extract(payload,'$.cache_read'),0)), 'tokens' FROM events WHERE kind = 'spend' UNION ALL SELECT 'cache written', sum(coalesce(json_extract(payload,'$.cache_write'),0)), 'tokens' FROM events WHERE kind = 'spend' UNION ALL SELECT 'agent sessions', count(*), 'sessions' FROM events WHERE kind = 'session_end' UNION ALL SELECT 'gate rounds', count(*), 'rounds' FROM gates UNION ALL SELECT 'heal sessions', count(*), 'sessions' FROM events WHERE kind = 'spend' AND json_extract(payload,'$.agent') LIKE '%heal%' UNION ALL SELECT 'dearest session (' || coalesce((SELECT json_extract(payload,'$.agent') FROM events WHERE kind = 'spend' ORDER BY json_extract(payload,'$.usd') DESC LIMIT 1),'?') || ')', max(json_extract(payload,'$.usd')), 'currencyUSD' FROM events WHERE kind = 'spend' UNION ALL SELECT 'longest run (' || coalesce((SELECT name FROM runs WHERE finished IS NOT NULL ORDER BY finished - started DESC LIMIT 1),'?') || ')', round(max(finished - started) / 60.0), 'm' FROM runs WHERE finished IS NOT NULL")]},

    # One bar per run: the axis is runs, not time, which is why this is not
    # on the page of one run.
    {"kind": "bars", "title": "Cost per run against limits.usd_per_run", "unit": "currencyUSD", "height": 8,
     "about": "Every run there has been, in the order they started: what each cost, against the limit.",
     "queries": [one("SELECT r.started AS time, 'cost' AS metric, coalesce(r.usd,0) AS value, r.name AS label FROM runs r ORDER BY 1"),
                 one("SELECT at AS time, name AS metric, value FROM limits WHERE name = 'limits.usd_per_run' AND at*1000 <= $__to AND (at*1000 >= $__from OR at = (SELECT max(at) FROM limits WHERE name = 'limits.usd_per_run' AND at*1000 < $__from)) ORDER BY at")]},

    # One bar per run: the axis is runs, not time, which is why this is not
    # on the page of one run.
    {"kind": "bars", "title": "Turns per run against limits.turns_per_run", "unit": "turns", "height": 8,
     "about": "Every run: how many turns it took, against the limit. One per tool call, which is what the pipeline reports as a turn.",
     "queries": [one("SELECT r.started AS time, 'turns' AS metric, coalesce(r.turns,0) AS value, r.name AS label FROM runs r ORDER BY 1"),
                 one("SELECT at AS time, name AS metric, value FROM limits WHERE name = 'limits.turns_per_run' AND at*1000 <= $__to AND (at*1000 >= $__from OR at = (SELECT max(at) FROM limits WHERE name = 'limits.turns_per_run' AND at*1000 < $__from)) ORDER BY at")]},

    # One bar per run: the axis is runs, not time, which is why this is not
    # on the page of one run.
    {"kind": "bars", "title": "How long a run has been going, against timeouts.run", "unit": "s", "height": 8,
     "about": "Every run: how long it ran. A bar that reaches the dashed mark is a run ammit stopped.",
     "queries": [one("SELECT r.started AS time, 'elapsed' AS metric, coalesce(r.finished, strftime('%s','now')) - r.started AS value, r.name AS label FROM runs r ORDER BY 1"),
                 one("SELECT at AS time, name AS metric, value FROM limits WHERE name = 'timeouts.run' AND at*1000 <= $__to AND (at*1000 >= $__from OR at = (SELECT max(at) FROM limits WHERE name = 'timeouts.run' AND at*1000 < $__from)) ORDER BY at")]},

    {"kind": "columns", "title": "Runs finished, by day", "unit": "runs",
     "about": "One line per verdict. A day with more red than green is a day to "
              "go and read, and the shape over weeks is the only thing that says "
              "whether any of this is improving.",
     "queries": [one("""
        SELECT cast(strftime('%s', date(finished,'unixepoch')) AS INTEGER)*1000 AS time,
               coalesce(nullif(verdict,''),'(none)') AS metric, count(*) AS value
        FROM runs WHERE finished IS NOT NULL GROUP BY 1,2 ORDER BY 1""")]},

    # A sum is one number a day: a column.
    {"kind": "columns", "title": "Spent, by day", "unit": "currencyUSD", "height": 7,
     "about": "What every run started that day cost, added up. The bar that answers whether it mattered.",
     "queries": [one("SELECT cast(strftime('%s', date(started,'unixepoch')) AS INTEGER)*1000 AS time, 'spent' AS metric, round(sum(usd),2) AS value FROM runs GROUP BY 1 ORDER BY 1")]},

    # One row per run; the page draws the day's spread as a candle.
    {"kind": "candles", "title": "What a run costs, by day", "unit": "currencyUSD", "height": 7,
     "about": "Every run of the day as one candle: the cheapest to the dearest, the middle half, and the average. The candle that answers whether a change made runs cheaper.",
     "queries": [one("SELECT cast(strftime('%s', date(started,'unixepoch')) AS INTEGER)*1000 AS time, name AS metric, coalesce(usd,0) AS value FROM runs ORDER BY 1")]},

    # One row per run; the page draws the day's spread as a candle.
    {"kind": "candles", "title": "Turns a run takes, by day", "unit": "turns", "height": 7,
     "about": "Every run of the day as one candle, in turns: the shortest to the longest, the middle half, and the average.",
     "queries": [one("SELECT cast(strftime('%s', date(started,'unixepoch')) AS INTEGER)*1000 AS time, name AS metric, turns AS value FROM runs WHERE turns > 0 ORDER BY 1")]},

    {"kind": "table", "title": "Where the money goes, over all runs", "height": 9,
     "about": "Every agent that has ever cost anything, dearest first, with what "
              "it charges per session. One phase at thirty-four dollars of a "
              "fifty-nine dollar run is the kind of thing only this view shows.",
     "queries": [one("""
        SELECT json_extract(payload,'$.agent') AS agent, count(*) AS sessions,
               round(sum(json_extract(payload,'$.usd')),2) AS usd,
               round(avg(json_extract(payload,'$.usd')),3) AS usd_each,
               round(sum(json_extract(payload,'$.seconds'))/3600,1) AS hours
        FROM events WHERE kind='session_end'
          AND json_extract(payload,'$.agent') IS NOT NULL
        GROUP BY 1 HAVING usd > 0 ORDER BY usd DESC LIMIT 40""")]},

    {"kind": "table", "title": "What every limit has caught", "height": 9,
     "about": "Each rule, how often it has fired and what was done about it. A "
              "rule that has never fired is either a limit nothing reaches or a "
              "limit nothing can reach — and the second kind is worth finding.",
     "queries": [one("""
        SELECT rule, action, count(*) AS times,
               round(min(observed),1) AS smallest, round(max(observed),1) AS largest,
               max(threshold) AS threshold
        FROM judgements WHERE action <> 'started'
        GROUP BY rule, action ORDER BY times DESC LIMIT 40""")]},

    {"kind": "table", "title": "Gates, over all runs", "height": 8,
     "about": "How often each check refuses, and how long its repair takes. The "
              "difference between a gate that earns its cost and a tollbooth is "
              "these two numbers.",
     "queries": [one("""
        SELECT phase, count(*) AS rounds,
               sum(verdict='red') AS red, sum(verdict='green') AS green,
               sum(findings) AS findings, round(avg(seconds)/60,1) AS avg_minutes
        FROM gates GROUP BY phase ORDER BY rounds DESC LIMIT 40""")]},

    {"kind": "table", "title": "Repeated calls, by kind", "height": 8,
     "about": "The same call, across every run there has been. Counted rather "
              "than remembered: a branch read one step module twenty times and "
              "ran the same search eleven, and nobody knew until somebody "
              "counted by hand.",
     "queries": [one("""
        SELECT kind, count(*) AS calls, count(DISTINCT signature) AS distinct_calls,
               count(*) - count(DISTINCT signature) AS repeats
        FROM calls GROUP BY kind ORDER BY calls DESC""")]},
]}

for p in lifetime["panels"]:
    if p["title"] in LABEL:
        p["label"] = LABEL[p["title"]]

out3 = pathlib.Path(__file__).with_name("lifetime.json")
out3.write_text(json.dumps(lifetime, indent=1, ensure_ascii=False) + "\n")
print(f"{out3}: {len([x for x in lifetime['panels'] if x['kind'] != 'row'])} panels")
