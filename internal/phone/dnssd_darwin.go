package phone

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Each dns-sd runs under a small sh wrapper, because Go can't make a child
// die with its parent on macOS: the wrapper holds dns-sd and kills it when
// its stdin closes, on Stop and whenever the app exits in any way (the
// pipe's write end is the app's), so no dns-sd outlives the app. The
// wrapper's reader keeps away from stdout, so the output ends when dns-sd
// does.
const dnssdWrapper = `exec 3<&0
/usr/bin/dns-sd "$@" </dev/null 2>/dev/null &
p=$!
( read -r _ <&3; kill "$p" 2>/dev/null ) >/dev/null 2>&1 &
wait "$p"`

type dnssdCmd struct {
	cmd   *exec.Cmd
	stdin io.Closer
	lines chan string
	quit  chan struct{}
	once  sync.Once
}

func platformDNSSD(args ...string) (dnssdProc, error) {
	if _, err := os.Stat("/usr/bin/dns-sd"); err != nil {
		return nil, errDNSSDUnsupported
	}
	// Arguments, never interpolated into the script: a host name comes
	// from the network.
	cmd := exec.Command("/bin/sh", append([]string{"-c", dnssdWrapper, "musubi-dns-sd"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	p := &dnssdCmd{cmd: cmd, stdin: stdin, lines: make(chan string), quit: make(chan struct{})}
	go func() {
		defer close(p.lines)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 1024), maxLine)
		for sc.Scan() {
			select {
			case p.lines <- strings.TrimRight(sc.Text(), "\r"):
			case <-p.quit:
				io.Copy(io.Discard, out)
				return
			}
		}
		// A line over maxLine stops the scan; nothing more is read.
		io.Copy(io.Discard, out)
	}()
	return p, nil
}

func (p *dnssdCmd) Lines() <-chan string { return p.lines }

// Stop closes the wrapper's stdin and signals its process group (the
// wrapper, dns-sd and the reader), killing it if it hasn't gone within
// two seconds.
func (p *dnssdCmd) Stop() {
	p.once.Do(func() {
		close(p.quit)
		p.stdin.Close()
		pgid := p.cmd.Process.Pid
		syscall.Kill(-pgid, syscall.SIGTERM)
		waited := make(chan struct{})
		go func() {
			p.cmd.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			syscall.Kill(-pgid, syscall.SIGKILL)
			<-waited
		}
	})
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
