#!/usr/bin/env python3
"""Plays the Mac against the phone app on an EMULATOR (docs/phone.md, "Sync: the Mac dials the phone").

Over `adb forward`, a mutual-TLS client speaks the framed protocol to the app's server on port
47616 and drives the app's screens with uiautomator: records a short meeting, pairs (cancel, then
confirm, checking the code on screen against its own), lists, fetches every chunk, reports
`imported` (audio deleted, meta kept), pushes an agenda (shown on Today), says `bye` (window
ends), and checks that a certificate other than the pinned one is refused after pairing.

    python3 -I android/synctest/fake_mac.py --serial emulator-5600

The app must be a debug build with short chunks (./gradlew assembleDebug -PchunkSeconds=10),
installed on the emulator. The script clears the app's data. It refuses any device that is not
an emulator. Certificates are throwaway, made with openssl in a temporary folder.
"""
import argparse
import base64
import hashlib
import json
import os
import re
import shutil
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone

PKG = "com.musubi.phone"
PORT = 47616
ADB = os.path.join(os.environ.get("ANDROID_HOME", "/opt/homebrew/share/android-commandlinetools"), "platform-tools", "adb")


class Fail(Exception):
    pass


def check(cond, what):
    if not cond:
        raise Fail(what)
    print("  ok:", what)


class Device:
    def __init__(self, serial):
        self.serial = serial

    def adb(self, *args, check_rc=True):
        r = subprocess.run([ADB, "-s", self.serial, *args], capture_output=True)
        if check_rc and r.returncode != 0:
            raise Fail("adb %s failed: %s" % (args[0], r.stderr.decode(errors="replace")[:200]))
        return r.stdout

    def shell(self, *args, check_rc=True):
        return self.adb("shell", *args, check_rc=check_rc).decode(errors="replace")

    def nodes(self):
        # Remove the last dump first: a failed dump (screen never idle) must not read a stale one.
        self.shell("rm", "-f", "/sdcard/ui.xml")
        self.shell("uiautomator", "dump", "/sdcard/ui.xml", check_rc=False)
        if "ui.xml" not in self.shell("ls", "/sdcard/", check_rc=False):
            return []
        xml = self.adb("exec-out", "cat", "/sdcard/ui.xml").decode(errors="replace")
        out = []
        for m in re.finditer(r'<node [^>]*>', xml):
            n = m.group(0)
            text = re.search(r' text="([^"]*)"', n).group(1)
            desc = re.search(r' content-desc="([^"]*)"', n).group(1)
            b = [int(x) for x in re.findall(r"\d+", re.search(r' bounds="([^"]*)"', n).group(1))]
            out.append((text, desc, b))
        return out

    def screen_text(self):
        return "\n".join(t for t, _, _ in self.nodes() if t)

    def tap(self, label, timeout=10):
        end = time.time() + timeout
        while time.time() < end:
            for text, desc, b in self.nodes():
                if label.lower() in (text.lower(), desc.lower()):
                    self.shell("input", "tap", str((b[0] + b[2]) // 2), str((b[1] + b[3]) // 2))
                    return
            time.sleep(0.5)
        raise Fail("no %r on screen" % label)

    def wait_text(self, pattern, timeout=15):
        end = time.time() + timeout
        while time.time() < end:
            s = self.screen_text()
            m = re.search(pattern, s)
            if m:
                return m
            time.sleep(0.5)
        raise Fail("screen never showed %r; it shows:\n%s" % (pattern, self.screen_text()))

    def run_as(self, *args):
        return self.shell("run-as", PKG, *args, check_rc=False)


def make_cert(d, name):
    key, crt = os.path.join(d, name + ".key"), os.path.join(d, name + ".crt")
    # serverAuth-only EKU, like the Mac's first-version certificate: the phone must not care.
    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
                    "-keyout", key, "-out", crt, "-days", "30", "-subj", "/CN=" + name,
                    "-addext", "extendedKeyUsage=serverAuth", "-addext", "keyUsage=digitalSignature"],
                   check=True, capture_output=True)
    der = subprocess.run(["openssl", "x509", "-in", crt, "-outform", "DER"], check=True, capture_output=True).stdout
    return crt, key, hashlib.sha256(der).digest()


