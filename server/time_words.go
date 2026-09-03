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
	return nil
}
