// Package procstat reads CPU and memory of the processes Musubi is made of
// (the app, its web view, the recorder, Ollama) and of Tomoe, for the
// diagnostics panel's resource readout. It only reads process information
// of the current user; it never signals or changes a process.
package procstat

import (
	"errors"
	"time"
)

// Proc is one process in the table.
type Proc struct {
	PID  int
	PPID int
	// Start tells a process from a later one that reused its pid.
	Start time.Time
	// Path is the executable's path ("" when it can't be read).
	Path string
}

// Usage is a process's cumulative CPU time and its memory now.
type Usage struct {
	// CPU is user + system time since the process started.
	CPU time.Duration
	// RSS is resident memory; it counts mapped file pages (Musubi's
	// memory-mapped database) that the system can drop at any time.
	RSS uint64
	// Footprint is macOS's phys_footprint, what Activity Monitor shows as
	// Memory; 0 where the system has no such measure.
	Footprint uint64
}

// Source is the process table: the real one per platform, a fake in tests.
type Source interface {
	Procs() ([]Proc, error)
	// Args is a process's command line (argv), read only for the few
	// processes whose path alone doesn't say what they are.
	Args(pid int) ([]string, error)
	Usage(pid int) (Usage, error)
	// Responsible is the process macOS holds responsible for pid (an app,
	// for the XPC services it uses, such as WebKit's web content
	// process). ok is false where that can't be known.
	Responsible(pid int) (resp int, ok bool)
}

// Machine is this computer's size, to read the readout against.
type Machine struct {
	Cores  int    `json:"cores"`
	Memory uint64 `json:"memory"`
}

// ErrUnsupported: no process table on this system (yet).
var ErrUnsupported = errors.New("resource readout is not supported on this system")
