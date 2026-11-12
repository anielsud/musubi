//go:build !darwin

package procstat

import "runtime"

// System: only macOS has a process table here so far; elsewhere the
// readout says it isn't supported rather than show partial numbers.
func System() (Source, error) { return nil, ErrUnsupported }

// MachineInfo: cores only; memory is left 0 where it isn't read.
func MachineInfo() Machine { return Machine{Cores: runtime.NumCPU()} }
