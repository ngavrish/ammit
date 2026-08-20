"""The server half: takes the events, keeps them, weighs them, acts.

Ammit sat beside the scales. A heart was weighed against the feather of truth,
Thoth wrote the result down, and what failed the weighing was eaten. That is the
whole design: this keeps the record, holds the measure, and devours what goes
past it.

Three jobs, in the order they matter.

**Keep.** Every event lands in one table with its run, phase and session. Nothing
is aggregated on the way in: the value of a watchdog's storage is answering a
question nobody thought of while the run was alive.

**Weigh.** Limits from a config file at every level a run has — the run, a phase,
a session, a single turn, the money. What makes this different from a timeout
inside the pipeline is only where it lives: outside the process, so a run that is
wedged cannot delay its own judgement. That is not hypothetical. A four-hour
budget enforced by a timer in the pipeline's own event loop fired at ten and a
half hours, after $345.

**Act.** Actions are commands in the config, not code here. `docker restart`, a
webhook, a signal, kubectl — the watchdog does not need to know what kind of
thing it watches, only what to run when a number crosses a line.

Standard library only, on purpose: this has to be the most boring thing running.
"""

from __future__ import annotations

import json
import os
import re
import shlex
import sqlite3
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CONFIG = os.getenv("AMMIT_CONFIG", "/config/limits.yml")
DB_PATH = os.getenv("AMMIT_DB", "/data/ammit.db")
PORT = int(os.getenv("AMMIT_PORT", "8099"))
TICK = int(os.getenv("AMMIT_TICK", "20"))
DRY = os.getenv("AMMIT_DRY_RUN", "0") == "1"

SCHEMA = """
CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      REAL    NOT NULL,
    kind    TEXT    NOT NULL,
    run     TEXT,
    phase   TEXT,
    session TEXT,
    agent   TEXT,
    branch  TEXT,
    payload TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS events_run ON events (run, at);
CREATE INDEX IF NOT EXISTS events_kind ON events (kind, at);

CREATE TABLE IF NOT EXISTS runs (
    run      TEXT PRIMARY KEY,
    name     TEXT,
    started  REAL,
    finished REAL,
    verdict  TEXT,
    summary  TEXT,
    usd      REAL    DEFAULT 0,
    turns    INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS judgements (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        REAL NOT NULL,
    run       TEXT,
    scope     TEXT NOT NULL,
    subject   TEXT,
    rule      TEXT NOT NULL,
    threshold REAL,
    observed  REAL,
    action    TEXT NOT NULL,
    outcome   TEXT
);
CREATE INDEX IF NOT EXISTS judgements_at ON judgements (at);

CREATE TABLE IF NOT EXISTS queue (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    payload   TEXT,
    requested REAL NOT NULL,
    started   REAL,
    finished  REAL,
    run       TEXT,
    state     TEXT NOT NULL DEFAULT 'waiting'
);
"""


def _open() -> sqlite3.Connection:
    os.makedirs(os.path.dirname(DB_PATH) or ".", exist_ok=True)
    conn = sqlite3.connect(DB_PATH, timeout=30, check_same_thread=False)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.row_factory = sqlite3.Row
    return conn


CONN = _open()
CONN.executescript(SCHEMA)
LOCK = threading.Lock()


def config() -> dict:
    """Two-level key/value, re-read every tick.

    Re-read rather than cached so a limit can be changed while a run is going,
    which is exactly when somebody wants to change one. No yaml library: the file
    is two levels deep and this service's whole value is having fewer moving
    parts than what it watches.
    """
    conf: dict = {}
    section = None
    try:
        text = open(CONFIG, encoding="utf-8").read()
    except OSError:
        return conf
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].rstrip()
        if not line.strip():
            continue
        if not line.startswith((" ", "\t")):
            section = line.rstrip(":").strip()
            conf[section] = {}
            continue
        if section is None:
            continue
        key, _, value = line.strip().partition(":")
        value = value.strip().strip('"').strip("'")
        conf[section][key.strip()] = (
            float(value) if re.fullmatch(r"-?\d+(\.\d+)?", value) else value)
    return conf


def store(event: dict) -> None:
    with LOCK:
        CONN.execute(
            "INSERT INTO events (at, kind, run, phase, session, agent, branch, payload)"
            " VALUES (?,?,?,?,?,?,?,?)",
            (event.get("at") or time.time(), event.get("kind", "?"), event.get("run"),
             event.get("phase"), event.get("session"), event.get("agent"),
             event.get("branch"), json.dumps(event)))
        kind, run = event.get("kind"), event.get("run")
        if kind == "run_start":
            CONN.execute("INSERT OR REPLACE INTO runs (run, name, started) VALUES (?,?,?)",
                         (run, event.get("name"), event.get("at") or time.time()))
        elif kind == "run_end":
            CONN.execute("UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?",
                         (event.get("at") or time.time(), event.get("verdict"),
                          event.get("summary"), run))
            CONN.execute("UPDATE queue SET state='done', finished=? WHERE run=?",
                         (time.time(), run))
        elif kind == "spend":
            CONN.execute("UPDATE runs SET usd = coalesce(usd,0) + ? WHERE run=?",
                         (float(event.get("usd") or 0), run))
        elif kind == "turn":
            CONN.execute("UPDATE runs SET turns = coalesce(turns,0) + 1 WHERE run=?",
                         (run,))
        CONN.commit()


