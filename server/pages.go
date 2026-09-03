package main

import "encoding/json"

// Every page the charts serve, in one place. A tab, a path, a spec, and
// whether the page is about every run at once (no window, no run: the widest
// range). Adding a page is adding a line here; the header, the routes, the
// scope check and the browser's own list all read this.
type pageDef struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Tab     string `json:"tab"`
	AllTime bool   `json:"allTime"`
	spec    func() chartSpec
}

var pages = []pageDef{
	{Key: "runs", Path: "/ammit/runs", Tab: "Runs", spec: panelsOf},
	{Key: "window", Path: "/ammit/window", Tab: "A window", spec: panelsOf},
	{Key: "lifetime", Path: "/ammit/lifetime", Tab: "All time", AllTime: true, spec: lifetimeOf},
	{Key: "heal", Path: "/ammit/heal", Tab: "Heal", AllTime: true, spec: healOf},
	{Key: "model", Path: "/ammit/model", Tab: "Model", AllTime: true, spec: modelOf},
}

// pageOf is the page with this key, or nil.
func pageOf(key string) *pageDef {
	for i := range pages {
		if pages[i].Key == key {
			return &pages[i]
		}
	}
	return nil
}

// pagesJSON is the registry as the browser reads it.
func pagesJSON() string {
	b, _ := json.Marshal(pages)
	return string(b)
}
