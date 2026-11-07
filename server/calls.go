package main

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// What an agent did, reduced to what makes two of them the same thing.
//
// The rule lives here rather than in the client because there is one rule. Two
// pipelines counting repeats their own way produce two numbers for one idea, and
// the whole point of the count is to be able to say "this ran eleven times"
// without anyone having to agree with anyone first.

var (
	// A leading `cd somewhere &&` says where, not what.
	cdPrefix = regexp.MustCompile(`^\s*cd\s+\S+\s*&&\s*`)
	spaces   = regexp.MustCompile(`\s+`)
	// The programs this pipeline ships, so a call to one of ours is not filed
	// as an anonymous shell command.
	ourTool = regexp.MustCompile(`(?:^|[\s|;&])(?:python3?\s+)?(?:/engine/tools/|bash\s+/engine/tools/)([\w.-]+)`)
	// A search, whatever it was spelled with.
	searchCmd = regexp.MustCompile(`(?:^|[|;&]\s*|\s)(grep|rg|ack|find|locate)\b`)
	testCmd   = regexp.MustCompile(`(?:^|[|;&]\s*|\s)(behave|pytest|npm\s+test|run_scenarios)\b`)
	readCmd   = regexp.MustCompile(`^(cat|less|more|wc|head|tail|sed\s+-n)\b`)
	// The first absolute path a command names is usually the thing it is about
	// — except /dev/null, which is where output goes to be ignored and was
	// filed as the thing four searches were about.
	firstPath = regexp.MustCompile(`(/[\w./+-]{2,})`)
	devNull   = regexp.MustCompile(`^/dev/`)
	// `python3 -c "... open('x.json') ..."` is a read of x.json, whatever it is
	// spelled with. Forty-nine of one phase's commands were this shape, pulling
	// rows out of files this pipeline had written itself.
	inlineOpen = regexp.MustCompile(`open\(\s*['"]([^'"]+)['"]`)
	// A shell command that writes a file, three ways the agents actually
	// do it: the engine's own writers take the path after --into
	// (put_rows.py for a run's rows, write_block.py for a feature), a
	// redirect puts stdout into it, tee puts it into it and onto stdout.
	// These were "cli" and "other", so a branch's features were not among
	// its writes: on run bd66a7d8 the ui branch's four feature files were
	// written with write_block.py and the gate, narrowing to what the
	// branch wrote, could not see them - nor the faults in them.
	intoPath = regexp.MustCompile(`--into\s+([^\s<>|;&]+)`)
	redirect = regexp.MustCompile(`(?:^|[^<>0-9&])>{1,2}\s*([^\s<>|;&]+)`)
	teePath  = regexp.MustCompile(`(?:^|[\s|;&])tee\s+(?:-a\s+)?([^\s<>|;&]+)`)
	// A redirect's token is what the shell was handed, not the file it
	// wrote: `cd /runs/X; cat >> a.md` wrote /runs/X/a.md, and
	// `F=a.md; cat >> "$F"` wrote a.md. Run 61eca0ae (APF-3313) had its two
	// writes to a bug draft recorded as `a.md` and as `"$F"`, and the data
	// trace, asking whether a session of this run wrote that draft, was
	// told no, and stayed red over a line the run had written itself.
	leadCd = regexp.MustCompile(`^\s*cd\s+("[^"]*"|'[^']*'|[^\s;&|]+)\s*(?:&&|;)`)
	assign = regexp.MustCompile(`(?:^|[\s;&|(])([A-Za-z_]\w*)=("[^"$]*"|'[^']*'|[^\s;&|<>()'"$]+)`)
	varRef = regexp.MustCompile(`\$\{([A-Za-z_]\w*)\}|\$([A-Za-z_]\w*)`)
)