def judge(scope: str, run: str, subject: str, rule: str, threshold, observed,
          action: str, outcome: str = "") -> None:
    """Write down what was weighed and what came of it.

    A limit crossed silently is not a limit, and a kill nobody recorded is
    indistinguishable from a crash.
    """
    with LOCK:
        CONN.execute(
            "INSERT INTO judgements (at, run, scope, subject, rule, threshold,"
            " observed, action, outcome) VALUES (?,?,?,?,?,?,?,?,?)",
            (time.time(), run, scope, subject, rule, threshold, observed, action,
             outcome))
        CONN.commit()
    print(f"ammit: {rule} — {observed} against {threshold} on {run or '-'}"
          f" {subject or ''} -> {action} {outcome}", flush=True)


def act(name: str, conf: dict, context: dict) -> str:
    """Run the command the config names, with this run's details substituted."""
    template = (conf.get("commands") or {}).get(name)
    if not template:
        return f"no command named {name}"
    cmd = str(template)
    for key, value in context.items():
        cmd = cmd.replace("{" + key + "}", str(value))
    if DRY:
        return f"[dry run] {cmd}"
    try:
        out = subprocess.run(shlex.split(cmd), capture_output=True, text=True,
                             timeout=180)
        return (out.stdout or out.stderr or "").strip()[:200] or f"exit {out.returncode}"
    except (OSError, subprocess.SubprocessError) as exc:
        return f"failed: {exc}"


def finish(run: str, verdict: str, summary: str) -> None:
    with LOCK:
        CONN.execute("UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?",
                     (time.time(), verdict, summary, run))
        CONN.execute("UPDATE queue SET state='done', finished=? WHERE run=?",
                     (time.time(), run))
        CONN.commit()


def open_runs() -> list:
    with LOCK:
        return CONN.execute(
            "SELECT run, name, started, usd, turns FROM runs"
            " WHERE finished IS NULL AND started > ?",
            (time.time() - 3 * 86400,)).fetchall()


def quiet_for(run: str) -> tuple[float, str, str]:
    with LOCK:
        row = CONN.execute(
            "SELECT at, agent, phase FROM events WHERE run=? ORDER BY id DESC LIMIT 1",
            (run,)).fetchone()
    return (time.time() - float(row["at"]), row["agent"] or "", row["phase"] or "") \
        if row else (0.0, "", "")


def open_phase(run: str) -> tuple[str, float]:
    with LOCK:
        rows = CONN.execute(
            "SELECT phase, kind, at FROM events WHERE run=?"
            " AND kind IN ('phase_start','phase_end') ORDER BY id", (run,)).fetchall()
    live: dict = {}
    for r in rows:
        if r["kind"] == "phase_start":
            live[r["phase"]] = float(r["at"])
        else:
            live.pop(r["phase"], None)
    if not live:
        return "", 0.0
    phase, t0 = sorted(live.items(), key=lambda kv: kv[1])[0]
    return phase, time.time() - t0


def open_sessions(run: str) -> list:
    with LOCK:
        rows = CONN.execute(
            "SELECT session, agent, kind, at FROM events WHERE run=?"
            " AND kind IN ('session_start','session_end') ORDER BY id", (run,)).fetchall()
    live: dict = {}
    for r in rows:
        if r["kind"] == "session_start":
            live[r["session"]] = (r["agent"], float(r["at"]))
        else:
            live.pop(r["session"], None)
    return [(sid, agent, time.time() - t0) for sid, (agent, t0) in live.items()]