class Conn:
    """One mutual-TLS connection to the phone, with the protocol's framing."""

    def __init__(self, cert, key):
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE  # the phone's certificate is pinned by hash below, as the Mac does
        ctx.minimum_version = ssl.TLSVersion.TLSv1_2
        ctx.load_cert_chain(cert, key)
        raw = socket.create_connection(("127.0.0.1", PORT), timeout=10)
        self.s = ctx.wrap_socket(raw)
        self.s.settimeout(150)
        self.peer_der = self.s.getpeercert(binary_form=True)
        self.phone_fp = hashlib.sha256(self.peer_der).digest()

    def send(self, obj, body=None):
        j = json.dumps(obj).encode()
        self.s.sendall(struct.pack(">I", len(j)) + j + (body or b""))

    def recv_exact(self, n):
        buf = b""
        while len(buf) < n:
            part = self.s.recv(n - len(buf))
            if not part:
                raise EOFError("closed")
            buf += part
        return buf

    def recv(self):
        n = struct.unpack(">I", self.recv_exact(4))[0]
        if n > 65536:
            raise Fail("reply frame too large")
        j = json.loads(self.recv_exact(n))
        body = self.recv_exact(j["body"]) if "body" in j else None
        return j, body

    def call(self, obj, body=None):
        self.send(obj, body)
        return self.recv()

    def close(self):
        try:
            self.s.close()
        except OSError:
            pass


def connect_retry(cert, key, timeout=20):
    end = time.time() + timeout
    while True:
        try:
            return Conn(cert, key)
        except (OSError, ssl.SSLError):
            if time.time() > end:
                raise
            time.sleep(1)


def code(mac_fp, phone_fp, mac_nonce, phone_nonce):
    h = hashlib.sha256(b"musubi-pair-v2" + mac_fp + phone_fp + mac_nonce + phone_nonce).digest()
    return "%06d" % (int.from_bytes(h[:4], "big") % 1000000)


def refused(cert, key):
    """True when the phone will not serve this certificate: no connection or no reply."""
    try:
        c = Conn(cert, key)
    except (OSError, ssl.SSLError):
        return True
    try:
        c.call({"op": "hello", "v": 2, "mac_name": "Stranger"})
        return False
    except (OSError, ssl.SSLError, EOFError):
        return True
    finally:
        c.close()


