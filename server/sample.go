package main

import (
	"context"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// What the pipeline is costing in machine, taken from outside it.
//
// A run that swells to eight gigabytes and gets killed by the kernel leaves the
// same evidence as a run that hung: nothing. The process that could have said so
// is the one that died. So the memory is read from out here, on the same tick as
// everything else, and it lands in the same table as the turns and the spend —
// which is what lets a chart put "this phase" and "four gigabytes" on one axis.
//
// The container client is in the image already, because the actions need one.
// Reading the numbers through it rather than through the Docker API keeps this
// file short and keeps the service honest about its one dependency.

type sample struct {
	container string
	memoryMB  float64
	memoryPct float64
	cpuPct    float64
	pids      int
}

// sampleMachine asks the container client for one reading of everything running.
//
// Bounded, because this is a call out to a daemon that has its own bad days.
// Unbounded it took minutes whenever images were being built, and it took the
// whole judging loop with it: no readings, no verdicts, no timeouts enforced —
// the watchdog wedged by exactly the kind of call it exists to catch in others.
// A reading that is late is worth nothing anyway; the next tick will take one.
func sampleMachine(match string) []sample {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format",
		"{{.Name}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.CPUPerc}}\t{{.PIDs}}").Output()
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("ammit: the container client did not answer in 10s; no reading this tick")
		}
		return nil
	}
	var taken []sample
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		cols := strings.Split(line, "\t")
		if len(cols) < 5 {
			continue
		}
		name := strings.TrimSpace(cols[0])
		if match != "" && !strings.Contains(name, match) {
			continue
		}
		used, _, _ := strings.Cut(cols[1], "/")
		pids, _ := strconv.Atoi(strings.TrimSpace(cols[4]))
		taken = append(taken, sample{
			container: name,
			memoryMB:  megabytes(used),
			memoryPct: percent(cols[2]),
			cpuPct:    percent(cols[3]),
			pids:      pids,
		})
	}
	return taken
}

// megabytes reads "1.53GiB", "512MiB", "980kB" the way the client prints them.
func megabytes(text string) float64 {
	text = strings.TrimSpace(text)
	cut := strings.IndexFunc(text, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	if cut <= 0 {
		return 0
	}
	number, err := strconv.ParseFloat(text[:cut], 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(text[cut:])) {
	case "b":
		return number / (1024 * 1024)
	case "kb", "kib":
		return number / 1024
	case "mb", "mib":
		return number
	case "gb", "gib":
		return number * 1024
	case "tb", "tib":
		return number * 1024 * 1024
	}
	return 0
}

func percent(text string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(text), "%"), 64)
	if err != nil {
		return 0
	}
	return value
}

var lastSample float64

// keepSamples records one reading, no more often than the config asks for, and
// judges it against limits.memory_mb. The reading is not attributed to a run:
// several branches share a worker, and a number that claims to know which of
// them ate the memory would be a number making something up. The charts join a
// sample to whatever phase and session were open at that moment, which is a
// claim about time and can be checked.
func keepSamples(conf Config) {
	every, ok := conf.num("sample", "every")
	if !ok {
		every = 60
	}
	if every <= 0 {
		return
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if now-lastSample < every {
		return
	}
	lastSample = now

	limit, hasLimit := conf.num("limits", "memory_mb")
	for _, s := range sampleMachine(conf.str("sample", "containers", "")) {
		store(event{
			"kind": "sample", "at": now, "container": s.container,
			"memory_mb": s.memoryMB, "memory_pct": s.memoryPct,
			"cpu_pct": s.cpuPct, "pids": s.pids,
		})
		if !hasLimit || s.memoryMB <= limit ||
			recently("limits.memory_mb", s.container, 600) {
			continue
		}
		action := conf.str("actions", "on_memory", "warn")
		ctx := map[string]string{"container": s.container, "worker": s.container}
		for key, value := range conf["context"] {
			if _, taken := ctx[key]; !taken {
				ctx[key] = value
			}
		}
		judge("container", "", s.container, "limits.memory_mb", limit, s.memoryMB,
			action, act(action, conf, ctx))
	}
}

func init() {
	if _, err := exec.LookPath("docker"); err != nil {
		log.Printf("ammit: no container client on PATH — memory and cpu will be empty")
	}
}
