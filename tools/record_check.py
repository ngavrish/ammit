#!/usr/bin/env python3
"""Every field every client sends, against RECORD.md as server/record.go holds it.

The page says a field not on it is dropped at the door. That promise is only
worth something if what the pipeline actually sends is on the page, and the
answer to that is not in this repository: it is in the runner, which sends
twelve kinds this service has to keep. So the check reads the sender's source
and the allowlist's source and puts them side by side.

  RUNSDB=/path/to/runsdb.py python3 tools/record_check.py

The runner is not part of this repository and is not always there. Without it
the clients under clients/ are still checked and the run ends green, with a
line saying which half was skipped, because CI on a fresh clone has only the
clients.
"""

from __future__ import annotations

import ast
import os
import re
import sys
from pathlib import Path

AMMIT = Path(__file__).resolve().parent.parent
RUNSDB = Path(os.environ.get(
    "RUNSDB", "/Users/boberit/work/agentic-v-model/services/agent-runner/runsdb.py"))

# What every event carries whether or not the sender names it: _event() fills
# these in for its caller.
ALWAYS = {"kind", "at", "run"}


def allowlist() -> tuple[set[str], dict[str, set[str]], set[str]]:
    """The three lists in server/record.go, as sets."""
    src = (AMMIT / "server/record.go").read_text()
    envelope = set(re.findall(
        r'"([^"]+)"', re.search(r"var envelope = \[\]string\{(.*?)\}", src, re.S).group(1)))
    block = re.search(r"var perKind = map\[string\]\[\]string\{(.*?)\n\}\n", src, re.S).group(1)
    per = {m.group(1): set(re.findall(r'"([^"]+)"', m.group(2)))
           for m in re.finditer(r'"([a-z_]+)":\s*\{(.*?)\}', block, re.S)}
    docs = set(re.findall(
        r'"([^"]+)"', re.search(r"var documentFields = \[\]string\{(.*?)\}", src, re.S).group(1)))
    return envelope, per, docs


def dataclass_fields(tree: ast.Module) -> dict[str, set[str]]:
    """Every annotated class field, by class name: what asdict() of it yields."""
    out = {}
    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef):
            named = {s.target.id for s in node.body
                     if isinstance(s, ast.AnnAssign) and isinstance(s.target, ast.Name)}
            if named:
                out[node.name] = named
    return out


def annotations_of(fn: ast.FunctionDef) -> dict[str, str]:
    """Each parameter's annotation reduced to a bare name: `X | None` is X."""
    out = {}
    args = fn.args
    for arg in list(args.args) + list(args.posonlyargs) + list(args.kwonlyargs):
        if arg.annotation is None:
            continue
        for name in ast.walk(arg.annotation):
            if isinstance(name, ast.Name) and name.id != "None":
                out[arg.arg] = name.id
                break
    return out


def spread_fields(value: ast.AST, fn: ast.FunctionDef,
                  shapes: dict[str, set[str]]) -> set[str] | None:
    """What a `**something` in a send resolves to, or None when it is unbounded.

    `**report.as_event()` is the dataclass the parameter is annotated with, and
    a dataclass is a closed list. `**extra` on a `**kwargs` parameter is not:
    that one is answered by reading the callers, in extra_at_call_sites().
    """
    annotated = annotations_of(fn)
    for node in ast.walk(value):
        if (isinstance(node, ast.Attribute) and node.attr == "as_event"
                and isinstance(node.value, ast.Name)):
            shape = shapes.get(annotated.get(node.value.id, ""))
            if shape:
                return set(shape)
    return None


def dict_pairs(node: ast.Dict) -> list:
    """The key and value nodes of a dict literal, paired."""
    return [(node.keys[i], node.values[i]) for i in range(len(node.keys))]


