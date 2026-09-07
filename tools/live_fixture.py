"""Two synthetic runs, posted through the Python client, for the live check.

The numbers are chosen to be checked by hand: the implementing phase does two
searches in run A and three in run B, three reads against two, and spends $0.40
against $0.60, so every delta on that row is a fraction anybody can do in their
head. Run A's testing phase has a five-minute hole in it, which is what the
idle column is for.

Posted through `ammit.Run` rather than curl because the client is half of what
is being checked: a server that answers a hand-written request and not the one
its own SDK sends is a server that works in the test and not in the pipeline.
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

import ammit

A_RUN, A_NAME = "live-a-1", "LIVE-A"
B_RUN, B_NAME = "live-b-1", "LIVE-B"

# The client's sends are off the caller's thread and best-effort, so the
# fixture waits for the record rather than for the sends: it asks the server
# what it has until the counts are the ones posted.
_SETTLE_SECONDS = 30.0
_POLL_SECONDS = 0.25
_IDLE_GAP_SECONDS = 300.0
_EXPECT_A_TURNS = 4
_EXPECT_B_TURNS = 5


def calls(run: ammit.Run, phase: str, agent: str, spec: list[tuple[str, dict]]) -> None:
    """Tool calls, as the runner reports them: the tool and what it was given.

    The server classifies them - search, read, write, test, map, cli, other -
    because the classification has to be one rule for every pipeline, and a
    count each side works out for itself is two numbers for one idea.
    """
    for tool, payload in spec:
        ammit.send("call", run=run.id, phase=phase, agent=agent, tool=tool,
                   input=payload, ok=True, seconds=0.1)


def build_a() -> None:
    run = ammit.Run(A_NAME, run_id=A_RUN, tags={"mode": "live-check"})
    with run.phase("implementing"), run.session("coder", model="sonnet") as s:
        for _ in range(3):
            s.turn()
        s.spend(usd=0.40, tokens_in=100_000, tokens_out=2_000)
        calls(run, "implementing", "coder", [
            ("Grep", {"pattern": "weigh", "path": "server"}),
            ("Grep", {"pattern": "feather", "path": "server"}),
            ("Read", {"file_path": "/repo/server/main.go"}),
            ("Read", {"file_path": "/repo/server/store.go"}),
            ("Read", {"file_path": "/repo/README.md"}),
            ("Write", {"file_path": "/repo/server/compare.go"}),
        ])
    with run.phase("testing"), run.session("tester", model="haiku") as s:
        s.turn()
        s.spend(usd=0.10, tokens_in=20_000, tokens_out=500)
        calls(run, "testing", "tester", [("Bash", {"command": "pytest tests -q"})])
        s.log("AttributeError: 'NoneType' object has no attribute 'weigh'", level="text")
    # A phase that said nothing for five minutes. Nothing else in this fixture
    # is quiet, so the idle column is zero everywhere but here.
    ammit.send("log", run=A_RUN, phase="testing", agent="tester", level="text",
               text="bringing the environment up", at=time.time() - _IDLE_GAP_SECONDS)
    run.document("map", "the framework map: a chartreuse thread through every step "
                        "definition, and the scenarios that reach it", phase="implementing")
    run.finish("PASS", "the live check's first run")


def build_b() -> None:
    run = ammit.Run(B_NAME, run_id=B_RUN, tags={"mode": "live-check"})
    with run.phase("implementing"), run.session("coder", model="sonnet") as s:
        for _ in range(4):
            s.turn()
        s.spend(usd=0.60, tokens_in=150_000, tokens_out=3_000)
        calls(run, "implementing", "coder", [
            ("Grep", {"pattern": "weigh", "path": "server"}),
            ("Grep", {"pattern": "feather", "path": "server"}),
            ("Grep", {"pattern": "devour", "path": "server"}),
            ("Read", {"file_path": "/repo/server/main.go"}),
            ("Read", {"file_path": "/repo/server/store.go"}),
            ("Write", {"file_path": "/repo/server/search.go"}),
        ])
    with run.phase("testing"), run.session("tester", model="haiku") as s:
        s.turn()
        s.spend(usd=0.05, tokens_in=10_000, tokens_out=250)
        calls(run, "testing", "tester", [
            ("Bash", {"command": "pytest tests -q"}),
            ("Bash", {"command": "pytest tests -q --last-failed"}),
        ])
        s.log("AttributeError raised again, in the other run", level="text")
    run.finish("PASS", "the live check's second run")


def settled() -> bool:
    """True once the server has both runs whole."""
    url = f"{ammit.endpoint()}/compare?a={A_RUN}&b={B_RUN}"
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=5) as response:
            body = json.loads(response.read())
    except (urllib.error.URLError, OSError, ValueError):
        return False
    totals = body.get("totals", {})
    return (totals.get("a", {}).get("turns") == _EXPECT_A_TURNS
            and totals.get("b", {}).get("turns") == _EXPECT_B_TURNS)


def main() -> int:
    print(f"ammit: posting fixtures to {ammit.endpoint()}", flush=True)
    build_a()
    build_b()
    deadline = time.time() + _SETTLE_SECONDS
    while time.time() < deadline:
        if settled():
            print("ammit: both runs are in the record", flush=True)
            return 0
        time.sleep(_POLL_SECONDS)
    print("ammit: the server never showed both runs whole", file=sys.stderr)
    return 1


if __name__ == "__main__":
    os.environ.setdefault("AMMIT_URL", ammit.endpoint())
    sys.exit(main())
