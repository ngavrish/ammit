"""Ask the server the two questions a terminal is the right place for.

    ammit compare APF-1934 APF-2531 [--by agent] [--json]
    ammit search "AttributeError" --run APF-1934

Both are the endpoints of the same name, printed. The server does the
arithmetic and the ranking, so what the terminal shows and what a chart draws
cannot disagree - the alternative is a second implementation of "what did this
run cost", which is how two numbers for one idea start.

Also reachable as `python -m ammit` for anyone who has the package but not the
script on their path.
"""

from __future__ import annotations

import json
import sys
import urllib.error
import urllib.parse
import urllib.request

from . import endpoint

USAGE = """usage:
  ammit compare RUN_A RUN_B [--by phase|agent] [--json] [--url URL]
  ammit search QUERY [--run RUN] [--kind KIND] [--limit N] [--json] [--url URL]

RUN is a run id or the ticket it ran for; the newest run of that ticket wins.
"""

_OK = 0
_MISUSE = 2

# compare needs two runs after the verb; search needs one query.
_COMPARE_ARGS = 2
_SEARCH_ARGS = 1


def _flags(argv: list[str]) -> tuple[list[str], dict[str, str]]:
    """Positional arguments and --key value pairs, with --json as a bare flag."""
    positional: list[str] = []
    named: dict[str, str] = {}
    rest = list(argv)
    while rest:
        arg = rest.pop(0)
        if not arg.startswith("--"):
            positional.append(arg)
            continue
        key = arg[2:]
        if key == "json":
            named["json"] = "1"
            continue
        if not rest:
            raise SystemExit(f"--{key} wants a value\n\n{USAGE}")
        named[key] = rest.pop(0)
    return positional, named


def _get(path: str, params: dict[str, str], as_json: bool) -> str:
    """One GET, and a readable failure rather than a traceback.

    The client half of this package never raises at the caller; a command-line
    tool is the one place where being told what went wrong is the point.
    """
    url = f"{endpoint()}{path}?{urllib.parse.urlencode(params)}"
    accept = "application/json" if as_json else "text/plain"
    req = urllib.request.Request(url, headers={"Accept": accept}, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            body = response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace").strip()
        raise SystemExit(f"ammit: {exc.code} from {url}\n{detail}") from exc
    except (urllib.error.URLError, OSError) as exc:
        raise SystemExit(f"ammit: cannot reach {endpoint()} ({exc})") from exc
    if as_json:
        return json.dumps(json.loads(body), indent=2)
    return body


def _compare(argv: list[str]) -> int:
    positional, named = _flags(argv)
    if len(positional) != _COMPARE_ARGS:
        raise SystemExit(USAGE)
    if "url" in named:
        endpoint(named["url"])
    params = {"a": positional[0], "b": positional[1]}
    if named.get("by"):
        params["by"] = named["by"]
    print(_get("/compare", params, "json" in named), end="")
    return _OK


def _search(argv: list[str]) -> int:
    positional, named = _flags(argv)
    if len(positional) != _SEARCH_ARGS:
        raise SystemExit(USAGE)
    if "url" in named:
        endpoint(named["url"])
    params = {"q": positional[0]}
    for key in ("run", "kind", "limit"):
        if named.get(key):
            params[key] = named[key]
    print(_get("/search", params, "json" in named), end="")
    return _OK


def main(argv: list[str] | None = None) -> int:
    args = list(sys.argv[1:] if argv is None else argv)
    if not args or args[0] in ("-h", "--help", "help"):
        print(USAGE, end="")
        return _OK
    verb, rest = args[0], args[1:]
    if verb == "compare":
        return _compare(rest)
    if verb == "search":
        return _search(rest)
    print(f"ammit: no command {verb}\n\n{USAGE}", end="", file=sys.stderr)
    return _MISUSE


if __name__ == "__main__":
    sys.exit(main())