def weigh(conf: dict) -> None:
    limits, caps = conf.get("timeouts", {}), conf.get("limits", {})
    actions = conf.get("actions", {})
    for row in open_runs():
        run, name = row["run"], row["name"] or ""
        age = time.time() - float(row["started"] or time.time())
        ctx = {"run": run, "name": name, **conf.get("context", {})}

        if limits.get("run") and age > limits["run"]:
            action = actions.get("on_run_timeout", "stop_run")
            judge("run", run, name, "timeouts.run", limits["run"], round(age), action,
                  act(action, conf, ctx))
            finish(run, "BLOCKED", f"ammit: over timeouts.run ({int(limits['run'])}s)")
            continue

        if caps.get("usd_per_run") and float(row["usd"] or 0) > caps["usd_per_run"]:
            action = actions.get("on_usd", "stop_run")
            judge("run", run, name, "limits.usd_per_run", caps["usd_per_run"],
                  round(float(row["usd"] or 0), 2), action, act(action, conf, ctx))
            finish(run, "BLOCKED", "ammit: over limits.usd_per_run")
            continue

        if caps.get("turns_per_run") and int(row["turns"] or 0) > caps["turns_per_run"]:
            judge("run", run, name, "limits.turns_per_run", caps["turns_per_run"],
                  int(row["turns"] or 0), "warn")

        quiet, agent, phase = quiet_for(run)
        if limits.get("turn") and quiet > limits["turn"]:
            action = actions.get("on_turn_timeout", "warn")
            judge("turn", run, f"{agent} {phase}".strip(), "timeouts.turn",
                  limits["turn"], round(quiet), action,
                  act(action, conf, {**ctx, "agent": agent, "phase": phase}))

        phase_name, phase_age = open_phase(run)
        if limits.get("phase") and phase_age > limits["phase"]:
            action = actions.get("on_phase_timeout", "warn")
            judge("phase", run, phase_name, "timeouts.phase", limits["phase"],
                  round(phase_age), action, act(action, conf, {**ctx, "phase": phase_name}))

        for sid, sagent, sage in open_sessions(run):
            if limits.get("session") and sage > limits["session"]:
                action = actions.get("on_session_timeout", "warn")
                judge("session", run, f"{sagent} {sid}", "timeouts.session",
                      limits["session"], round(sage), action,
                      act(action, conf, {**ctx, "agent": sagent, "session": sid}))


def pump_queue(conf: dict) -> None:
    """Start the next queued item when a slot frees, in order."""
    slots = int(float((conf.get("queue") or {}).get("parallel", 1) or 1))
    with LOCK:
        active = CONN.execute(
            "SELECT count(*) c FROM runs WHERE finished IS NULL AND started > ?",
            (time.time() - 86400,)).fetchone()["c"]
        waiting = CONN.execute(
            "SELECT id, name, payload FROM queue WHERE state='waiting'"
            " ORDER BY requested LIMIT 1").fetchone()
    if active >= slots or not waiting:
        return
    outcome = act((conf.get("actions") or {}).get("on_start", "start_run"), conf,
                  {"name": waiting["name"], "payload": waiting["payload"] or "",
                   **conf.get("context", {})})
    with LOCK:
        CONN.execute("UPDATE queue SET state='running', started=? WHERE id=?",
                     (time.time(), waiting["id"]))
        CONN.commit()
    judge("queue", "", waiting["name"], "queue.parallel", slots, active + 1,
          "started", outcome)


class Handler(BaseHTTPRequestHandler):
    def _json(self, code: int, payload) -> None:
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self) -> None:  # noqa: N802 — http.server's spelling
        length = int(self.headers.get("Content-Length") or 0)
        try:
            payload = json.loads(self.rfile.read(length) if length else b"{}")
        except ValueError:
            return self._json(400, {"error": "not json"})
        path = self.path.rstrip("/")
        if path == "/events":
            store(payload)
            return self._json(202, {"ok": True})
        if path == "/queue":
            with LOCK:
                CONN.execute("INSERT INTO queue (name, payload, requested) VALUES (?,?,?)",
                             (payload.get("name", "?"),
                              json.dumps(payload.get("payload") or {}), time.time()))
                CONN.commit()
            return self._json(201, {"queued": payload.get("name")})
        self._json(404, {"error": "no such path"})

    def do_GET(self) -> None:  # noqa: N802
        path = self.path.split("?")[0].rstrip("/")
        if path in ("", "/health"):
            return self._json(200, {"ok": True, "db": DB_PATH})
        table = {"/runs": "SELECT * FROM runs ORDER BY started DESC LIMIT 50",
                 "/judgements": "SELECT * FROM judgements ORDER BY id DESC LIMIT 200",
                 "/queue": "SELECT * FROM queue ORDER BY id DESC LIMIT 100"}.get(path)
        if table:
            with LOCK:
                rows = CONN.execute(table).fetchall()
            return self._json(200, [dict(r) for r in rows])
        if path == "/limits":
            return self._json(200, config())
        self._json(404, {"error": "no such path"})

    def log_message(self, *args) -> None:
        return


def watch() -> None:
    while True:
        try:
            conf = config()
            if conf:
                weigh(conf)
                pump_queue(conf)
        except Exception as exc:  # noqa: BLE001 — a watchdog does not die
            print(f"ammit: tick failed ({type(exc).__name__}: {exc})", flush=True)
        time.sleep(TICK)


def main() -> int:
    threading.Thread(target=watch, daemon=True, name="scales").start()
    print(f"ammit: listening on :{PORT}, limits from {CONFIG}, db {DB_PATH}"
          f"{' (dry run)' if DRY else ''}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
