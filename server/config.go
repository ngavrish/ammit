package main

import (
	"os"
	"strconv"
	"strings"
)

// The config as it stood: limits.yml read on every tick, and the hands-off switch.

func env(key, fallback string) string {
	if envValue := os.Getenv(key); envValue != "" {
		return envValue
	}
	return fallback
}

// Config is two levels of key/value, read fresh on every tick.
//
// Fresh rather than cached because a limit gets changed while a run is going,
// which is exactly when somebody wants to change one. Two levels because that is
// all the file needs, and a yaml dependency in the one service whose value is
// having fewer moving parts than what it watches would be a poor trade.
type Config map[string]map[string]string

// cutComment splits a line into what it says and the comment after it. A '#'
// only starts a comment at the start of a line or after a space: a command is
// allowed to contain one, and a command that loses half of itself to a comment
// marker is a limit that cannot be enforced.
func cutComment(line string) (string, string) {
	for pos := 0; pos < len(line); pos++ {
		if line[pos] != '#' {
			continue
		}
		if pos == 0 || line[pos-1] == ' ' || line[pos-1] == '\t' {
			return line[:pos], line[pos:]
		}
	}
	return line, ""
}

// unquote takes the quotes off a value that is wrapped in them and leaves every
// other quote where it was. `sh -c "rm -f x"` keeps its own pair: stripped from
// one end only, the command the service runs to stop a run is a syntax error,
// and the run it was meant to stop keeps going.
func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if first == '"' && last == '"' {
			return value[1 : len(value)-1]
		}
		// Inside single quotes YAML writes a quote as two. Left doubled,
		// start_run's `-d ''{payload}''` reached the shell as two empty
		// strings around a bare payload, and the shell ate the JSON's own
		// quotes: the orchestrator was sent {key:APF-1934}.
		if first == '\'' && last == '\'' {
			return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
	}
	return value
}

// anchored reads YAML's `&name value` and `*name`, which the line reader would
// otherwise store verbatim.
//
// Not a general YAML implementation, and here for one reason: three limits were
// written as an anchor and an alias, and every one of them had been dead since
// the day it was written. `phase_min.gatefix: &repair_floor 10` was kept as the
// string "&repair_floor 10", `phase_min.implrulefix: *repair_floor` as
// "*repair_floor", neither parses as a number, and num() answers "no such
// limit" to a value that is right there in the file - so all three repair
// phases were judged against the general 60-second floor for weeks, and run
// 558879e7 was ended by it after a repair phase did its work in 26 seconds.
//
// A file that is edited by people will be written in the language it is named
// after. Either the reader speaks that much of it or the file stops being YAML;
// this is the cheaper half.
func anchored(value string, anchors map[string]string) string {
	if strings.HasPrefix(value, "&") {
		anchor, anchoredValue, hasValue := strings.Cut(value[1:], " ")
		if !hasValue {
			return value
		}
		anchoredValue = strings.TrimSpace(anchoredValue)
		anchors[anchor] = anchoredValue
		return anchoredValue
	}
	if strings.HasPrefix(value, "*") {
		if anchoredValue, ok := anchors[strings.TrimSpace(value[1:])]; ok {
			return anchoredValue
		}
	}
	return value
}

func loadConfig(path string) Config {
	limitsYAML, err := os.ReadFile(path)
	if err != nil {
		return Config{}
	}
	return parseLimits(string(limitsYAML))
}

// indentOf is how far a line is indented, a tab counting as one.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// parseLimits reads the two levels of limits.yml, with the three pieces of
// YAML the file is actually written in: quotes, anchors, and block scalars.
//
// A block scalar - `notify: >-` and the lines indented under it - was read as
// the value ">-" and a run of junk keys made of the lines below. notify, warn
// and cooldown were written that way, so each of them ran `sh -c ">-"`, which
// makes an empty file named "-" and exits 0: every alert this service has
// "sent" since was the word done in a judgement row, and nothing else.
func parseLimits(raw string) Config {
	conf := Config{}
	section := ""
	anchors := map[string]string{}
	lines := strings.Split(raw, "\n")
	for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
		code, _ := cutComment(lines[lineIndex])
		if strings.TrimSpace(code) == "" {
			continue
		}
		if !strings.HasPrefix(code, " ") && !strings.HasPrefix(code, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(code), ":"))
			conf[section] = map[string]string{}
			continue
		}
		if section == "" {
			continue
		}
		field, setting, hasColon := strings.Cut(strings.TrimSpace(code), ":")
		if !hasColon {
			continue
		}
		setting = strings.TrimSpace(setting)
		switch setting {
		case ">", ">-", "|", "|-":
			// Every following line indented deeper than the key is the
			// value: folded into one line by `>`, kept as lines by `|`. The
			// block's lines are taken whole, comment marks and all - inside a
			// block a # is text.
			own := indentOf(code)
			var body []string
			for lineIndex+1 < len(lines) {
				next := lines[lineIndex+1]
				if strings.TrimSpace(next) != "" && indentOf(next) <= own {
					break
				}
				body = append(body, strings.TrimSpace(next))
				lineIndex++
			}
			for len(body) > 0 && body[len(body)-1] == "" {
				body = body[:len(body)-1]
			}
			sep := " "
			if setting[0] == '|' {
				sep = "\n"
			}
			conf[section][strings.TrimSpace(field)] = strings.Join(body, sep)
			continue
		}
		conf[section][strings.TrimSpace(field)] = anchored(unquote(setting), anchors)
	}
	return conf
}

func (c Config) num(section, key string) (float64, bool) {
	setting, ok := c[section][key]
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseFloat(setting, 64)
	if err != nil || number == 0 {
		return 0, false // zero means no limit, and saying zero is a decision
	}
	return number, true
}

// numFor is num with a scope tried first: `run.adhoc` before `run`.
//
// One number for every run was right while every run was the same shape. It
// stopped being right the day a second shape arrived: an ad-hoc question is one
// phase and one session, and the four hours the conveyor is given is not a
// limit on it at all — it would have to hang for most of a working day before
// anything here had an opinion.
//
// Scoping rather than a second config: the limit keeps its name, keeps its row
// in the record, and keeps being edited in the one place limits are edited. A
// scope nobody set falls back to the unscoped number, so every limit that has
// not been given a per-mode value behaves exactly as it did.
func (c Config) numFor(section, key, scope string) (float64, bool) {
	if scope != "" {
		if scoped, ok := c.num(section, key+"."+scope); ok {
			return scoped, true
		}
	}
	return c.num(section, key)
}

// handsOff is actions.enforce read as a switch: anything but on, yes, true
// or 1 means the watchdog watches and writes and touches nothing.
func handsOff(c Config) bool {
	switch strings.ToLower(c.str("actions", "enforce", "on")) {
	case "on", "yes", "true", "1":
		return false
	}
	return true
}

func (c Config) str(section, key, fallback string) string {
	if setting, ok := c[section][key]; ok && setting != "" {
		return setting
	}
	return fallback
}