def runsdb_sends(path: Path) -> tuple[dict[str, set[str]], set[str]]:
    """{kind: fields} for every _event()/_post('/events') call, and the kinds
    whose fields this file alone cannot bound."""
    tree = ast.parse(path.read_text())
    shapes = dataclass_fields(tree)
    sends: dict[str, set[str]] = {}
    unbounded: set[str] = set()
    for fn in ast.walk(tree):
        if not isinstance(fn, ast.FunctionDef):
            continue
        for node in ast.walk(fn):
            if not isinstance(node, ast.Call):
                continue
            name = getattr(node.func, "id", None) or getattr(node.func, "attr", None)
            if name == "_event" and node.args:
                kind = getattr(node.args[0], "value", "?")
                fields = {kw.arg for kw in node.keywords if kw.arg}
                for kw in node.keywords:
                    if kw.arg is not None:
                        continue
                    named = spread_fields(kw.value, fn, shapes)
                    if named is None:
                        unbounded.add(kind)
                    else:
                        fields |= named
                sends.setdefault(kind, set()).update(fields | ALWAYS)
            elif name == "_post" and node.args[1:] and isinstance(node.args[1], ast.Dict):
                path_arg, sent = getattr(node.args[0], "value", ""), node.args[1]
                keys = {k.value for k in sent.keys if isinstance(k, ast.Constant)}
                if path_arg == "/events":
                    named = [getattr(v, "value", "?") for k, v in dict_pairs(sent)
                             if getattr(k, "value", None) == "kind"]
                    sends.setdefault(named[0] if named else "?", set()).update(keys)
                elif path_arg == "/documents":
                    sends.setdefault("DOCUMENT", set()).update(keys)
    return sends, unbounded


def extra_at_call_sites(root: Path, func: str) -> set[str]:
    """The keyword names every caller of runsdb.<func>() passes.

    finish(**extra) is where run_end's payload is decided, and it is decided by
    the callers rather than by runsdb. The runner's own tests are left out: a
    test passes a field to prove the filter drops it.
    """
    found: set[str] = set()
    for py in sorted(root.glob("*.py")):
        if py.name.startswith("test_"):
            continue
        try:
            tree = ast.parse(py.read_text())
        except SyntaxError:
            continue
        for node in ast.walk(tree):
            if (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
                    and node.func.attr == func
                    and getattr(node.func.value, "id", "") == "runsdb"):
                found |= {kw.arg for kw in node.keywords if kw.arg}
    return found


def client_sends() -> dict[str, set[str]]:
    """Every name that looks like a field in the shipped clients."""
    out = {}
    for path in sorted((AMMIT / "clients").rglob("*")):
        if path.is_file() and path.suffix != ".mod":
            src = path.read_text(errors="replace")
            out[str(path.relative_to(AMMIT))] = (
                set(re.findall(r'"([a-z_][a-z_0-9]{1,20})"\s*[:=,]', src))
                | set(re.findall(r"'([a-z_][a-z_0-9]{1,20})'\s*=>", src)))
    py = AMMIT / "src/ammit/__init__.py"
    out["src/ammit/__init__.py"] = set(re.findall(r'"([a-z_][a-z_0-9]{1,20})"\s*[:=]', py.read_text()))
    return out


def main() -> int:
    envelope, per, docs = allowlist()
    known_any = set(envelope).union(*per.values())
    bad = 0

    if RUNSDB.exists():
        print(f"== the runner: {RUNSDB}")
        sends, unbounded = runsdb_sends(RUNSDB)
        extra = extra_at_call_sites(RUNSDB.parent, "finish")
        if extra:
            sends.setdefault("run_end", set()).update(extra)
            unbounded.discard("run_end")
        for kind, fields in sorted(sends.items()):
            if kind == "?":
                continue
            if kind == "DOCUMENT":
                drops = sorted(fields - docs)
            elif kind not in per:
                bad += 1
                print(f"  {kind:<16} KIND NOT ON THE LIST - drops {sorted(fields - envelope)}")
                continue
            else:
                drops = sorted(f for f in fields if f not in envelope and f not in per[kind])
            note = " (unbounded spread, no list can hold it)" if kind in unbounded else ""
            print(f"  {kind:<16} {'DROPPED' if drops or note else 'ok':<8} {drops or ''}{note}")
            bad += bool(drops) or bool(note)
        if extra:
            print(f"  run_end's **extra, from the runner's own finish() calls: {sorted(extra)}")
    else:
        # Loudly, and still green. The clients under clients/ were never the
        # half this check was written for, and a run that says "ok" without
        # having read the runner would be the same silence the allowlist was
        # dropping fields in.
        print("WARN  the runner is not here, so the half this check exists for")
        print(f"WARN  was not checked: {RUNSDB}")
        print("WARN  set RUNSDB to a runsdb.py to check it.")

    print("\n== the shipped clients under clients/")
    for name, keys in client_sends().items():
        unknown = sorted(k for k in keys if k not in known_any and k not in docs and k not in per)
        print(f"  {name:<30} unknown-to-the-allowlist: {unknown or 'none'}")

    if RUNSDB.exists():
        print(f"\n{bad} kind(s) the runner sends lose fields.")
    else:
        print("\nchecked: the clients only. WARN above says what was skipped.")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
