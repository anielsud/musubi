package procstat

/*
#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/resource.h>
#include <mach/mach_time.h>

// responsibility_get_pid_responsible_for_pid is in libSystem but in no
// public header; it is how Activity Monitor puts an app's WebKit
// processes under the app. Weak, so a system without it still runs and
// the web view row is simply left out.
extern pid_t responsibility_get_pid_responsible_for_pid(pid_t) __attribute__((weak_import));

static int ps_has_responsibility(void) { return responsibility_get_pid_responsible_for_pid != NULL; }
static pid_t ps_responsible(pid_t pid) { return responsibility_get_pid_responsible_for_pid(pid); }

static int ps_bsdinfo(pid_t pid, int *ppid, uint32_t *uid, uint64_t *sec, uint64_t *usec) {
	struct proc_bsdinfo bi;
	int n = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &bi, sizeof bi);
	if (n != (int)sizeof bi) return -1;
	*ppid = (int)bi.pbi_ppid;
	*uid = bi.pbi_uid;
	*sec = bi.pbi_start_tvsec;
	*usec = bi.pbi_start_tvusec;
	return 0;
}

static int ps_rusage(pid_t pid, uint64_t *user, uint64_t *sys, uint64_t *rss, uint64_t *foot) {
	struct rusage_info_v2 ri;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&ri) != 0) return -1;
	*user = ri.ri_user_time;
	*sys = ri.ri_system_time;
	*rss = ri.ri_resident_size;
	*foot = ri.ri_phys_footprint;
	return 0;
}

static void ps_timebase(uint32_t *numer, uint32_t *denom) {
	mach_timebase_info_data_t tb;
	mach_timebase_info(&tb);
	*numer = tb.numer;
	*denom = tb.denom;
}
*/
import "C"

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// System is this Mac's process table, read through libproc (no `ps`).
func System() (Source, error) {
	var n, d C.uint32_t
	C.ps_timebase(&n, &d)
	if n == 0 || d == 0 {
		n, d = 1, 1
	}
	return &darwinSource{uid: uint32(os.Getuid()), numer: uint64(n), denom: uint64(d), paths: map[pathKey]string{}}, nil
}

// MachineInfo: cores and physical memory.
func MachineInfo() Machine {
	m := Machine{Cores: runtime.NumCPU()}
	if v, err := unix.SysctlUint64("hw.memsize"); err == nil {
		m.Memory = v
	}
	return m
}

type pathKey struct {
	pid   int
	start time.Time
}

type darwinSource struct {
	uid uint32
	// rusage times are in Mach ticks, not nanoseconds, on Apple silicon
	// (125/3 ns per tick); numer/denom convert them.
	numer, denom uint64
	mu           sync.Mutex
	// paths caches each process's executable path, keyed with its start
	// so a reused pid is read again.
	paths map[pathKey]string
}

func (s *darwinSource) Procs() ([]Proc, error) {
	// proc_listallpids with a NULL buffer returns the count; ask for a
	// little more room since processes start in between.
	cnt := C.proc_listallpids(nil, 0)
	if cnt <= 0 {
		return nil, errors.New("proc_listallpids failed")
	}
	buf := make([]C.int, int(cnt)+64)
	got := C.proc_listallpids(unsafe.Pointer(&buf[0]), C.int(len(buf))*C.int(unsafe.Sizeof(buf[0])))
	if got <= 0 {
		return nil, errors.New("proc_listallpids failed")
	}
	if int(got) > len(buf) {
		got = C.int(len(buf))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[pathKey]bool, int(got))
	out := make([]Proc, 0, int(got))
	for _, cp := range buf[:got] {
		pid := int(cp)
		if pid <= 0 {
			continue
		}
		var ppid C.int
		var uid C.uint32_t
		var sec, usec C.uint64_t
		if C.ps_bsdinfo(C.pid_t(pid), &ppid, &uid, &sec, &usec) != 0 {
			continue
		}
		// Only this user's processes: everything the readout shows runs
		// as the user, and others' usage isn't readable anyway.
		if uint32(uid) != s.uid {
			continue
		}
		p := Proc{PID: pid, PPID: int(ppid), Start: time.Unix(int64(sec), int64(usec)*1000)}
		k := pathKey{pid, p.Start}
		path, ok := s.paths[k]
		if !ok {
			path = pidPath(pid)
			s.paths[k] = path
		}
		seen[k] = true
		p.Path = path
		out = append(out, p)
	}
	for k := range s.paths {
		if !seen[k] {
			delete(s.paths, k)
		}
	}
	return out, nil
}

func pidPath(pid int) string {
	buf := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
	n := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}

func (s *darwinSource) Usage(pid int) (Usage, error) {
	var user, sys, rss, foot C.uint64_t
	if C.ps_rusage(C.pid_t(pid), &user, &sys, &rss, &foot) != 0 {
		return Usage{}, errors.New("proc_pid_rusage failed")
	}
	ticks := uint64(user) + uint64(sys)
	// Split to avoid overflowing ticks*numer for long-lived processes.
	ns := ticks/s.denom*s.numer + ticks%s.denom*s.numer/s.denom
	return Usage{CPU: time.Duration(ns), RSS: uint64(rss), Footprint: uint64(foot)}, nil
}

func (s *darwinSource) Responsible(pid int) (int, bool) {
	if C.ps_has_responsibility() == 0 {
		return 0, false
	}
	r := int(C.ps_responsible(C.pid_t(pid)))
	return r, r > 0
}

// Args reads argv through the kern.procargs2 sysctl: a 32-bit argc, the
// executable path, NUL padding, then argc NUL-terminated arguments.
func (s *darwinSource) Args(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcArgs(b)
}
