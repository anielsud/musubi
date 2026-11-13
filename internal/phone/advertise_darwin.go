package phone

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The system's dns-sd registers the service for as long as it runs. Go
// can't make a child die with its parent on macOS, so a small sh wrapper
// holds dns-sd and stops it when its stdin closes: on Stop, and when the
// app exits in any way (the pipe's write end is the app's). It also exits
// when dns-sd does, so Done reports an advertisement that ended by itself.
const dnssdWrapper = `exec 3<&0
/usr/bin/dns-sd -R "$@" </dev/null >/dev/null 2>&1 &
p=$!
( read -r _ <&3; kill "$p" 2>/dev/null ) &
wait "$p"`

type dnssdProc struct {
	cmd   *exec.Cmd
	stdin io.Closer
	done  chan struct{}
}

func platformAdvertise(instance string, port int, txt []string) (advertProc, error) {
	if _, err := os.Stat("/usr/bin/dns-sd"); err != nil {
		return nil, errAdvertiseUnsupported
	}
	// Arguments, never interpolated into the script: the computer name is
	// the owner's text.
	args := append([]string{"-c", dnssdWrapper, "musubi-dns-sd", instance, ServiceType, "local.", strconv.Itoa(port)}, txt...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	p := &dnssdProc{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *dnssdProc) Done() <-chan struct{} { return p.done }

// Stop closes the wrapper's stdin and signals its process group (the
// wrapper, dns-sd and the reader), killing it if it hasn't gone within
// two seconds.
func (p *dnssdProc) Stop() {
	p.stdin.Close()
	pgid := p.cmd.Process.Pid
	syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		syscall.Kill(-pgid, syscall.SIGKILL)
		<-p.done
	}
}

func computerName() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "ComputerName").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}
