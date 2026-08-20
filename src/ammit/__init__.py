"""Report what a long-running pipeline is doing, so something outside it can judge.

The client half of ammit. No dependencies, and it never blocks the work it
reports on: every send is best-effort and off the caller's thread, so a server
that is down costs the pipeline milliseconds and nothing else. A watchdog that
can take the pipeline with it is not a watchdog.

    import ammit

    run = ammit.Run("APF-1934", tags={"mode": "test"})
    with run.phase("implementing"):
        with run.session("implement", branch="req-3", model="sonnet") as s:
            s.turn()                       # heartbeat: this one is still thinking
            s.spend(usd=0.42, tokens_in=120_000, tokens_out=3_400)
    run.finish("PASS", "31 scenarios, 0 failures")

Every call is a fact with a timestamp. What the facts mean — too long, too
expensive, too quiet — is the server's business, and the limits live in its
config rather than in the code that reports.
"""

from __future__ import annotations

import json
import os
import threading
import time
import urllib.error
import urllib.request
import uuid

__version__ = "0.1.0"
__all__ = ["Run", "Phase", "Session", "send", "endpoint"]

_ENDPOINT = os.getenv("AMMIT_URL", "http://ammit:8099")
_TIMEOUT = float(os.getenv("AMMIT_TIMEOUT", "3"))
_ENABLED = os.getenv("AMMIT_DISABLE", "0") != "1"
_last_complaint = [0.0]


def endpoint(url: str = "") -> str:
    """Where events go. Set once here, or through AMMIT_URL."""
    global _ENDPOINT
    if url:
        _ENDPOINT = url.rstrip("/")
    return _ENDPOINT


def send(kind: str, **fields) -> None:
    """One event, best-effort, off the caller's thread.

    Reporting must never be the reason a run is slower or stops. If the server is
    unreachable the event is dropped and a line is printed once a minute rather
    than once an event.
    """
    if not _ENABLED:
        return
    body = json.dumps({"kind": kind, "at": time.time(), **fields}).encode()
    threading.Thread(target=_post, args=(body,), daemon=True).start()


def _post(body: bytes) -> None:
    req = urllib.request.Request(f"{_ENDPOINT}/events", data=body,
                                 headers={"Content-Type": "application/json"},
                                 method="POST")
    try:
        urllib.request.urlopen(req, timeout=_TIMEOUT).read()
    except (urllib.error.URLError, OSError, ValueError) as exc:
        now = time.time()
        if now - _last_complaint[0] > 60:
            _last_complaint[0] = now
            print(f"ammit: not reporting ({exc})", flush=True)


class Session:
    """One conversation with a model, or one long tool call.

    `turn()` is the heartbeat the server watches. A session that stops calling it
    is waiting on something that is not coming back — two hours inside a single
    turn, no error, no retry, is the failure this project was written for.
    """

    def __init__(self, run: "Run", agent: str, branch: str = "", model: str = ""):
        self.run, self.agent, self.branch, self.model = run, agent, branch, model
        self.id = uuid.uuid4().hex[:12]
        self.turns = 0
        self.usd = 0.0
        self._t0 = time.time()

    def __enter__(self) -> "Session":
        send("session_start", run=self.run.id, session=self.id, agent=self.agent,
             branch=self.branch, model=self.model, phase=self.run.current_phase)
        return self

    def __exit__(self, exc_type, exc, tb) -> bool:
        send("session_end", run=self.run.id, session=self.id, agent=self.agent,
             seconds=round(time.time() - self._t0, 1), turns=self.turns,
             usd=round(self.usd, 4), failed=bool(exc_type),
             error=str(exc)[:300] if exc else "")
        return False

    def turn(self, note: str = "") -> None:
        self.turns += 1
        send("turn", run=self.run.id, session=self.id, agent=self.agent,
             branch=self.branch, phase=self.run.current_phase, n=self.turns,
             note=note[:400])

    def spend(self, usd: float = 0.0, tokens_in: int = 0, tokens_out: int = 0,
              cache_read: int = 0, cache_write: int = 0) -> None:
        self.usd += usd
        send("spend", run=self.run.id, session=self.id, agent=self.agent,
             phase=self.run.current_phase, usd=usd, tokens_in=tokens_in,
             tokens_out=tokens_out, cache_read=cache_read, cache_write=cache_write)

    def log(self, text: str, level: str = "text") -> None:
        """A line of what the session actually did.

        Optional, and worth it: the difference between "this took two hours" and
        "this took two hours and here is the last thing it said" is the whole
        diagnosis.
        """
        send("log", run=self.run.id, session=self.id, agent=self.agent,
             branch=self.branch, phase=self.run.current_phase, level=level,
             text=text[:8000])


class Phase:
    def __init__(self, run: "Run", name: str):
        self.run, self.name = run, name
        self._t0 = time.time()

    def __enter__(self) -> "Phase":
        self.run.current_phase = self.name
        send("phase_start", run=self.run.id, phase=self.name)
        return self

    def __exit__(self, exc_type, exc, tb) -> bool:
        send("phase_end", run=self.run.id, phase=self.name,
             seconds=round(time.time() - self._t0, 1), failed=bool(exc_type))
        self.run.current_phase = ""
        return False


class Run:
    """One unit of work the outside world cares about — a ticket, a job, a build."""

    def __init__(self, name: str, run_id: str = "", tags: dict | None = None,
                 url: str = ""):
        if url:
            endpoint(url)
        self.id = run_id or uuid.uuid4().hex[:12]
        self.name = name
        self.current_phase = ""
        self._t0 = time.time()
        send("run_start", run=self.id, name=name, tags=tags or {})

    def phase(self, name: str) -> Phase:
        return Phase(self, name)

    def session(self, agent: str, branch: str = "", model: str = "") -> Session:
        return Session(self, agent, branch, model)

    def document(self, kind: str, body: str, phase: str = "") -> None:
        """An artefact a phase produced: a map of the codebase, the requirements,
        a report.

        The body is written to a file and the row keeps the path — a framework
        map is over a megabyte, and a database that swallows one per run is a
        database nobody wants to keep for a year. Sent synchronously, because a
        document is worth the wait a turn is not."""
        payload = json.dumps({"run": self.id, "kind": kind, "phase": phase,
                              "body": body}).encode()
        req = urllib.request.Request(f"{_ENDPOINT}/documents", data=payload,
                                     headers={"Content-Type": "application/json"},
                                     method="POST")
        try:
            urllib.request.urlopen(req, timeout=max(_TIMEOUT, 30)).read()
        except (urllib.error.URLError, OSError) as exc:
            print(f"ammit: document {kind} not stored ({exc})", flush=True)

    def note(self, text: str, **fields) -> None:
        send("note", run=self.id, text=text[:2000], **fields)

    def finish(self, verdict: str = "", summary: str = "") -> None:
        send("run_end", run=self.id, verdict=verdict, summary=summary[:2000],
             seconds=round(time.time() - self._t0, 1))

    def __enter__(self) -> "Run":
        return self

    def __exit__(self, exc_type, exc, tb) -> bool:
        self.finish("FAILED" if exc_type else "", str(exc)[:300] if exc else "")
        return False
