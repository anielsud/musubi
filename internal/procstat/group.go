package procstat

import (
	"path/filepath"
	"slices"
	"strings"
)

// Component keys, in the order the readout shows them.
const (
	KeyMusubi   = "musubi"
	KeyWebView  = "webview"
	KeyRecorder = "recorder"
	KeyOllama   = "ollama"
	KeyTomoe    = "tomoe"
)

var names = map[string]string{
	KeyMusubi:   "Musubi",
	KeyWebView:  "Musubi web view",
	KeyRecorder: "Recorder",
	KeyOllama:   "Local models (Ollama)",
	KeyTomoe:    "Tomoe (not used by Musubi)",
}

var order = []string{KeyMusubi, KeyWebView, KeyRecorder, KeyOllama, KeyTomoe}

// recorderApp is the installed recorder's executable.
const recorderApp = "Musubi Recorder.app/Contents/MacOS/musubi-recorder"

// Member is one process of a component.
type Member struct {
	PID  int
	Name string
	Proc Proc
}

// Group is a component's processes: its root(s) first, then their
// descendants.
type Group struct {
	Key     string
	Name    string
	Members []Member
}

// Find groups the process table into components. self is this app's pid;
// recorderPID is the pid the recorder reports on its control socket (0
// when not connected or from an older recorder). A process belongs to at
// most one component, so a recorder or model server launched by Musubi
// isn't counted twice. Components with no process are left out.
func Find(procs []Proc, src Source, self, recorderPID int) []Group {
	byPID := make(map[int]Proc, len(procs))
	kids := map[int][]int{}
	for _, p := range procs {
		byPID[p.PID] = p
		kids[p.PPID] = append(kids[p.PPID], p.PID)
	}
	for _, k := range kids {
		slices.Sort(k)
	}
	args := func(pid int) []string {
		a, _ := src.Args(pid)
		return a
	}
	claimed := map[int]bool{}
	groups := map[string]*Group{}
	// take adds roots and their descendants not yet claimed.
	take := func(key string, roots []int) {
		g := &Group{Key: key, Name: names[key]}
		var walk func(pid int)
		walk = func(pid int) {
			if claimed[pid] {
				return
			}
			claimed[pid] = true
			p := byPID[pid]
			g.Members = append(g.Members, Member{PID: pid, Name: procName(p, args), Proc: p})
			for _, c := range kids[pid] {
				walk(c)
			}
		}
		for _, r := range roots {
			if _, ok := byPID[r]; ok {
				walk(r)
			}
		}
		if len(g.Members) > 0 {
			groups[key] = g
		}
	}

	// Claim order is by certainty, not display: the recorder and Ollama
	// before Musubi's own descendants.
	take(KeyRecorder, recorderRoots(procs, byPID, recorderPID, args))
	take(KeyOllama, rootsOf(procs, byPID, func(p Proc) bool {
		if base(p.Path) != "ollama" {
			return false
		}
		a := args(p.PID)
		return len(a) > 1 && a[1] == "serve"
	}))
	var web []int
	for _, p := range procs {
		// The web content process is an XPC service whose parent is
		// launchd, so only the responsible pid ties it to this app;
		// without that API there is no reliable tie and no row.
		if base(p.Path) == "com.apple.WebKit.WebContent" {
			if r, ok := src.Responsible(p.PID); ok && r == self && p.PID != self {
				web = append(web, p.PID)
			}
		}
	}
	take(KeyWebView, web)
	take(KeyTomoe, rootsOf(procs, byPID, func(p Proc) bool { return base(p.Path) == "tomoe-gui" }))
	take(KeyMusubi, []int{self})

	var out []Group
	for _, k := range order {
		if g := groups[k]; g != nil {
			out = append(out, *g)
		}
	}
	return out
}

// recorderRoots prefers the pid the recorder reported; without it, the
// installed app's executable or a `musubi-recorder run` (a dev build).
func recorderRoots(procs []Proc, byPID map[int]Proc, reported int, args func(int) []string) []int {
	if p, ok := byPID[reported]; ok && reported > 0 && base(p.Path) == "musubi-recorder" {
		return []int{reported}
	}
	return rootsOf(procs, byPID, func(p Proc) bool {
		if strings.HasSuffix(p.Path, "/"+recorderApp) {
			return true
		}
		if base(p.Path) != "musubi-recorder" {
			return false
		}
		a := args(p.PID)
		return len(a) > 1 && a[1] == "run"
	})
}

// rootsOf: matching processes whose parent doesn't match too, so a
// matching child is counted under its parent, not as a second root.
func rootsOf(procs []Proc, byPID map[int]Proc, match func(Proc) bool) []int {
	hit := map[int]bool{}
	for _, p := range procs {
		if match(p) {
			hit[p.PID] = true
		}
	}
	var roots []int
	for _, p := range procs {
		if hit[p.PID] && !hit[p.PPID] {
			roots = append(roots, p.PID)
		}
	}
	slices.Sort(roots)
	return roots
}

// procName is the executable's name, with the subcommand for the two
// programs that run as several processes of the same executable.
func procName(p Proc, args func(int) []string) string {
	b := base(p.Path)
	if b == "" {
		return "process"
	}
	switch b {
	case "musubi-recorder", "ollama":
		if a := args(p.PID); len(a) > 1 && !strings.HasPrefix(a[1], "-") {
			return b + " " + a[1]
		}
	case "com.apple.WebKit.WebContent":
		return "WebKit web content"
	}
	return b
}

func base(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}
