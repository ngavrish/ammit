#!/usr/bin/env python3
import ast
import subprocess
import sys


def git(*args: str) -> str:
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def base() -> str:
    ref = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    return git("merge-base", "HEAD", ref).strip()


def changed(merge_base: str) -> list[str]:
    return git("diff", "--name-only", "--diff-filter=AM", merge_base, "HEAD").splitlines()


def at_head(path: str) -> str:
    return git("show", f"HEAD:{path}")


def hunks(merge_base: str, path: str) -> tuple[set[int], None]:
    added, line = set(), 0
    for row in git("diff", "-U0", merge_base, "HEAD", "--", path).splitlines():
        if row.startswith("@@"):
            line = int(row.split()[2][1:].split(",")[0])
        elif row.startswith("+") and not row.startswith("+++"):
            added.add(line)
            line += 1
    return added, None

OUTPUT_CALLS = {"print", "write", "debug", "info", "warning", "warn", "error", "exception", "critical", "log",
                "fatal", "fail", "exit", "echo", "Exit", "SystemExit"}
GO_OUTPUT = ("fmt.Print", "fmt.Fprint", "fmt.Sprint", "fmt.Errorf", "log.", "errors.New", "slog.", "t.Error", "t.Fatal")
ALLOWED = {"_"}


def cyrillic(text: str) -> bool:
    return any("Ѐ" <= ch <= "ӿ" for ch in text)


def short(name: str) -> bool:
    return len(name) == 1 and name not in ALLOWED


def bound_names(node: ast.AST) -> list:
    if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Store):
        return [(node.id, node.lineno)]
    if isinstance(node, ast.arg):
        return [(node.arg, node.lineno)]
    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
        return [(node.name, node.lineno)]
    if isinstance(node, ast.ExceptHandler) and node.name:
        return [(node.name, node.lineno)]
    if isinstance(node, ast.alias):
        return [((node.asname or node.name).split(".")[0], node.lineno)]
    return []


def call_name(node: ast.Call) -> str:
    func = node.func
    if isinstance(func, ast.Name):
        return func.id
    if isinstance(func, ast.Attribute):
        return func.attr
    return ""


def python_findings(src: str, added: set[int]) -> list:
    try:
        tree = ast.parse(src)
    except SyntaxError:
        return []
    out = []
    for node in ast.walk(tree):
        for name, line in bound_names(node):
            if line in added and short(name):
                out.append((line, f"one-letter name `{name}`: name it for what it holds"))
        if isinstance(node, ast.Call) and call_name(node) in OUTPUT_CALLS:
            for sub in ast.walk(node):
                if (isinstance(sub, ast.Constant) and isinstance(sub.value, str) and cyrillic(sub.value)
                        and sub.lineno in added):
                    out.append((sub.lineno, "output in Russian: what a program prints or logs is in English"))
                    break
    return out


def go_short(text: str) -> list:
    code = text.split("//", 1)[0]
    if ":=" not in code:
        return []
    lhs = code.split(":=", 1)[0].strip()
    for lead in ("for ", "if ", "switch ", "select "):
        lhs = lhs.removeprefix(lead)
    if " range" in lhs:
        lhs = lhs.split(" range", 1)[0]
    return [name.strip() for name in lhs.split(",") if short(name.strip()) and name.strip().isidentifier()]


def go_findings(src: str, added: set[int]) -> list:
    out = []
    for number, text in enumerate(src.splitlines(), 1):
        if number not in added:
            continue
        out += [(number, f"one-letter name `{name}`: name it for what it holds") for name in go_short(text)]
        if cyrillic(text) and any(call in text for call in GO_OUTPUT):
            out.append((number, "output in Russian: what a program prints or logs is in English"))
    return out


def main() -> int:
    merge_base = base()
    red = 0
    for path in changed(merge_base):
        if not path.endswith((".py", ".go")):
            continue
        src = at_head(path)
        added = hunks(merge_base, path)[0]
        found = python_findings(src, added) if path.endswith(".py") else go_findings(src, added)
        for line, why in sorted(found):
            red += 1
            print(f"{path}:{line}: {why}")
    if red:
        print(f"NAMING RED: {red} finding(s) on added lines. A name says what it holds; output is in English.")
        return 1
    print("NAMING GREEN: no one-letter name and no Russian output on added lines")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
