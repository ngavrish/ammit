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
    unread = list(argv)
    while unread:
        arg = unread.pop(0)
        if not arg.startswith("--"):
            positional.append(arg)
            continue
        flag = arg[2:]
        if flag == "json":
            named["json"] = "1"
            continue
        if not unread:
            raise SystemExit(f"--{flag} wants a value\n\n{USAGE}")
        named[flag] = unread.pop(0)
    return positional, named


def _get(path: str, query: dict[str, str], as_json: bool) -> str:
    """One GET, and a readable failure rather than a traceback.

    The client half of this package never raises at the caller; a command-line
    tool is the one place where being told what went wrong is the point.
    """
    url = f"{endpoint()}{path}?{urllib.parse.urlencode(query)}"
    accept = "application/json" if as_json else "text/plain"
    req = urllib.request.Request(url, headers={"Accept": accept}, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            reply = response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace").strip()
        raise SystemExit(f"ammit: {exc.code} from {url}\n{detail}") from exc
    except (urllib.error.URLError, OSError) as exc:
        raise SystemExit(f"ammit: cannot reach {endpoint()} ({exc})") from exc
    if as_json:
        return json.dumps(json.loads(reply), indent=2)
    return reply


def _compare(argv: list[str]) -> int:
    positional, named = _flags(argv)
    if len(positional) != _COMPARE_ARGS:
        raise SystemExit(USAGE)
    if "url" in named:
        endpoint(named["url"])
    query = {"a": positional[0], "b": positional[1]}
    if named.get("by"):
        query["by"] = named["by"]
    print(_get("/compare", query, "json" in named), end="")
    return _OK


def _search(argv: list[str]) -> int:
    positional, named = _flags(argv)
    if len(positional) != _SEARCH_ARGS:
        raise SystemExit(USAGE)
    if "url" in named:
        endpoint(named["url"])
    query = {"q": positional[0]}
    for filter_name in ("run", "kind", "limit"):
        if named.get(filter_name):
            query[filter_name] = named[filter_name]
    print(_get("/search", query, "json" in named), end="")
    return _OK


def main(argv: list[str] | None = None) -> int:
    words = list(sys.argv[1:] if argv is None else argv)
    if not words or words[0] in ("-h", "--help", "help"):
        print(USAGE, end="")
        return _OK
    verb, verb_args = words[0], words[1:]
    if verb == "compare":
        return _compare(verb_args)
    if verb == "search":
        return _search(verb_args)
    print(f"ammit: no command {verb}\n\n{USAGE}", end="", file=sys.stderr)
    return _MISUSE


if __name__ == "__main__":
    sys.exit(main())
