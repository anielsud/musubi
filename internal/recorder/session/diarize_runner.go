package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// DiarizeAttempt is one step of the retry ladder for out-of-process
// diarization.
type DiarizeAttempt struct {
	UseCPU bool
	Label  string // for the log
}

// DefaultDiarizeAttempts tries the GPU twice and then the CPU.
//
// Diarization runs in a child process because sherpa-onnx can crash with
// a segfault inside Process(); in a child that costs only the attempt.
// Such crashes are often a one-off (driver or scheduling state), so a
// fresh process on the same device is worth one more try; a CPU run
// avoids GPU-only runtime bugs altogether, slower but dependable.
var DefaultDiarizeAttempts = []DiarizeAttempt{
	{UseCPU: false, Label: "GPU, try 1 of 3"},
	{UseCPU: false, Label: "GPU, try 2 of 3"},
	{UseCPU: true, Label: "CPU fallback, try 3 of 3"},
}

// diarizeWorkerName is the executable that implements the
// diarize-session subcommand. It is still Tomoe's binary; the recorder
// will provide its own worker later.
const diarizeWorkerName = "tomoe"

// FindDiarizeWorker returns the path of the diarize-session worker, or ""
// if there is none. It prefers the running executable when that is the
// worker, then a worker installed in the same directory (the GUI is
// installed next to the CLI), then the first one on $PATH.
func FindDiarizeWorker() string {
	if self, err := os.Executable(); err == nil {
		if filepath.Base(self) == diarizeWorkerName {
			return self
		}
		next := filepath.Join(filepath.Dir(self), diarizeWorkerName)
		if fi, err := os.Stat(next); err == nil && !fi.IsDir() {
			return next
		}
	}
	if p, err := exec.LookPath(diarizeWorkerName); err == nil {
		return p
	}
	return ""
}

// RunDiarizeWithRetry diarizes session sessID by running
// `<worker> diarize-session <id> [--cpu]` once per attempt until one
// succeeds; nil or empty attempts means DefaultDiarizeAttempts. The
// worker's output goes to this process's stdout and stderr, so it lands
// in the same log.
func RunDiarizeWithRetry(sessID string, attempts []DiarizeAttempt) error {
	worker := FindDiarizeWorker()
	if worker == "" {
		return fmt.Errorf("diarize worker %q not found next to this executable or on $PATH", diarizeWorkerName)
	}
	return diarizeWithWorker(worker, sessID, attempts)
}

// diarizeWithWorker is RunDiarizeWithRetry with the worker given, so
// tests can use a stand-in script.
//
// Any exit of the worker, a crash or a plain failure, moves on to the
// next attempt, since the next one may run on a different device. Only a
// worker that can't be started at all ends the ladder early: every
// attempt would fail the same way.
func diarizeWithWorker(worker, sessID string, attempts []DiarizeAttempt) error {
	if len(attempts) == 0 {
		attempts = DefaultDiarizeAttempts
	}
	var last error
	for _, try := range attempts {
		args := []string{"diarize-session", sessID}
		if try.UseCPU {
			args = append(args, "--cpu")
		}
		fmt.Printf("diarize: session %s: %s\n", sessID, try.Label)
		cmd := exec.Command(worker, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		if err == nil {
			fmt.Printf("diarize: session %s: %s done\n", sessID, try.Label)
			return nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return fmt.Errorf("could not start the diarize worker: %w", err)
		}
		last = err
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			fmt.Printf("diarize: session %s: %s crashed (%v), trying next\n", sessID, try.Label, ws.Signal())
		} else {
			fmt.Printf("diarize: session %s: %s failed with exit code %d, trying next\n", sessID, try.Label, exit.ExitCode())
		}
	}
	return fmt.Errorf("diarize failed on all %d attempts, the last with: %w", len(attempts), last)
}
