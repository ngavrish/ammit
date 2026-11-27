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
    record_go = (AMMIT / "server/record.go").read_text()
    envelope = set(re.findall(
        r'"([^"]+)"', re.search(r"var envelope = \[\]string\{(.*?)\}", record_go, re.S).group(1)))
    block = re.search(r"var perKind = map\[string\]\[\]string\{(.*?)\n\}\n", record_go, re.S).group(1)
    per = {kind_match.group(1): set(re.findall(r'"([^"]+)"', kind_match.group(2)))
           for kind_match in re.finditer(r'"([a-z_]+)":\s*\{(.*?)\}', block, re.S)}
    docs = set(re.findall(
        r'"([^"]+)"', re.search(r"var documentFields = \[\]string\{(.*?)\}", record_go, re.S).group(1)))
    return envelope, per, docs


def dataclass_fields(tree: ast.Module) -> dict[str, set[str]]:
    """Every annotated class field, by class name: what asdict() of it yields."""
    fields_by_class = {}
    for tree_node in ast.walk(tree):
        if isinstance(tree_node, ast.ClassDef):
            named = {statement.target.id for statement in tree_node.body
                     if isinstance(statement, ast.AnnAssign) and isinstance(statement.target, ast.Name)}
            if named:
                fields_by_class[tree_node.name] = named
    return fields_by_class


def annotations_of(function: ast.FunctionDef) -> dict[str, str]:
    """Each parameter's annotation reduced to a bare name: `X | None` is X."""
    annotation_by_param = {}
    signature = function.args
    for arg in list(signature.args) + list(signature.posonlyargs) + list(signature.kwonlyargs):
        if arg.annotation is None:
            continue
        for annotation_node in ast.walk(arg.annotation):
            if isinstance(annotation_node, ast.Name) and annotation_node.id != "None":
                annotation_by_param[arg.arg] = annotation_node.id
                break
    return annotation_by_param


def spread_fields(spread: ast.AST, function: ast.FunctionDef,
                  shapes: dict[str, set[str]]) -> set[str] | None:
    """What a `**something` in a send resolves to, or None when it is unbounded.

    `**report.as_event()` is the dataclass the parameter is annotated with, and
    a dataclass is a closed list. `**extra` on a `**kwargs` parameter is not:
    that one is answered by reading the callers, in extra_at_call_sites().
    """
    annotated = annotations_of(function)
    for tree_node in ast.walk(spread):
        if (isinstance(tree_node, ast.Attribute) and tree_node.attr == "as_event"
                and isinstance(tree_node.value, ast.Name)):
            shape = shapes.get(annotated.get(tree_node.value.id, ""))
            if shape:
                return set(shape)
    return None


def dict_pairs(dict_node: ast.Dict) -> list:
    """The key and value nodes of a dict literal, paired."""
    return [(dict_node.keys[index], dict_node.values[index]) for index in range(len(dict_node.keys))]


def runsdb_sends(path: Path) -> tuple[dict[str, set[str]], set[str]]:
    """{kind: fields} for every _event()/_post('/events') call, and the kinds
    whose fields this file alone cannot bound."""
    tree = ast.parse(path.read_text())
    shapes = dataclass_fields(tree)
    sends: dict[str, set[str]] = {}
    unbounded: set[str] = set()
    for function in ast.walk(tree):
        if not isinstance(function, ast.FunctionDef):
            continue
        for tree_node in ast.walk(function):
            if not isinstance(tree_node, ast.Call):
                continue
            callee = getattr(tree_node.func, "id", None) or getattr(tree_node.func, "attr", None)
            if callee == "_event" and tree_node.args:
                kind = getattr(tree_node.args[0], "value", "?")
                fields = {keyword.arg for keyword in tree_node.keywords if keyword.arg}
                for keyword in tree_node.keywords:
                    if keyword.arg is not None:
                        continue
                    named = spread_fields(keyword.value, function, shapes)
                    if named is None:
                        unbounded.add(kind)
                    else:
                        fields |= named
                sends.setdefault(kind, set()).update(fields | ALWAYS)
            elif callee == "_post" and tree_node.args[1:] and isinstance(tree_node.args[1], ast.Dict):
                path_arg, sent = getattr(tree_node.args[0], "value", ""), tree_node.args[1]
                keys = {key_node.value for key_node in sent.keys if isinstance(key_node, ast.Constant)}
                if path_arg == "/events":
                    named = [getattr(value_node, "value", "?") for key_node, value_node in dict_pairs(sent)
                             if getattr(key_node, "value", None) == "kind"]
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
    keywords_passed: set[str] = set()
    for module_path in sorted(root.glob("*.py")):
        if module_path.name.startswith("test_"):
            continue
        try:
            tree = ast.parse(module_path.read_text())
        except SyntaxError:
            continue
        for tree_node in ast.walk(tree):
            if (isinstance(tree_node, ast.Call) and isinstance(tree_node.func, ast.Attribute)
                    and tree_node.func.attr == func
                    and getattr(tree_node.func.value, "id", "") == "runsdb"):
                keywords_passed |= {keyword.arg for keyword in tree_node.keywords if keyword.arg}
    return keywords_passed


def client_sends() -> dict[str, set[str]]:
    """Every name that looks like a field in the shipped clients."""
    fields_by_client = {}
    for path in sorted((AMMIT / "clients").rglob("*")):
        if path.is_file() and path.suffix != ".mod":
            client_source = path.read_text(errors="replace")
            fields_by_client[str(path.relative_to(AMMIT))] = (
                set(re.findall(r'"([a-z_][a-z_0-9]{1,20})"\s*[:=,]', client_source))
                | set(re.findall(r"'([a-z_][a-z_0-9]{1,20})'\s*=>", client_source)))
    python_client = AMMIT / "src/ammit/__init__.py"
    fields_by_client["src/ammit/__init__.py"] = set(re.findall(r'"([a-z_][a-z_0-9]{1,20})"\s*[:=]', python_client.read_text()))
    return fields_by_client


def main() -> int:
    envelope, per, docs = allowlist()
    known_any = set(envelope).union(*per.values())
    lossy_kinds = 0

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
                lossy_kinds += 1
                print(f"  {kind:<16} KIND NOT ON THE LIST - drops {sorted(fields - envelope)}")
                continue
            else:
                drops = sorted(field for field in fields if field not in envelope and field not in per[kind])
            note = " (unbounded spread, no list can hold it)" if kind in unbounded else ""
            print(f"  {kind:<16} {'DROPPED' if drops or note else 'ok':<8} {drops or ''}{note}")
            lossy_kinds += bool(drops) or bool(note)
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
    for client, keys in client_sends().items():
        unknown = sorted(field for field in keys if field not in known_any and field not in docs and field not in per)
        print(f"  {client:<30} unknown-to-the-allowlist: {unknown or 'none'}")

    if RUNSDB.exists():
        print(f"\n{lossy_kinds} kind(s) the runner sends lose fields.")
    else:
        print("\nchecked: the clients only. WARN above says what was skipped.")
    return 1 if lossy_kinds else 0


if __name__ == "__main__":
    sys.exit(main())
