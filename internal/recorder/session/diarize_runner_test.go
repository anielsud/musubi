package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubWorker writes a shell script standing in for the diarize worker.
// Each run appends its arguments as one line to the returned log file, then
// runs script ($3 is "--cpu" on a CPU attempt).
func stubWorker(t *testing.T, script string) (bin, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "worker.sh")
	log = filepath.Join(dir, "runs")
	body := "#!/bin/sh\necho \"$@\" >> '" + log + "'\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func runs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestDefaultLadderIsGPUGPUCPU(t *testing.T) {
	var got []bool
	for _, a := range DefaultDiarizeAttempts {
		got = append(got, a.UseCPU)
		if a.Label == "" {
			t.Error("attempt without a label")
		}
	}
	if len(got) != 3 || got[0] || got[1] || !got[2] {
		t.Errorf("UseCPU per attempt = %v, want false false true", got)
	}
}

func TestDiarizeWorkerFirstTrySucceeds(t *testing.T) {
	bin, log := stubWorker(t, "exit 0")
	if err := diarizeWithWorker(bin, "s-1", nil); err != nil {
		t.Fatal(err)
	}
	if r := runs(t, log); len(r) != 1 || r[0] != "diarize-session s-1" {
		t.Errorf("runs = %q", r)
	}
}

func TestDiarizeWorkerRetriesCrashesAndFailures(t *testing.T) {
	for name, fail := range map[string]string{"crash": "kill -SEGV $$", "exit": "exit 3"} {
		t.Run(name, func(t *testing.T) {
			bin, log := stubWorker(t, `[ "$3" = --cpu ] && exit 0`+"\n"+fail)
			if err := diarizeWithWorker(bin, "s-2", DefaultDiarizeAttempts); err != nil {
				t.Fatal(err)
			}
			want := []string{"diarize-session s-2", "diarize-session s-2", "diarize-session s-2 --cpu"}
			if r := runs(t, log); strings.Join(r, "|") != strings.Join(want, "|") {
				t.Errorf("runs = %q, want %q", r, want)
			}
		})
	}
}

func TestDiarizeWorkerAllFail(t *testing.T) {
	bin, log := stubWorker(t, "kill -SEGV $$")
	ladder := []DiarizeAttempt{{Label: "a"}, {Label: "b", UseCPU: true}}
	err := diarizeWithWorker(bin, "s-3", ladder)
	if err == nil || !strings.Contains(err.Error(), "all 2 attempts") {
		t.Errorf("err = %v", err)
	}
	if r := runs(t, log); len(r) != 2 {
		t.Errorf("ran %d times, want 2", len(r))
	}
}

func TestDiarizeWorkerThatCannotStartStopsAtOnce(t *testing.T) {
	err := diarizeWithWorker(filepath.Join(t.TempDir(), "missing"), "s-4", nil)
	if err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Errorf("err = %v", err)
	}
}

func TestFindDiarizeWorkerOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX executable")
	}
	dir := t.TempDir()
	worker := filepath.Join(dir, diarizeWorkerName)
	if err := os.WriteFile(worker, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	// The test binary sits in a build directory with no worker beside it,
	// so the $PATH copy is the one found.
	if got := FindDiarizeWorker(); got != worker {
		t.Errorf("FindDiarizeWorker() = %q, want %q", got, worker)
	}
	t.Setenv("PATH", t.TempDir())
	if got := FindDiarizeWorker(); got != "" {
		t.Errorf("with no worker anywhere, got %q", got)
	}
}
