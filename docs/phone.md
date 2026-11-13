# Musubi on the phone

Status: being built (first round for on-site meetings, 2026-11-16 week).

## Why

Some meetings happen away from the Mac: on site at a conference, in
person, ad hoc in a corridor. The phone records them and carries prep;
the Mac, when the phone can next reach it, turns the audio into a
meeting like any other. The Mac is not reachable while the phone records,
so the phone works fully offline.

## What we know

- Offline transcription of a mic-only recording works: the recorder's
  offline replay matched Tomoe byte for byte on a 75-minute mic-only
  interview ([recorder-parity.md](recorder-parity.md)), and the owner has
  unpacked phone audio with Tomoe's `transcribe` before.
- Musubi links a recorder session to a calendar event by time
  (`occurred_as` links) and writes `calendar.json` into the session
  folder ([internal/engine/m365.go](../internal/engine/m365.go)).
- Prep briefs are drafted automatically only for the rest of the current
  week ([internal/gui/prep.go](../internal/gui/prep.go), `draftBriefsOnce`).
- Android records the microphone from a foreground service of type
  `microphone`. It cannot record calls or other apps' audio.

## What we don't know

1. Speaker labels on a phone lying on a table in a noisy venue. The
   pipeline handles a mic-only room; a conference floor is untested.
2. How the owner's phone (model, Android version, battery saver) treats a
   two-hour foreground recording. Tested on the device before the trip.
3. Whether Outlook on the phone shares its calendar with Android's
   calendar. Not needed: the phone also carries the events Musubi knows.

## Design

Three parts. Parts 1 and 2 are this round; part 3 comes after.

### 1. The phone app (`android/`)

Kotlin, one activity, minSdk 26, targetSdk 35. Few dependencies (AndroidX
core, lifecycle, WorkManager, Material components).

**Screens**
- **Today**: the next 10 days' events (from the last sync, merged with
  the phone's own calendar when it allows reading it), grouped by day.
  Each shows time, subject, attendees and a brief if one was synced. Tap
  an event → **Record this meeting**. A large **Record (ad hoc)** button
  is always on top.
- **Recording**: elapsed time, a level meter (so a dead mic is obvious),
  the event it belongs to (changeable afterwards), **Stop**.
- **Recordings**: each recording with its state: recording, on phone,
  uploading, on Mac, transcribed, failed (with the reason).
- **Brief**: the brief text, its sensitivity label shown, nothing hidden.
- **Mac**: pair, sync now, last sync time, unpair.

**Recording**
- Foreground service, type `microphone`, with an ongoing notification
  that has Stop. A partial wake lock while recording.
- `AudioRecord` + `MediaCodec`, AAC-LC in ADTS, 48 kHz mono, 96 kbps,
  source `VOICE_RECOGNITION` (falls back to `MIC`). A new file every 5
  minutes, cut on a frame boundary with no gap, so a crash or a dead
  battery loses at most the last seconds: ADTS is readable up to the last
  frame. (`MediaRecorder.setNextOutputFile` only works for MPEG-4.)
- Each recording is a folder in app-private storage: `meta.json` (id,
  start time with zone offset, end time, device name, the chosen event's
  id/subject/start or none, chunk list with size and SHA-256) and
  `chunk-0000.aac`, `chunk-0001.aac`, …. `meta.json` is rewritten
  (write temp, rename) at start, at each new chunk and at stop.
- On launch, a recording left "recording" by a killed process is closed:
  its chunks are kept, its end time is the last chunk's modified time.
- Refuses to start with less than 500 MB free; stops cleanly under
  100 MB. An incoming phone call takes the mic: the recording continues
  and records what it can.

**Storage and privacy**
- Everything lives in app-private storage, which Android encrypts at rest
  with a key tied to the lock screen. `allowBackup="false"` and data
  extraction rules exclude all files from cloud backup and device
  transfer.
- The pairing token is encrypted with an AES-256-GCM key held in the
  Android Keystore (not exportable).
- Audio leaves the phone only to the paired Mac.

**Live text** (owner's decision of 2026-11-13: build now, start/stop,
a light model)
- A **Live text** switch on the Recording screen turns on-device
  transcription on and off at any time during a recording. Off by
  default; the app remembers the last choice.
