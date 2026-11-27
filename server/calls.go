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
)

// classify turns one tool call into (kind, target, signature).
//
// signature answers "has this exact call run before". target answers the wider
// question — "how many times has anything touched this file" — which is the one
// that found a session reading a single step module twenty times through four
// different tools.
func classify(tool string, input map[string]any) (kind, target, signature string) {
	str := func(k string) string {
		if inputText, ok := input[k].(string); ok {
			return strings.TrimSpace(inputText)
		}
		return ""
	}
	switch {
	case tool == "Read":
		filePath := str("file_path")
		window := ""
		if offset, ok := input["offset"]; ok {
			offsetJSON, _ := json.Marshal(offset)
			window = "@" + string(offsetJSON)
		}
		return "read", filePath, "Read " + filePath + window
	case tool == "Write" || tool == "Edit" || tool == "NotebookEdit":
		filePath := str("file_path")
		return "write", filePath, tool + " " + filePath
	case tool == "Grep":
		pattern := str("pattern")
		return "search", pattern, "Grep " + pattern + " " + str("path")
	case tool == "Glob":
		return "search", str("pattern"), "Glob " + str("pattern")
	case strings.HasPrefix(tool, "mcp__"):
		inputJSON, _ := json.Marshal(input)
		return "map", tool, tool + " " + string(inputJSON)
	case tool == "Bash":
		cmd := spaces.ReplaceAllString(cdPrefix.ReplaceAllString(str("command"), ""), " ")
		return bashKind(cmd)
	}
	inputJSON, _ := json.Marshal(input)
	return "other", tool, tool + " " + string(inputJSON)
}

// bashKind reads a shell command for what it is doing, in the order that
// matters: a pipeline that greps and then heads is a search, not a read.
func bashKind(cmd string) (kind, target, signature string) {
	sig := cmd
	if len(sig) > 400 {
		sig = sig[:400]
	}
	if match := intoPath.FindStringSubmatch(cmd); match != nil && ourTool.MatchString(cmd) {
		return "write", match[1], sig
	}
	if match := ourTool.FindStringSubmatch(cmd); match != nil {
		return "cli", match[1], sig
	}
	if match := redirect.FindStringSubmatch(cmd); match != nil && !devNull.MatchString(match[1]) {
		return "write", match[1], sig
	}
	if match := teePath.FindStringSubmatch(cmd); match != nil && !devNull.MatchString(match[1]) {
		return "write", match[1], sig
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
	if match := inlineOpen.FindStringSubmatch(cmd); match != nil {
		return "read", match[1], sig
	}
	program := cmd
	if space := strings.IndexAny(program, " \t"); space > 0 {
		program = program[:space]
	}
	return "other", path.Base(program), sig
}

func firstOf(cmd, fallback string) string {
	for _, match := range firstPath.FindAllStringSubmatch(cmd, 8) {
		if !devNull.MatchString(match[1]) {
			return match[1]
		}
	}
	return fallback
}
