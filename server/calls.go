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
)

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
		cmd := spaces.ReplaceAllString(cdPrefix.ReplaceAllString(str("command"), ""), " ")
		return bashKind(cmd)
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
	if m := ourTool.FindStringSubmatch(cmd); m != nil {
		return "cli", m[1], sig
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
