package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeApp writes a signed (ad hoc) bundle with id and version.
func fakeApp(t *testing.T, dir, id, version, payload string) string {
	t.Helper()
	app := filepath.Join(dir, "Musubi.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + id + `</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
<key>CFBundleExecutable</key><string>musubi-gui</string>
</dict></plist>`
	os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644)
	os.WriteFile(filepath.Join(app, "Contents", "MacOS", "musubi-gui"), []byte("#!/bin/sh\necho "+payload+"\n"), 0o755)
	if out, err := exec.Command("codesign", "--sign", "-", "--force", app).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v %s", err, out)
	}
	return app
}

func dmgOf(t *testing.T, app string) []byte {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	if out, err := exec.Command("ditto", app, filepath.Join(src, "Musubi.app")).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v %s", err, out)
	}
	dmg := filepath.Join(dir, "x.dmg")
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-volname", "T", "-srcfolder", src, "-format", "UDZO", "-ov", dmg).CombinedOutput(); err != nil {
		t.Fatalf("hdiutil: %v %s", err, out)
	}
	b, _ := os.ReadFile(dmg)
	return b
}

func TestInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("builds disk images")
	}
	const id = "io.example.musubi-test"
	newDMG := dmgOf(t, fakeApp(t, t.TempDir(), id, "v0.3.0", "new"))
	otherDMG := dmgOf(t, fakeApp(t, t.TempDir(), "io.example.other", "v0.3.0", "other"))
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

	files := map[string][]byte{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	rel := &Release{Version: "v0.3.0", DMG: srv.URL + "/a.dmg", SHA256: srv.URL + "/a.dmg.sha256"}

	cases := []struct {
		name    string
		dmg     []byte
		sum     string
		wantErr string
	}{
		{"checksum mismatch", newDMG, sum(otherDMG), "checksum"},
		{"another app", otherDMG, sum(otherDMG), "not \"" + id + "\""},
		{"good", newDMG, sum(newDMG), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			apps := t.TempDir()
			cur := fakeApp(t, apps, id, "v0.2.0", "old")
			files["/a.dmg"] = c.dmg
			files["/a.dmg.sha256"] = []byte(c.sum + "  Musubi-v0.3.0.dmg\n")
			err := Install(context.Background(), srv.Client(), rel, cur, func(string) {})
			got, _ := os.ReadFile(filepath.Join(cur, "Contents", "MacOS", "musubi-gui"))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				if !strings.Contains(string(got), "old") {
					t.Fatal("a refused update changed the app")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "new") {
				t.Fatalf("app not replaced: %s", got)
			}
			if _, err := os.Stat(filepath.Join(apps, oldApp)); err != nil {
				t.Fatal("the previous version wasn't kept until the next launch")
			}
			RemovePrevious(cur)
			if _, err := os.Stat(filepath.Join(apps, oldApp)); err == nil {
				t.Fatal("RemovePrevious left it")
			}
		})
	}
}
