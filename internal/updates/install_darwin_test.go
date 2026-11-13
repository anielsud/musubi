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
	return fakeSigned(t, dir, id, version, payload, "-", "")
}

// fakeSigned is fakeApp signed by identity in keychain kc.
func fakeSigned(t *testing.T, dir, id, version, payload, identity, kc string) string {
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
	args := []string{"--sign", identity, "--force"}
	if kc != "" {
		args = append(args, "--keychain", kc)
	}
	if out, err := exec.Command("codesign", append(args, app)...).CombinedOutput(); err != nil {
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

// certs makes a scratch keychain with two self-signed code-signing
// identities and returns it and their SHA-1 hashes.
func certs(t *testing.T) (kc string, ids []string) {
	t.Helper()
	dir := t.TempDir()
	kc = filepath.Join(dir, "t.keychain-db")
	run := func(name string, args ...string) {
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
	}
	run("security", "create-keychain", "-p", "pw", kc)
	t.Cleanup(func() { exec.Command("security", "delete-keychain", kc).Run() })
	run("security", "unlock-keychain", "-p", "pw", kc)
	cnf := filepath.Join(dir, "c.cnf")
	os.WriteFile(cnf, []byte("[req]\ndistinguished_name=dn\nx509_extensions=ext\nprompt=no\n[dn]\nCN=Musubi Test\n[ext]\nbasicConstraints=critical,CA:false\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=critical,codeSigning\n"), 0o600)
	for _, n := range []string{"a", "b"} {
		key, crt, p12 := filepath.Join(dir, n+".key"), filepath.Join(dir, n+".pem"), filepath.Join(dir, n+".p12")
		run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", crt, "-days", "1", "-config", cnf)
		run("openssl", "pkcs12", "-export", "-inkey", key, "-in", crt, "-out", p12, "-passout", "pass:pw", "-legacy")
		run("security", "import", p12, "-k", kc, "-P", "pw", "-T", "/usr/bin/codesign")
		out, err := exec.Command("openssl", "x509", "-in", crt, "-noout", "-fingerprint", "-sha1").Output()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strings.ReplaceAll(strings.TrimSpace(strings.SplitN(string(out), "=", 2)[1]), ":", ""))
	}
	run("security", "set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-s", "-k", "pw", kc)
	return kc, ids
}

func TestInstallKeepsSigner(t *testing.T) {
	if testing.Short() {
		t.Skip("builds disk images")
	}
	const id = "io.example.musubi-test"
	kc, ids := certs(t)
	sameDMG := dmgOf(t, fakeSigned(t, t.TempDir(), id, "v0.3.0", "new", ids[0], kc))
	otherDMG := dmgOf(t, fakeSigned(t, t.TempDir(), id, "v0.3.0", "new", ids[1], kc))
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	files := map[string][]byte{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(files[r.URL.Path]) }))
	defer srv.Close()
	rel := &Release{Version: "v0.3.0", DMG: srv.URL + "/a.dmg", SHA256: srv.URL + "/a.sha256"}
	for _, c := range []struct {
		name string
		dmg  []byte
		ok   bool
	}{{"another certificate", otherDMG, false}, {"same certificate", sameDMG, true}} {
		t.Run(c.name, func(t *testing.T) {
			cur := fakeSigned(t, t.TempDir(), id, "v0.2.0", "old", ids[0], kc)
			files["/a.dmg"], files["/a.sha256"] = c.dmg, []byte(sum(c.dmg)+"  x\n")
			err := Install(context.Background(), srv.Client(), rel, cur, func(string) {})
			if c.ok && err != nil {
				t.Fatal(err)
			}
			if !c.ok && (err == nil || !strings.Contains(err.Error(), "certificate")) {
				t.Fatalf("err = %v, want refused for its certificate", err)
			}
		})
	}
}
