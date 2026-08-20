"""Bring an existing pipeline's history into ammit, once.

The pipeline this was written for kept its runs, phases, sessions and transcripts
in Postgres behind a service of its own — the same job ammit does, done twice.
Merging them means one store, one schema, one dashboard; it also means not
throwing away the twenty-two runs that taught us what the limits should be.

    python3 import_postgres.py --dsn postgresql://user:pass@host/runs \\
                               --db /data/ammit.db

Reads with psql (no driver), writes with sqlite3 (no driver). Idempotent: a run
already present is skipped, so this can be re-run after a failure.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import subprocess
import sys
import time
from datetime import datetime

SEP = "\x1f"


def psql(dsn: str, query: str) -> list[list[str]]:
    out = subprocess.run(["psql", dsn, "-tAF", SEP, "-c", query],
                         capture_output=True, text=True, timeout=300)
    if out.returncode != 0:
        print(f"import: {out.stderr.strip()[:200]}", file=sys.stderr)
        return []
    return [line.split(SEP) for line in out.stdout.strip().splitlines() if line]


def epoch(value: str) -> float:
    """Postgres timestamps to seconds, tolerantly."""
    if not value:
        return 0.0
    for fmt in ("%Y-%m-%d %H:%M:%S.%f%z", "%Y-%m-%d %H:%M:%S%z",
                "%Y-%m-%d %H:%M:%S.%f", "%Y-%m-%d %H:%M:%S"):
        try:
            return datetime.strptime(value.strip(), fmt).timestamp()
        except ValueError:
            continue
    return 0.0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dsn", required=True, help="postgres connection string")
    ap.add_argument("--db", default="/data/ammit.db", help="ammit's sqlite file")
    args = ap.parse_args()

    conn = sqlite3.connect(args.db)
    conn.execute("PRAGMA journal_mode=WAL")
    have = {r[0] for r in conn.execute("SELECT run FROM runs")}

    runs = psql(args.dsn, "SELECT id, ticket, started_at, coalesce(finished_at,''),"
                          " coalesce(verdict,''), coalesce(summary,'') FROM runs ORDER BY id")
    imported = 0
    for rid, ticket, started, finished, verdict, summary in runs:
        run = f"pg-{rid}"
        if run in have:
            continue
        t0 = epoch(started)
        conn.execute(
            "INSERT INTO runs (run, name, started, finished, verdict, summary,"
            " usd, turns) VALUES (?,?,?,?,?,?,0,0)",
            (run, ticket, t0, epoch(finished) or None, verdict or None, summary or None))

        for at, agent, branch, phase, kind, body in psql(
                args.dsn,
                f"SELECT at, coalesce(agent,''), coalesce(branch,''), coalesce(phase,''),"
                f" kind, replace(coalesce(body,''), chr(31), ' ') FROM logs"
                f" WHERE run_id = {rid} ORDER BY id"):
            payload = {"kind": "log", "run": run, "agent": agent, "branch": branch,
                       "phase": phase, "level": kind, "text": body[:8000]}
            conn.execute(
                "INSERT INTO events (at, kind, run, phase, session, agent, branch,"
                " payload) VALUES (?,?,?,?,?,?,?,?)",
                (epoch(at), "log", run, phase, None, agent, branch,
                 json.dumps(payload)))

        for at, agent, branch, seconds, turns, usd in psql(
                args.dsn,
                f"SELECT at, coalesce(agent,''), coalesce(branch,''),"
                f" coalesce(seconds,0), coalesce(turns,0), coalesce(usd,0)"
                f" FROM agent_calls WHERE run_id = {rid} ORDER BY id"):
            payload = {"kind": "session_end", "run": run, "agent": agent,
                       "branch": branch, "seconds": float(seconds or 0),
                       "turns": int(float(turns or 0)), "usd": float(usd or 0)}
            conn.execute(
                "INSERT INTO events (at, kind, run, phase, session, agent, branch,"
                " payload) VALUES (?,?,?,?,?,?,?,?)",
                (epoch(at), "session_end", run, None, None, agent, branch,
                 json.dumps(payload)))
            conn.execute("UPDATE runs SET usd = coalesce(usd,0) + ?,"
                         " turns = coalesce(turns,0) + ? WHERE run = ?",
                         (float(usd or 0), int(float(turns or 0)), run))

        for name, started_at, finished_at, branch in psql(
                args.dsn,
                f"SELECT name, started_at, coalesce(finished_at,''), coalesce(branch,'')"
                f" FROM phases WHERE run_id = {rid} ORDER BY id"):
            for kind, when in (("phase_start", started_at), ("phase_end", finished_at)):
                if not when:
                    continue
                conn.execute(
                    "INSERT INTO events (at, kind, run, phase, session, agent, branch,"
                    " payload) VALUES (?,?,?,?,?,?,?,?)",
                    (epoch(when), kind, run, name, None, None, branch,
                     json.dumps({"kind": kind, "run": run, "phase": name})))
        imported += 1
        conn.commit()
        print(f"import: {ticket} (run {rid}) brought over", flush=True)

    conn.commit()
    kept = conn.execute("SELECT count(*) FROM runs").fetchone()[0]
    events = conn.execute("SELECT count(*) FROM events").fetchone()[0]
    print(f"import: {imported} run(s) imported; {kept} runs and {events} events in "
          f"{args.db}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
