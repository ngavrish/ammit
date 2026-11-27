#!/usr/bin/env python3
import ast

import subprocess
import sys
from pathlib import Path

from collections import Counter

OUTPUT_CALLS = {"print", "write", "debug", "info", "warning", "warn", "error", "exception", "critical", "log",
                "fatal", "fail", "exit", "echo", "Exit", "SystemExit"}
GO_OUTPUT = ("fmt.Print", "fmt.Fprint", "fmt.Sprint", "fmt.Errorf", "log.", "errors.New", "slog.", "t.Error", "t.Fatal")
MIN_NAME_LENGTH = 3
GENERATED_MARK = "DO NOT EDIT"
LIBRARY_MARK = "noqa: naming"
ALLOWED = {"_", "ok", "id", "db", "io"}
VAGUE = {"got", "out", "res", "ret", "rv", "tmp", "temp", "val", "value", "values", "data", "obj", "item", "items",
         "thing", "things", "stuff", "info", "foo", "bar", "baz", "result", "results", "doc", "raw", "buf", "arr",
         "lst", "dct", "elem", "entry", "entries", "said", "seen", "hit", "rest", "head", "body", "text", "line",
         "row", "rows", "found", "bad", "todo", "src", "var", "args", "kwargs", "params", "opts", "cfg",
         "fh", "ap", "sub", "node", "part", "parts", "key", "name", "acc", "cur", "prev", "nxt"}


def git(*git_args: str) -> str:
    return subprocess.run(["git", *git_args], check=True, capture_output=True, text=True).stdout


def base() -> str:
    ref = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    return git("merge-base", "HEAD", ref).strip()


def changed(merge_base: str) -> list[str]:
    return git("diff", "--name-only", "--diff-filter=AM", merge_base, "HEAD").splitlines()


def at_head(path: str) -> str:
    return git("show", f"HEAD:{path}")


def hunks(merge_base: str, path: str) -> tuple[set[int], Counter]:
    added, removed, new_line_no = set(), Counter(), 0
    for diff_line in git("diff", "-U0", merge_base, "HEAD", "--", path).splitlines():
        if diff_line.startswith("@@"):
            new_line_no = int(diff_line.split()[2][1:].split(",")[0])
        elif diff_line.startswith("+") and not diff_line.startswith("+++"):
            added.add(new_line_no)
            new_line_no += 1
        elif diff_line.startswith("-") and not diff_line.startswith("---"):
            removed[diff_line[1:].strip()] += 1
    return added, removed


def cyrillic(code_text: str) -> bool:
    return any("Ѐ" <= letter <= "ӿ" for letter in code_text)


def short(identifier: str) -> bool:
    bare = identifier.strip("_").lower()
    dunder = identifier.startswith("__") and identifier.endswith("__")
    return identifier not in ALLOWED and not dunder and (len(bare) < MIN_NAME_LENGTH or bare in VAGUE)


def generated(file_text: str) -> bool:
    return GENERATED_MARK in "\n".join(file_text.splitlines()[:5])