- A small streaming English model (sherpa-onnx, Apache-2.0, a ~20M
  parameter streaming zipformer, int8), downloaded once from a pinned
  URL with a pinned SHA-256 (**Settings → Live text model → Download**),
  kept in app-private storage. Nothing is sent anywhere.
- It reads the same audio the recorder captures (resampled to 16 kHz)
  through a bounded queue on its own thread. It can never slow or stop the
  recording: when it falls behind it drops audio for the live text only;
  any error turns it off and says so.
- The text is shown on the Recording screen and kept as `live.txt` in
  the recording's folder, readable on the phone afterwards; it is not
  uploaded (the Mac transcribes the audio itself) and is deleted with
  the audio.

**Updates** (owner's decision of 2026-11-13)
- Releases are published to the public repo `anielsud/musubi-releases`
  with tag `android-v<versionName>` and two assets:
  `musubi-phone-<versionName>.apk` and `musubi-phone-<versionName>.apk.sha256`.
- The app checks on start (at most every 6 hours) and on **Check for
  updates** (Settings): the GitHub releases API, unauthenticated; the
  newest `android-v*` tag above the installed version.
- It downloads over HTTPS from GitHub's hosts only, at most 100 MB,
  checks the SHA-256 (corruption) and that the APK is signed by the same
  certificate as the installed app (authenticity; Android enforces it
  too), then installs through `PackageInstaller` (no user action needed
  where Android allows it; otherwise one tap).
- Never while recording: an update found during a recording waits until
  it stops.
- Signing: one release key, kept outside git
  (`~/.config/musubi/android/release.jks`, mode 0600), its passwords in
  the macOS Keychain. `android/release.sh` builds, signs and writes the
  checksum; it publishes only with `--publish`.

**Finding the Mac** (owner's decision of 2026-11-13: no typed addresses)
- The Mac advertises `_musubi._tcp` on the local network (Bonjour) while
  phone access is on: instance name `Musubi on <computer name>`, port,
  TXT `v=1` and `fp=<first 16 hex digits of the certificate
  fingerprint>`.
- The phone lists the Macs it finds (Android NSD). After pairing it keeps
  the Mac's instance name and pinned fingerprint and finds its current
  address the same way at every sync (the address may change); the last
  address that worked is tried first. Bonjour does not cross Tailscale; a
  manual address stays available under **Advanced**.

**Sync** (WorkManager, when the Mac is reachable, and on **Sync now**)
- Upload each finished recording's chunks, then complete it. Delete a
  recording's audio from the phone only once the Mac reports it
  `imported` (saved as a meeting session). Keep `meta.json` for 30 days
  so the list shows what happened.
- Fetch the agenda (events and briefs). Replace the phone's copy; drop
  events that have ended more than a day ago.

### 2. The Mac side

**Phone access server** (in the Musubi app, `internal/phone`): off until
the owner turns on **Settings → Phone**. HTTPS only, on a fixed port
(47615). Accepts connections only from private addresses (RFC 1918,
100.64.0.0/10 for Tailscale, link-local, loopback); everything else is
closed at accept.

- **TLS**: a self-signed ECDSA P-256 certificate made on first use, kept
  in the Musubi data folder (key file mode 0600). The phone pins its
  SHA-256 fingerprint at pairing.
- **Pairing, confirmed on both** (numeric comparison, as Bluetooth does):
  1. The phone picks a Mac it found, connects (TLS, noting the
     certificate's SHA-256; its first 16 hex digits must match the
     advertised `fp`), and sends `POST /v1/pair/request {device_name,
     nonce}` with 16 random bytes; the Mac answers `{request_id, nonce}`
     with 16 of its own.
  2. Both compute the same 6-digit code: the first 4 bytes of
     SHA-256(`"musubi-pair-v1"` ‖ certificate SHA-256 (32 bytes) ‖ phone
     nonce ‖ Mac nonce) as a big-endian unsigned number, mod 1,000,000,
     zero-padded. A machine in the middle shows the phone a different
     certificate, so the codes would differ.
  3. Musubi brings its window forward with "Pair <device name>? Code
     123 456" and **Allow** / **Deny**; the phone shows the code with
     **Confirm** / **Cancel**. The phone's Confirm is
     `POST /v1/pair/{request_id}/confirm`.
  4. The phone polls `GET /v1/pair/{request_id}`: `pending`, `denied`,
     `expired`, or, once both have confirmed, `{device_id, token}`
     (returned once; the request is then gone).
  - A request expires after 2 minutes. One pending request at a time;
    at most 5 requests per 10 minutes; after 3 denials in 10 minutes
    requests are refused for 10 minutes. The 8-digit code flow is
    removed.
- **Devices**: pairing issues a 32-byte random token. The Mac stores only
  its SHA-256 and the device's name and dates. **Settings → Phone** lists
  devices and revokes them. Tokens travel in `Authorization: Bearer`,
  never in a URL. Every response is `Cache-Control: no-store`.
- Pairing, revocation, upload completion and failures are logged without
  tokens or codes.

**API** (JSON unless noted; every call but the `pair` calls needs the token)

| Call | Body → reply |
|---|---|
| `POST /v1/pair/request` | `{device_name, nonce}` (base64, 16 bytes) → `{request_id, nonce}` |
| `POST /v1/pair/{request_id}/confirm` | → `204` |
| `GET /v1/pair/{request_id}` | → `{state: pending \| denied \| expired}` or `{state: "paired", device_id, token}` |
| `GET /v1/agenda?days=10` | → `{events: [{id, start, end, subject, location, organizer, attendees: [{name}], brief: {text, made_at, sensitivity: {level, share, why}} \| null}], drafting: n}` |
| `PUT /v1/recordings/{rid}/chunks/{n}` | `audio/aac` body, header `X-Sha256` → `204`. Same chunk again: `204`; different bytes: `409`. At most 64 MB. |
| `POST /v1/recordings/{rid}/complete` | the phone's `meta.json` → `202 {state}` |
| `GET /v1/recordings/{rid}` | → `{state: received \| waiting \| transcribing \| imported \| failed, session_id, error}` |

`rid` is a UUID made by the phone; `n` is 0–9999. The agenda is the owner's
own (owner scope): the phone carries the same briefs the owner sees.
`days` is capped at 14. An agenda request starts drafting any missing
briefs in that window in the background (the same drafting as the weekly
one, not during a live meeting); `drafting` says how many are left, and
the phone asks again later.

**Import**: on `complete`, the server checks every chunk against
`meta.json`, joins the ADTS chunks into one file in the recorder's inbox,
and asks the recorder to import it (control op `import`). It never runs
while the recorder is recording a live meeting (state `waiting`).

**Recorder import** (`musubi-recorder import <file>` and control op
`import`): creates a session from an outside audio file: start time from
the caller, the audio as the session's single mic track (`audio.m4a`),
transcribed and diarized by the same offline pipeline as `session
replay`'s current config, then the same post-meeting steps as a recorded
meeting (speaker labels; voice profiles name speakers but are not
taught from phone audio unless `phone_teaches_voices` is on, the owner's
decision of 2026-11-13). The session is marked
`platform: "phone"`. Musubi then picks it up like any finished session
and links it to the calendar event by time; when the phone named an
event, that event is the link.

### 3. Musubi's screens on the phone (after this round)

The Musubi app serves its own screens (the same frontend) to paired
phones over the same server, for reading and Ask only: an allowlist of
read methods plus Ask, events streamed over server-sent events, the
session in an `HttpOnly; Secure; SameSite=Strict` cookie set by the app.
Not built yet.

## Trade-offs

- No hints on the phone, and its live text is a preview from a small
  model: the Mac's transcript is the record.
- Updates come from a public repo: the APK can be downloaded and read by
  anyone (no data or keys in it), as the Mac installers already can.
- The Mac announces itself on the local network while phone access is
  on, so others on that network can see a Musubi is there.
- The Mac gains a network listener, off by default, private addresses
  only, paired devices only. It is the one new attack surface.
- Audio and briefs sit on the phone until the next sync. Protection is
  Android's storage encryption and the lock screen. Revoking a phone
  stops it syncing; it does not wipe what is already on it.
- Briefs are as fresh as the last sync.
