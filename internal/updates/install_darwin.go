package updates

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	maxDMG = 1 << 30 // far above a real DMG (~50 MB); stops a runaway download
	oldApp = ".Musubi-previous.app"
)

// AppPath is the .app bundle the running binary is in, if it can be
// replaced: not on a disk image and not in macOS's App Translocation (an
// app opened in place from Downloads).
func AppPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	switch {
	case filepath.Ext(app) != ".app":
		return "", errors.New("Musubi isn't running from an app bundle")
	case strings.HasPrefix(app, "/Volumes/"):
		return "", errors.New("Musubi is running from the disk image; drag it to Applications first")
	case strings.Contains(app, "/AppTranslocation/"):
		return "", errors.New("macOS is running Musubi from a temporary copy; move it to Applications first")
	}
	return app, nil
}

// Install downloads r's DMG, checks it against its SHA-256 file, that it
// is Musubi (the running app's bundle identifier) at r's version and that
// its signature verifies, then puts it in place of app. The app it
// replaces is kept beside it as .Musubi-previous.app until the next
// launch (RemovePrevious), so a failed swap is put back. step reports
// progress.
func Install(ctx context.Context, client *http.Client, r *Release, app string, step func(string)) error {
	if r == nil || r.DMG == "" || r.SHA256 == "" {
		return errors.New("this release has no installer to update from")
	}
	work, err := os.MkdirTemp("", "musubi-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	step("Downloading " + r.Version)
	want, err := fetchSum(ctx, client, r.SHA256)
	if err != nil {
		return fmt.Errorf("reading the checksum: %w", err)
	}
	dmg := filepath.Join(work, "Musubi.dmg")
	got, err := download(ctx, client, r.DMG, dmg)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	if got != want {
		return errors.New("the download doesn't match its checksum; nothing was changed")
	}

	step("Checking " + r.Version)
	mnt := filepath.Join(work, "mnt")
	if err := run(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg); err != nil {
		return fmt.Errorf("opening the disk image: %w", err)
	}
	defer exec.Command("hdiutil", "detach", "-force", mnt).Run()
	next := filepath.Join(mnt, "Musubi.app")
	if err := sameApp(ctx, app, next, r.Version); err != nil {
		return err
	}
	if err := run(ctx, "codesign", "--verify", "--deep", "--strict", next); err != nil {
		return fmt.Errorf("the new version's signature doesn't verify: %w", err)
	}

	step("Installing " + r.Version)
	dir := filepath.Dir(app)
	staged := filepath.Join(dir, ".Musubi-"+r.Version+".app")
	os.RemoveAll(staged)
	if err := run(ctx, "ditto", next, staged); err != nil {
		os.RemoveAll(staged)
		return fmt.Errorf("copying into %s: %w", dir, err)
	}
	// Downloaded by Musubi, not a browser, so it carries no quarantine;
	// cleared in case anything added one.
	exec.Command("xattr", "-dr", "com.apple.quarantine", staged).Run()
	prev := filepath.Join(dir, oldApp)
	os.RemoveAll(prev)
	if err := os.Rename(app, prev); err != nil {
		os.RemoveAll(staged)
		return fmt.Errorf("moving the current version aside: %w", err)
	}
	if err := os.Rename(staged, app); err != nil {
		if rerr := os.Rename(prev, app); rerr != nil {
			return fmt.Errorf("installing failed (%v) and putting the current version back failed: %w; it is at %s", err, rerr, prev)
		}
		os.RemoveAll(staged)
		return fmt.Errorf("installing: %w; the current version is unchanged", err)
	}
	return nil
}

// RemovePrevious deletes the version an update replaced, once the new
// one has started.
func RemovePrevious(app string) {
	os.RemoveAll(filepath.Join(filepath.Dir(app), oldApp))
}

// Relaunch opens app again once this process (pid) has exited. The
// helper outlives us; app and pid are arguments, never part of the
// script.
func Relaunch(app string, pid int) error {
	cmd := exec.Command("/bin/sh", "-c", `while kill -0 "$1" 2>/dev/null; do sleep 0.3; done; exec /usr/bin/open "$2"`,
		"sh", strconv.Itoa(pid), app)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func sameApp(ctx context.Context, app, next, version string) error {
	id, err := plistValue(ctx, filepath.Join(app, "Contents", "Info.plist"), "CFBundleIdentifier")
	if err != nil {
		return err
	}
	nid, err := plistValue(ctx, filepath.Join(next, "Contents", "Info.plist"), "CFBundleIdentifier")
	if err != nil {
		return fmt.Errorf("the disk image holds no Musubi.app: %w", err)
	}
	if nid != id {
		return fmt.Errorf("the disk image holds %q, not %q", nid, id)
	}
	if v, err := plistValue(ctx, filepath.Join(next, "Contents", "Info.plist"), "CFBundleShortVersionString"); err != nil || v != version {
		return fmt.Errorf("the disk image holds version %q, not %s", v, version)
	}
	return nil
}

func plistValue(ctx context.Context, plist, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "plutil", "-extract", key, "raw", "-o", "-", plist).Output()
	if err != nil {
		return "", fmt.Errorf("reading %s from %s: %w", key, plist, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Musubi")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	// GitHub redirects downloads to its file host; stay on HTTPS.
	if resp.Request.URL.Scheme != "https" {
		resp.Body.Close()
		return nil, fmt.Errorf("redirected off HTTPS")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return resp, nil
}

// fetchSum reads a sha256sum line ("<hex>  <file>").
func fetchSum(ctx context.Context, client *http.Client, url string) (string, error) {
	resp, err := get(ctx, client, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	line, _ := bufio.NewReader(io.LimitReader(resp.Body, 4096)).ReadString('\n')
	f := strings.Fields(line)
	if len(f) == 0 {
		return "", errors.New("empty checksum file")
	}
	sum := strings.ToLower(f[0])
	if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
		return "", errors.New("not a SHA-256 checksum")
	}
	return sum, nil
}

// download writes url to dst and returns its SHA-256.
func download(ctx context.Context, client *http.Client, url, dst string) (string, error) {
	resp, err := get(ctx, client, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDMG+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > maxDMG {
		return "", errors.New("the download is larger than any Musubi installer")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
