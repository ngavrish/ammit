#!/usr/bin/env python3
import ast
import subprocess
import sys


def git(*git_args: str) -> str:
    return subprocess.run(["git", *git_args], check=True, capture_output=True, text=True).stdout


def base() -> str:
    ref = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    return git("merge-base", "HEAD", ref).strip()


def changed(merge_base: str) -> list[str]:
    return git("diff", "--name-only", "--diff-filter=AM", merge_base, "HEAD").splitlines()


def at_head(path: str) -> str:
    return git("show", f"HEAD:{path}")


def hunks(merge_base: str, path: str) -> tuple[set[int], None]:
    added, line_number = set(), 0
    for diff_line in git("diff", "-U0", merge_base, "HEAD", "--", path).splitlines():
        if diff_line.startswith("@@"):
            line_number = int(diff_line.split()[2][1:].split(",")[0])
        elif diff_line.startswith("+") and not diff_line.startswith("+++"):
            added.add(line_number)
            line_number += 1
    return added, None

OUTPUT_CALLS = {"print", "write", "debug", "info", "warning", "warn", "error", "exception", "critical", "log",
                "fatal", "fail", "exit", "echo", "Exit", "SystemExit"}
GO_OUTPUT = ("fmt.Print", "fmt.Fprint", "fmt.Sprint", "fmt.Errorf", "log.", "errors.New", "slog.", "t.Error", "t.Fatal")
MIN_NAME_LENGTH = 3
ALLOWED = {"_", "ok", "id", "db", "io"}
VAGUE = {"got", "out", "res", "ret", "rv", "tmp", "temp", "val", "value", "values", "data", "obj", "item", "items",
         "thing", "things", "stuff", "info", "foo", "bar", "baz", "result", "results", "doc", "raw", "buf", "arr",
         "lst", "dct", "elem", "entry", "entries", "said", "seen", "hit", "rest", "head", "body", "text", "line",
         "row", "rows", "found", "bad", "todo", "src", "var", "args", "kwargs", "params", "opts", "cfg",
         "fh", "ap", "sub", "node", "part", "parts", "key", "name", "acc", "cur", "prev", "nxt"}


def cyrillic(code_text: str) -> bool:
    return any("Ѐ" <= letter <= "ӿ" for letter in code_text)


def short(identifier: str) -> bool:
    bare = identifier.strip("_").lower()
    return identifier not in ALLOWED and (len(bare) < MIN_NAME_LENGTH or bare in VAGUE)


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
    for tree_node in ast.walk(tree):
        for identifier, line_number in bound_names(tree_node):
            if line_number in added and short(identifier):
                findings.append((line_number, f"`{identifier}` says nothing: name it for what it holds"))
        if isinstance(tree_node, ast.Call) and call_name(tree_node) in OUTPUT_CALLS:
            for inner_node in ast.walk(tree_node):
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


def go_findings(file_text: str, added: set[int]) -> list:
    findings = []
    for number, code_text in enumerate(file_text.splitlines(), 1):
        if number not in added:
            continue
        findings += [(number, f"`{identifier}` says nothing: name it for what it holds") for identifier in go_short(code_text)]
        if cyrillic(code_text) and any(call in code_text for call in GO_OUTPUT):
            findings.append((number, "output in Russian: what a program prints or logs is in English"))
    return findings


def main() -> int:
    merge_base = base()
    red = 0
    for path in changed(merge_base):
        if not path.endswith((".py", ".go")):
            continue
        file_text = at_head(path)
        added = hunks(merge_base, path)[0]
        findings = python_findings(file_text, added) if path.endswith(".py") else go_findings(file_text, added)
        for line_number, why in sorted(findings):
            red += 1
            print(f"{path}:{line_number}: {why}")
    if red:
        print(f"NAMING RED: {red} finding(s) on added lines. A name says what it holds; output is in English.")
        return 1
    print("NAMING GREEN: every added name says what it holds and no output is in Russian")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