def bound_names(tree_node: ast.AST) -> list:
    if isinstance(tree_node, ast.Name) and isinstance(tree_node.ctx, ast.Store) and not tree_node.id.startswith("_"):
        return [(tree_node.id, tree_node.lineno)]
    if isinstance(tree_node, ast.arg):
        return [(tree_node.arg, tree_node.lineno)]
    if isinstance(tree_node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
        return [(tree_node.name, tree_node.lineno)]
    if isinstance(tree_node, ast.ExceptHandler) and tree_node.name:
        return [(tree_node.name, tree_node.lineno)]
    if isinstance(tree_node, ast.alias) and tree_node.asname:
        return [(tree_node.asname, tree_node.lineno)]
    return []


def call_name(tree_node: ast.Call) -> str:
    func = tree_node.func
    if isinstance(func, ast.Name):
        return func.id
    if isinstance(func, ast.Attribute):
        return func.attr
    return ""


def python_findings(file_text: str, added: set[int]) -> list:
    try:
        tree = ast.parse(file_text)
    except SyntaxError:
        return []
    findings = []
    source_lines = file_text.splitlines()
    for tree_node in ast.walk(tree):
        for identifier, line_number in bound_names(tree_node):
            if line_number in added and short(identifier) and LIBRARY_MARK not in source_lines[line_number - 1]:
                findings.append((line_number, f"`{identifier}` says nothing: name it for what it holds"))
        message_node = tree_node.msg if isinstance(tree_node, ast.Assert) else None
        if message_node is not None or (isinstance(tree_node, ast.Call) and call_name(tree_node) in OUTPUT_CALLS):
            for inner_node in ast.walk(message_node or tree_node):
                if (isinstance(inner_node, ast.Constant) and isinstance(inner_node.value, str) and cyrillic(inner_node.value)
                        and inner_node.lineno in added):
                    findings.append((inner_node.lineno, "output in Russian: what a program prints or logs is in English"))
                    break
    return findings


def go_short(code_text: str) -> list:
    code = code_text.split("//", 1)[0]
    if ":=" not in code:
        return []
    lhs = code.split(":=", 1)[0].strip()
    for lead in ("for ", "if ", "switch ", "select "):
        lhs = lhs.removeprefix(lead)
    if " range" in lhs:
        lhs = lhs.split(" range", 1)[0]
    return [identifier.strip() for identifier in lhs.split(",") if short(identifier.strip()) and identifier.strip().isidentifier()]


def go_code_lines(file_text: str) -> list[tuple[int, str]]:
    code_lines, in_raw_string = [], False
    for number, source_line in enumerate(file_text.splitlines(), 1):
        if source_line.count("`") % 2:
            if not in_raw_string:
                code_lines.append((number, source_line.split("`", 1)[0]))
            in_raw_string = not in_raw_string
        elif not in_raw_string:
            code_lines.append((number, source_line))
    return code_lines


def go_findings(file_text: str, added: set[int]) -> list:
    findings = []
    for number, code_text in go_code_lines(file_text):
        if number not in added or LIBRARY_MARK in code_text:
            continue
        findings += [(number, f"`{identifier}` says nothing: name it for what it holds") for identifier in go_short(code_text)]
        if cyrillic(code_text) and any(call in code_text for call in GO_OUTPUT):
            findings.append((number, "output in Russian: what a program prints or logs is in English"))
    return findings


def whole_files(requested: list[str]) -> list[tuple[str, str, set[int]]]:
    tracked = requested or subprocess.run(["git", "ls-files", "*.py", "*.go"], check=True, capture_output=True,
                                          text=True).stdout.split()
    checked = []
    for path in tracked:
        if path.endswith((".py", ".go")) and Path(path).is_file():
            file_text = Path(path).read_text(encoding="utf-8", errors="replace")
            checked.append((path, file_text, set(range(1, file_text.count("\n") + 2))))
    return checked


def added_lines() -> list[tuple[str, str, set[int]]]:
    merge_base = base()
    return [(path, at_head(path), hunks(merge_base, path)[0])
            for path in changed(merge_base) if path.endswith((".py", ".go"))]


def main() -> int:
    whole = "--all" in sys.argv
    targets = whole_files([arg for arg in sys.argv[1:] if arg != "--all"]) if whole else added_lines()
    finding_count = 0
    for path, file_text, lines_checked in targets:
        if generated(file_text):
            continue
        findings = python_findings(file_text, lines_checked) if path.endswith(".py") else go_findings(file_text, lines_checked)
        for line_number, problem in sorted(findings):
            finding_count += 1
            print(f"{path}:{line_number}: {problem}")
    scope = "every tracked file" if whole else "added lines"
    if finding_count:
        print(f"NAMING RED: {finding_count} finding(s) in {scope}. A name says what it holds; output is in English.")
        return 1
    print(f"NAMING GREEN: every name in {scope} says what it holds and no output is in Russian")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