def pair_attempt(dev, mac, confirm):
    dev.tap("Mac")
    dev.tap("Pair with your Mac")
    dev.wait_text(r"Waiting for your Mac")
    c = connect_retry(mac[0], mac[1])
    mac_nonce = os.urandom(16)
    r, _ = c.call({"op": "pair_request", "mac_name": "Test Mac", "nonce": base64.b64encode(mac_nonce).decode()})
    check(r.get("ok") is True and r.get("device_name"), "pair_request answered with the device name")
    phone_nonce = base64.b64decode(r["nonce"])
    want = code(mac[2], c.phone_fp, mac_nonce, phone_nonce)
    shown = dev.wait_text(r"\b(\d{3}) (\d{3})\b")
    check(shown.group(1) + shown.group(2) == want, "the phone shows the code the Mac computes")
    dev.tap("Confirm" if confirm else "Cancel")
    r, _ = c.call({"op": "pair_confirm"})
    return c, r


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--serial", required=True)
    args = ap.parse_args()
    dev = Device(args.serial)
    # Never anything but an emulator: this script wipes the app's data.
    if dev.shell("getprop", "ro.kernel.qemu").strip() != "1" and dev.shell("getprop", "ro.boot.qemu").strip() != "1":
        sys.exit("refusing: %s is not an emulator" % args.serial)

    tmp = tempfile.mkdtemp(prefix="fake-mac-")
    try:
        mac = make_cert(tmp, "test-mac")
        stranger = make_cert(tmp, "stranger")

        print("setup")
        dev.shell("pm", "clear", PKG)
        for p in ("android.permission.RECORD_AUDIO", "android.permission.POST_NOTIFICATIONS"):
            dev.shell("pm", "grant", PKG, p, check_rc=False)
        dev.adb("forward", "tcp:%d" % PORT, "tcp:%d" % PORT)
        dev.shell("am", "start", "-n", PKG + "/.MainActivity")
        time.sleep(3)

        print("record a short meeting")
        dev.tap("Record (ad hoc)")
        time.sleep(25)
        # The Recording screen never goes idle (timer, level meter), so uiautomator cannot read
        # it: tap where Stop is on a phone-shaped screen (centre, just under half the height).
        w, h = [int(x) for x in re.findall(r"\d+", dev.shell("wm", "size").split(":")[-1])][:2]
        dev.shell("input", "tap", str(w // 2), str(int(h * 0.49)))
        time.sleep(3)
        dev.wait_text(r"Record \(ad hoc\)")

        print("unpaired: nothing listens")
        check(refused(mac[0], mac[1]), "no server before pairing")

        print("pair, cancelled on the phone")
        c, r = pair_attempt(dev, mac, confirm=False)
        check(r == {"ok": False, "error": "cancelled"}, "pair_confirm answers cancelled")
        c.close()
        dev.wait_text(r"Pairing cancelled")

        print("pair, confirmed")
        c, r = pair_attempt(dev, mac, confirm=True)
        check(r == {"ok": True}, "pair_confirm answers ok")
        tls = c.s.version(), c.s.cipher()[0]
        check(tls[0] in ("TLSv1.3", "TLSv1.2") and ("GCM" in tls[1] or "CHACHA20" in tls[1]), "TLS %s with %s" % tls)
        der = c.peer_der
        with open(os.path.join(tmp, "phone.der"), "wb") as f:
            f.write(der)
        text = subprocess.run(["openssl", "x509", "-inform", "DER", "-in", os.path.join(tmp, "phone.der"), "-noout", "-text"],
                              capture_output=True, check=True).stdout.decode()
        check("prime256v1" in text and "ecdsa-with-SHA256" in text, "phone certificate is ECDSA P-256")
        pem = os.path.join(tmp, "phone.pem")
        subprocess.run(["openssl", "x509", "-inform", "DER", "-in", os.path.join(tmp, "phone.der"), "-out", pem], check=True)
        ss = subprocess.run(["openssl", "verify", "-check_ss_sig", "-partial_chain", "-CAfile", pem, pem], capture_output=True)
        check(ss.returncode == 0, "phone certificate is self-signed by the Keystore key")
        phone_fp = c.phone_fp
        print("  phone certificate SHA-256 starts", phone_fp.hex()[:16])
        instance = "musubi-%s._musubi-phone._tcp" % phone_fp.hex()[:8]
        check(instance in dev.shell("dumpsys", "servicediscovery"), "advertised as %s" % instance)

        print("sync on the same connection")
        r, _ = c.call({"op": "hello", "v": 2, "mac_name": "Test Mac"})
        check(r.get("ok") and r.get("v") == 2 and r.get("app_version") == "0.3.0" and isinstance(r.get("addresses"), list),
              "hello: v2, app version, addresses %s" % r.get("addresses"))
        r, _ = c.call({"op": "list"})
        recs = r["recordings"]
        check(len(recs) == 1 and recs[0]["state"] is None, "list: one finished recording, no state yet")
        meta = recs[0]["meta"]
        rid = meta["id"]
        check(len(meta["chunks"]) >= 2, "%d chunks in meta" % len(meta["chunks"]))
        for ch in meta["chunks"]:
            r, body = c.call({"op": "chunk", "rid": rid, "n": ch["n"]})
            check(r.get("ok") and len(body) == ch["size"] and hashlib.sha256(body).hexdigest() == ch["sha256"] == r["sha256"],
                  "chunk %d: %d bytes, SHA-256 matches meta" % (ch["n"], len(body)))
        files = dev.run_as("ls", "files/recordings/" + rid)
        check("chunk-0000.aac" in files, "audio on the phone before import")
        r, _ = c.call({"op": "state", "rid": rid, "state": "imported", "session_id": "test-session"})
        check(r == {"ok": True}, "state imported accepted")
        files = dev.run_as("ls", "files/recordings/" + rid)
        check("chunk-" not in files and "meta.json" in files, "audio deleted, meta.json kept")
        start = (datetime.now(timezone.utc) + timedelta(hours=2)).replace(microsecond=0)
        agenda = {"events": [{"id": "ev-1", "start": start.isoformat(), "end": (start + timedelta(hours=1)).isoformat(),
                              "subject": "Invented planning session", "location": "Room 1", "organizer": "Test Organizer",
                              "attendees": [{"name": "Test Person"}],
                              "brief": {"text": "An invented brief.", "made_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat(),
                                        "sensitivity": {"level": "internal", "share": "internal", "why": ""}}}],
                  "drafting": 0, "made_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat()}
        body = json.dumps(agenda).encode()
        r, _ = c.call({"op": "agenda", "body": len(body)}, body)
        check(r == {"ok": True}, "agenda accepted")
        r, _ = c.call({"op": "bye"})
        check(r == {"ok": True}, "bye answered")
        c.close()

        print("results on screen")
        dev.tap("Mac")
        dev.wait_text(r"Paired with Test Mac")
        dev.wait_text(r"Synced: \d+ parts sent, 1 transcribed, agenda updated")
        dev.tap("Today")
        dev.wait_text(r"Invented planning session")
        dev.tap("Recordings")
        dev.wait_text(r"Transcribed")

        print("window ended after the sync")
        time.sleep(3)
        check(refused(mac[0], mac[1]), "not listening after bye with nothing pending")
        check("Advertiser: serviceFullName" not in dev.shell("dumpsys", "servicediscovery"), "advertisement withdrawn")

        print("reopening the app opens a window")
        dev.shell("input", "keyevent", "HOME")
        time.sleep(2)
        dev.shell("am", "start", "-n", PKG + "/.MainActivity")
        c = connect_retry(mac[0], mac[1])
        check(c.phone_fp == phone_fp, "same phone certificate (Keystore key kept)")
        r, _ = c.call({"op": "hello", "v": 2, "mac_name": "Test Mac"})
        check(r.get("ok") is True, "pinned Mac served")
        r, _ = c.call({"op": "list"})
        check(r["recordings"] == [], "imported recording no longer listed")
        r, _ = c.call({"op": "chunk", "rid": "00000000-0000-4000-8000-000000000000", "n": 0})
        check(r == {"ok": False, "error": "unknown_recording"}, "unknown recording refused")
        c.close()

        print("a certificate other than the pinned one")
        check(refused(stranger[0], stranger[1]), "unpinned certificate refused after pairing")
        dev.tap("Mac")
        dev.wait_text(r"Refused a Mac that is not the paired one")

        print("protocol violations close the connection")
        c = connect_retry(mac[0], mac[1])
        c.send({"op": "delete"})
        try:
            c.recv()
            check(False, "unknown op closes")
        except (EOFError, OSError, ssl.SSLError):
            check(True, "unknown op closes without a reply")
        c.close()
        c = connect_retry(mac[0], mac[1])
        c.s.sendall(struct.pack(">I", 65537))
        try:
            c.recv()
            check(False, "oversize closes")
        except (EOFError, OSError, ssl.SSLError):
            check(True, "oversize frame closes without a reply")
        c.close()
        print("PASS")
    except Fail as e:
        print("FAIL:", e)
        sys.exit(1)
    finally:
        subprocess.run([ADB, "-s", args.serial, "forward", "--remove", "tcp:%d" % PORT], capture_output=True)
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    main()
