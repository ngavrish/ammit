package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The words a span of time is written in, wherever the page writes one: on
// an axis, in a legend, under the pointer. Whole words, never letters - "27h
// 47m" on an axis is a code, and the chart is read by people who did not
// write it. The page's formatter builds every label from this map and from
// nothing else, so there is one place to look and one place to test.
var timeWords = map[string]string{
	"millisecond": "millisecond", "milliseconds": "milliseconds",
	"second": "second", "seconds": "seconds",
	"minute": "minute", "minutes": "minutes",
	"hour": "hour", "hours": "hours",
	"day": "day", "days": "days",
}

func timeWordsJS() string {
	b, _ := json.Marshal(timeWords)
	return string(b)
}

// abbreviations are what an axis must never say for a unit of time.
var abbreviations = regexp.MustCompile(`"\s?(h|m|s|d|hr|hrs|min|mins|sec|secs)"`)

// axisTimeIsSpelledOut is the gate behind the rule that a span of time on this
// page is written in whole words. It reads the page as served, finds the one
// formatter every time label goes through, and refuses if that formatter
// carries a unit as a bare letter or a clipped word - or does not draw its
// words from timeWords at all. The words themselves are checked too: a map
// entry that is an abbreviation would pass the formatter and fail the reader.
func axisTimeIsSpelledOut(page string) error {
	for k, w := range timeWords {
		if len(w) < 3 || abbreviations.MatchString(`"`+w+`"`) {
			return fmt.Errorf("timeWords[%q] = %q is an abbreviation", k, w)
		}
	}
	start := strings.Index(page, "function dur(")
	if start < 0 {
		return fmt.Errorf("the page has no dur() formatter")
	}
	end := strings.Index(page[start:], "\n}")
	if end < 0 {
		return fmt.Errorf("dur() does not end")
	}
	body := page[start : start+end]
	if !strings.Contains(body, "TIME_WORDS") {
		return fmt.Errorf("dur() does not take its words from TIME_WORDS")
	}
	if m := abbreviations.FindString(body); m != "" {
		return fmt.Errorf("dur() writes a unit of time as %s", m)
	}
	// And then the rest of the page, because the rule's own sentence is that a
	// label built anywhere else is a label the gate cannot see - and one was.
	// `ago()` returned h+"h "+m+"m" beside the correct formatter for as long
	// as both existed: the gate read dur() alone, found it clean, and said the
	// page spelled its units out while the runs table wrote "27h 47m".
	rest := page[:start] + page[start+end:]
	for _, m := range abbreviations.FindAllStringIndex(rest, -1) {
		line := nearby(rest, m[0])
		// A one-letter literal is only a unit when it is glued onto a number
		// and the line is doing time arithmetic. Without both, the gate reds
		// on `+"s"` pluralising a word, which is what it did on its first run
		// over the whole page - and a gate that cries wolf on the page's
		// English is one somebody narrows back to dur() within the week.
		if !glued.MatchString(line) || !clockwork.MatchString(line) {
			continue
		}
		return fmt.Errorf("a time label outside dur() writes a unit as %s: %s",
			rest[m[0]:m[1]], line)
	}
	return nil
}

// glued: the literal is concatenated onto something, which is how a unit is
// written; clockwork: the line computes a span, which is what makes the letter
// a unit rather than a plural.
var glued = regexp.MustCompile(`\+\s*"\s?(h|m|s|d|hr|hrs|min|mins|sec|secs)"`)
var clockwork = regexp.MustCompile(
	`/\s*(?:60|1000|3600|86400)|Math\.(?:floor|round)|%\s*60|` +
		`\b(?:sec|secs|seconds?|min|mins|minutes?|hours?|days?|elapsed|` +
		`duration|took|age|ago)\b`)

// nearby is the line the offending literal sits on, so the message says where
// to look rather than what to grep for.
func nearby(text string, at int) string {
	from := strings.LastIndex(text[:at], "\n") + 1
	to := strings.Index(text[at:], "\n")
	if to < 0 {
		to = len(text) - at
	}
	return strings.TrimSpace(text[from : at+to])
}