// resolveWrite names the file a shell write went to, from the whole command:
// the variables it assigned before the write and the directory a leading cd
// moved to. What it cannot work out is left as it was written - a target
// that says `$F` is honest about not knowing, one that guesses is not.
func resolveWrite(cmd, target string) string {
	at := strings.Index(cmd, target)
	if at < 0 {
		at = len(cmd)
	}
	vars := map[string]string{}
	for _, m := range assign.FindAllStringSubmatchIndex(cmd, -1) {
		if m[0] < at {
			vars[cmd[m[2]:m[3]]] = unquote(cmd[m[4]:m[5]])
		}
	}
	known := true
	t := varRef.ReplaceAllStringFunc(unquote(target), func(ref string) string {
		if v, ok := vars[strings.Trim(ref, "${}")]; ok {
			return v
		}
		known = false
		return ref
	})
	if !known || t == "" || strings.ContainsAny(t, "$`*?~") {
		return target
	}
	if !strings.HasPrefix(t, "/") {
		m := leadCd.FindStringSubmatch(cmd)
		if m == nil {
			return t
		}
		dir := unquote(m[1])
		if !strings.HasPrefix(dir, "/") || strings.Contains(dir, "$") {
			return t
		}
		t = path.Join(dir, t)
	}
	return path.Clean(t)
}

// classify turns one tool call into (kind, target, signature).
//
// signature answers "has this exact call run before". target answers the wider
// question — "how many times has anything touched this file" — which is the one
// that found a session reading a single step module twenty times through four
// different tools.
func classify(tool string, input map[string]any) (kind, target, signature string) {
	str := func(k string) string {
		if v, ok := input[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	switch {
	case tool == "Read":
		p := str("file_path")
		window := ""
		if o, ok := input["offset"]; ok {
			b, _ := json.Marshal(o)
			window = "@" + string(b)
		}
		return "read", p, "Read " + p + window
	case tool == "Write" || tool == "Edit" || tool == "NotebookEdit":
		p := str("file_path")
		return "write", p, tool + " " + p
	case tool == "Grep":
		p := str("pattern")
		return "search", p, "Grep " + p + " " + str("path")
	case tool == "Glob":
		return "search", str("pattern"), "Glob " + str("pattern")
	case strings.HasPrefix(tool, "mcp__"):
		b, _ := json.Marshal(input)
		return "map", tool, tool + " " + string(b)
	case tool == "Bash":
		raw := str("command")
		cmd := spaces.ReplaceAllString(cdPrefix.ReplaceAllString(raw, ""), " ")
		kind, target, signature = bashKind(cmd)
		// put_rows and write_block resolve their --into names themselves,
		// against the run directory and not the shell's, so only a
		// redirect or a tee is resolved here.
		if kind == "write" && !ourTool.MatchString(cmd) {
			target = resolveWrite(spaces.ReplaceAllString(raw, " "), target)
		}
		return kind, target, signature
	}
	b, _ := json.Marshal(input)
	return "other", tool, tool + " " + string(b)
}

// bashKind reads a shell command for what it is doing, in the order that
// matters: a pipeline that greps and then heads is a search, not a read.
func bashKind(cmd string) (kind, target, signature string) {
	sig := cmd
	if len(sig) > 400 {
		sig = sig[:400]
	}
	if m := intoPath.FindStringSubmatch(cmd); m != nil && ourTool.MatchString(cmd) {
		return "write", m[1], sig
	}
	if m := ourTool.FindStringSubmatch(cmd); m != nil {
		return "cli", m[1], sig
	}
	if m := redirect.FindStringSubmatch(cmd); m != nil && !devNull.MatchString(m[1]) {
		return "write", m[1], sig
	}
	if m := teePath.FindStringSubmatch(cmd); m != nil && !devNull.MatchString(m[1]) {
		return "write", m[1], sig
	}
	if strings.Contains(cmd, "where-are-we") {
		return "map", "where-are-we", sig
	}
	if testCmd.MatchString(cmd) {
		return "test", firstOf(cmd, "tests"), sig
	}
	if searchCmd.MatchString(cmd) {
		return "search", firstOf(cmd, "repository"), sig
	}
	if readCmd.MatchString(cmd) {
		return "read", firstOf(cmd, ""), sig
	}
	if m := inlineOpen.FindStringSubmatch(cmd); m != nil {
		return "read", m[1], sig
	}
	head := cmd
	if i := strings.IndexAny(head, " \t"); i > 0 {
		head = head[:i]
	}
	return "other", path.Base(head), sig
}

func firstOf(cmd, fallback string) string {
	for _, m := range firstPath.FindAllStringSubmatch(cmd, 8) {
		if !devNull.MatchString(m[1]) {
			return m[1]
		}
	}
	return fallback
}
