package main

import (
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// The config as it stood: limits.yml read on every tick, and the hands-off switch.

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i], line[i:]
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
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func loadConfig(path string) Config {
	conf := Config{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return conf
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line, _ = cutComment(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			conf[section] = map[string]string{}
			continue
		}
		if section == "" {
			continue
		}
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		conf[section][strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return conf
}

func (c Config) num(section, key string) (float64, bool) {
	v, ok := c[section][key]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f == 0 {
		return 0, false // zero means no limit, and saying zero is a decision
	}
	return f, true
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
	if v, ok := c[section][key]; ok && v != "" {
		return v
	}
	return fallback
}
