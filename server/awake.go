package main

import (
	"sync"
	"time"
)

// How long this process has been watching, asked at the moment an age is
// measured rather than at the top of the round that measures it.
//
// The suspend guard in main.go looks at the gap between rounds, so it sees a
// sleep that falls between two rounds and nothing else. Run df4cd16a
// (APF-3122) died in the hole that leaves. The host slept most of a night and
// woke for twenty seconds at a time; on one of those wakes the guard fired and
// re-armed _upSince, the next round passed the guard on time, and the VM froze
// again inside that round, before weigh read the pulse. Seventeen minutes
// later it thawed mid-round: weigh read a pulse 1013s old, an uptime of 1032s
// that it had slept through all but twenty of, and sent stop_run to a run - and
// in the same second to run 4e57bd70 at 1029s - whose processes had been asleep
// with everything else. ammit's own 15-second sampler has the same 1035s hole
// in it, at 1792822502 and 1792823537.
//
// So the absence is detected where it bites. _seen is the last moment this
// process is known to have been running; keepAwake moves it every second, and
// awake() - called after a pulse is read, not before - treats a gap in it
// longer than absence as our own sleep, exactly as the round guard and a
// restart do: nothing older than it is ours to judge.
var (
	awakeMu sync.Mutex
	_seen   = nowWall()
	// absence is the gap in our own running that counts as having been away.
	// main sets it to the round guard's own measure, three ticks.
	absence = 60.0
)

// awake is how many seconds this process has watched without a break, as of
// now. A gap since it last ran longer than absence re-arms the clock first.
func awake() float64 {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	now := nowWall()
	if now-_seen > absence {
		_upSince = now
	}
	_seen = now
	return now - _upSince
}

// rearm records an absence noticed somewhere else - the round guard in main.
func rearm() {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	_upSince = nowWall()
	_seen = _upSince
}

// upSince reads the absence clock under its lock.
func upSince() float64 {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	return _upSince
}

// keepAwake moves _seen while this process runs, so that a gap in it is a
// gap in us. Its own goroutine: a round that is slow for a reason of its own
// (a query, a daemon) is not a sleep, and must not read as one.
func keepAwake() {
	for {
		awake()
		time.Sleep(time.Second)
	}
}
