#!/usr/bin/env python3
"""golangci-lint against a ratcheted baseline.

    golint.py [--baseline]

`go vet` was the whole of this service's Go linting, and go vet has no rule
for the defect that killed run e3d2c550: `db.Query` with no context, holding
the global mutex while SQLite computed a join for forty minutes. `noctx` has
exactly that rule, and found ten more of the same shape the first time it ran.

The debt found on the first run is real and is not paid off in one commit.
Pretending otherwise produces a red build everybody learns to ignore, so the
counts are recorded per linter and this fails when one GROWS or a new linter
starts reporting. Debt can be paid, never taken on.

Exit 0 = nothing got worse. Exit 1 = something did, and it is named.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
MODULE = os.path.join(ROOT, "server")
BASELINE = os.path.join("deploy", "go-lint-baseline.json")
_RUN_TIMEOUT_S = 600
_PINNED = "2.6.2"


def _counts() -> tuple:
    """Findings per linter, or None with a reason when the tool is absent."""
    # The version matters: a baseline taken with one release and checked with
    # another differs by rules that came and went, and the ratchet then reds on
    # a change nobody made. This baseline was taken with 2.6.2, which is what
    # CI installs; a mismatch says so rather than counting it as debt.
    exe = shutil.which("golangci-lint") or os.path.expanduser(
        "~/go/bin/golangci-lint")
    if not os.path.exists(exe):
        return None, ("golangci-lint is not installed "
                      "(go install github.com/golangci/golangci-lint/v2/"
                      "cmd/golangci-lint@latest)")
    report_path = os.path.join(tempfile.mkdtemp(), "gl.json")
    try:
        subprocess.run([exe, "run", f"--output.json.path={report_path}", "--max-same-issues=0",
                        "--max-issues-per-linter=0", "./..."],
                       capture_output=True, text=True, cwd=MODULE,
                       timeout=_RUN_TIMEOUT_S)
    except (OSError, subprocess.SubprocessError) as exc:
        return None, f"golangci-lint did not run: {exc}"
    try:
        with open(report_path, encoding="utf-8") as report_file:
            report = json.load(report_file)
    except (OSError, ValueError) as exc:
        return None, f"golangci-lint wrote no report: {exc}"
    ver = subprocess.run([exe, "version"], capture_output=True, text=True,
                         timeout=60).stdout
    if _PINNED not in ver:
        print(f"  note    golangci-lint is not {_PINNED}; the baseline was "
              f"taken with that version and the counts will not line up")
    counts: dict = {}
    for issue in report.get("Issues") or []:
        linter = issue.get("FromLinter") or "?"
        counts[linter] = counts.get(linter, 0) + 1
    return counts, ""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--baseline", action="store_true")
    parser.add_argument("--require", action="store_true",
                    help="treat a missing golangci-lint as a failure (CI)")
    options = parser.parse_args()

    counts, note = _counts()
    where = os.path.join(ROOT, BASELINE)
    if counts is None:
        print(f"  MISSING {note}")
        return 1 if options.require else 0

    if options.baseline:
        with open(where, "w", encoding="utf-8") as baseline_file:
            json.dump(counts, baseline_file, indent=1, sort_keys=True)
            baseline_file.write("\n")
        print(f"baseline written: {sum(counts.values())} finding(s) across "
              f"{len(counts)} linter(s) -> {BASELINE}")
        return 0

    try:
        with open(where, encoding="utf-8") as baseline_file:
            base = json.load(baseline_file)
    except (OSError, ValueError):
        base = {}

    grew, new, fell = [], [], []
    for linter, count in sorted(counts.items()):
        was = base.get(linter)
        if was is None:
            new.append(f"{linter}: {count} (new linter, none before)")
        elif count > was:
            grew.append(f"{linter}: {count}, was {was}")
        elif count < was:
            fell.append(f"{linter}: {count}, was {was}")
    for linter, was in sorted(base.items()):
        if linter not in counts and was:
            fell.append(f"{linter}: 0, was {was} - gone")

    for line_said in fell:
        print(f"  paid    {line_said}")
    for line_said in new + grew:
        print(f"  WORSE   {line_said}")

    if new or grew:
        print(f"GO-LINT RED: {len(new) + len(grew)} linter(s) grew. Debt here "
              f"can be paid and never taken on.")
        return 1
    total = sum(counts.values())
    if fell:
        print(f"GO-LINT GREEN: nothing grew, and {len(fell)} shrank - re-run "
              f"with --baseline to lock the gain in")
    else:
        print(f"GO-LINT GREEN: nothing grew ({total} known finding(s) held)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
