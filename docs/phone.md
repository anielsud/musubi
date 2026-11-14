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
- The phone's TLS key lives in the Android Keystore (not exportable);
  the paired Mac is a pinned certificate hash, not a secret.
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
  the macOS Keychain, and as repository secrets (`MUSUBI_ANDROID_KEYSTORE`,
  `MUSUBI_ANDROID_KEYSTORE_PASSWORD`); the owner keeps a backup. A tag
  `android-v<versionName>` runs `.github/workflows/android-release.yml`,
  which tests, builds and signs with `android/release.sh` and publishes to
  `musubi-releases` (`RELEASED_TOKEN`, as the Mac release). Locally,
  `android/release.sh` builds the same signed APK; `--publish` exists for
  emergencies only.

**Sync: the Mac dials the phone** (owner's decision of 2026-11-13, after
the first version met the Mac's firewall)

The work Mac's firewall refuses incoming connections to apps without an
Apple-issued signature, so the Mac cannot be the server. It makes only
outgoing connections, which the firewall does not filter; the phone is
the server. (Routing around the firewall through some other signed
program would sidestep the company's policy, so it is not an option.)

- **Listening**: the phone runs a TLS server on port 47616 only in
  windows: while the app is in the foreground; for up to 8 minutes after
  a recording stops; and for up to 3 minutes in a periodic WorkManager
  job (every 15 minutes, network connected; Android may space these out
  when the phone is idle). A window ends early once a paired Mac has
  finished a sync. Opening the app is the way to sync now.
- **Advertising**: during a window the phone registers `_musubi-phone._tcp`
  (Android NSD), instance `musubi-<first 8 hex digits of its certificate
  SHA-256>` (no device or owner name: it is also seen on other
  networks), TXT `v=2`, `fp=<first 16 hex digits>`, and `pair=1` only
  while the owner has pairing open.
- **Accepting**: only connections from private addresses (RFC 1918,
  100.64.0.0/10 and fd7a:115c:a1e0::/48 for Tailscale, link-local,
  loopback). Mutual TLS: outside pairing, a client must present the
  paired Mac's certificate (pinned SHA-256); anything else fails the
  handshake. At most one connection at a time.
- **Phone certificate**: self-signed ECDSA P-256, made on first use, its
  key generated in and never leaving the Android Keystore.
- **Addresses for Tailscale**: the phone reports its current addresses
  (including a Tailscale 100.x / fd7a: address when it has one) at every
  sync; the Mac dials those when it doesn't see the phone on the local
  network. That works only when the Mac runs Tailscale too.
- **Recordings**: the phone serves finished recordings' `meta.json` and
  chunks; the Mac fetches what it lacks and reports each recording's
  state back. The phone deletes a recording's audio only when the Mac
  reports it `imported`, and keeps `meta.json` 30 days.
- **Agenda**: the Mac pushes the agenda (events and briefs) on every
  sync; the phone replaces its copy and drops events ended more than a
  day ago.
- **Mac screen** on the phone: paired Mac's name, last sync and what it
  did, **Pair with your Mac**, **Unpair**. No addresses to type.

**Pairing, confirmed on both** (numeric comparison, as Bluetooth does)
1. On the phone the owner taps **Pair with your Mac**: a window opens for
   2 minutes and the advertisement carries `pair=1`.
2. The Mac, with phone sync on, sees `pair=1`, connects (presenting its
   certificate; during the pairing window the phone accepts any client
   certificate, for the pairing ops only) and sends `pair_request`
   `{mac_name, nonce}` (16 random bytes); the phone answers
   `{device_name, nonce}` (16 of its own).
3. Both compute the code: the first 4 bytes of SHA-256(`"musubi-pair-v2"`
   ‖ Mac certificate SHA-256 (32 bytes) ‖ phone certificate SHA-256 (32
   bytes) ‖ Mac nonce ‖ phone nonce) as a big-endian unsigned number, mod
   1,000,000, zero-padded to 6 digits. Each side takes both certificates
   from the TLS session itself, so a machine in the middle makes the
   codes differ.
   Test vector: Mac certificate SHA-256 = bytes `00 01 … 1f`, phone
   certificate SHA-256 = `20 … 3f`, Mac nonce = `40 … 4f`, phone nonce =
   `50 … 5f` → SHA-256 starts `c1d59190`, code **999120**.
4. Musubi brings its window forward: "Pair <device name>? Code 123 456",
   **Allow** / **Deny**. The phone shows the code with **Confirm** /
   **Cancel**. After Allow the Mac sends `pair_confirm` and waits (up to
   2 minutes) on the same connection; the phone answers `{ok: true}` only
   once its owner tapped Confirm, otherwise `{ok: false, error:
   "cancelled" | "expired"}`. Deny sends `pair_cancel`.
5. On success the Mac pins the phone's certificate (and stores its name,
   addresses, dates); the phone pins the Mac's and its name. There are
   no tokens.
- The Mac shows one prompt at a time; after 3 denials of the same phone
  certificate in 10 minutes it ignores that phone for 10 minutes.
- Unpair on either side removes that side's pin; the other side's
  connections then fail and it says so.

**Protocol** (over the mutual-TLS connection; TLS 1.2 or 1.3, ECDHE with
AES-GCM or ChaCha20). The Mac sends requests one at a time; the phone
answers each. Every message is a 4-byte big-endian length, that many
bytes of UTF-8 JSON (at most 64 KB), then, if the JSON has `"body": n`,
n raw bytes (at most 64 MB). Requests are `{"op": …, …}`; replies are
`{"ok": true, …}` or `{"ok": false, "error": "<short code>"}`. Unknown
ops, oversize frames and malformed JSON close the connection.

| op | request → reply |
|---|---|
| `hello` | `{v: 2, mac_name}` → `{v: 2, device_name, app_version, addresses: [ip…]}` |
| `list` | → `{recordings: [{meta: <meta.json>, state: <last state the Mac reported> \| null}], too_large: [rid…]}` (finished recordings only; as many as fit the frame, the rest on a later sync; a recording whose `meta.json` alone can't fit any frame is skipped and named in `too_large` (at most 20), which the Mac logs, so it never holds up the others) |
| `chunk` | `{rid, n}` → `{sha256}` + body (the chunk's bytes) |
| `state` | `{rid, state: received \| waiting \| transcribing \| imported \| failed, session_id?, error?}` → `{}` |
| `agenda` | `{body: n}` + the agenda JSON (the shape below) → `{}` |
| `bye` | → `{}`; the phone closes its window if nothing else is pending |
| `pair_request`, `pair_confirm`, `pair_cancel` | as in Pairing |

Agenda JSON: `{events: [{id, start, end, subject, location, organizer,
attendees: [{name}], brief: {text, made_at, sensitivity: {level, share,
why}, sources: [source]} | null, open: [{text, who, due, thread, raised,
quiet, overdue, private, sources: [source]}]}], drafting: n, made_at}`,
where `source` is `{n, title, kind, occurred_at, speakers, text,
sensitivity}` (owner's decision of 2026-11-13: citations either link to
what backs them or are left out).
- `brief.text` cites `[n]`, the `n` of a source in `brief.sources` (the
  same numbering as the desktop's Answer). `open` are the "open between
  us" items the desktop's prep shows, in its order; their text has the
  desktop's item citations removed, and their `sources` (at most 3,
  numbered from 1) are the passages behind them.
- A source's `text` is the cited passage, at most 1,200 characters; each
  carries its own sensitivity label. At most 30 sources per brief and 15
  open items per event.
- The phone renders `**bold**`, bullet lists (`-`/`*`, nested by two
  spaces) and paragraphs, shows `[n]` as a small tappable mark that opens
  the source (title, date, speakers, passage, label), and drops a `[n]`
  with no matching source.

`rid` is the phone's UUID, `n` 0–9999. The phone refuses `chunk` and
`state` for unknown recordings.

**Recording details** (owner's decision of 2026-11-13: attach to the
meeting)
- On the Recording screen and on any recording afterwards: **Notes**
  (free text, at most 20,000 characters, editable any time), **People**
  (picked from the event's attendees, or typed; at most 50), **Photos**
  (camera, or the system photo picker; JPEG, at most 15 MB each, at
  most 30; stored in the recording's folder, without location: GPS tags
  and XMP are removed when a photo is saved there, orientation kept).
- `meta.json` gains `details: {rev, attendees: [{name, email?}], notes:
  {size, sha256, updated}, photos: [{name, size, sha256, taken}]}`; `rev`
  goes up with every edit. `version` becomes 2. The notes' text is
  `notes.txt` in the recording's folder (UTF-8, at most 20,000
  characters), so a long note never makes `meta.json` (and `list`) large.
- New op `file` `{rid, name}` → `{sha256}` + body: `notes.txt` (when
  `details.notes.size` > 0) or a photo listed in `details.photos`; each
  checked against the size and SHA-256 `meta.json` lists.
- `state` gains `details_rev`: the `rev` the Mac has applied. `list` also
  includes an imported recording whose `details.rev` is above the
  `details_rev` the Mac last reported (and only its `meta`, no chunks
  fetched again).
- The phone deletes a recording's photos together with its audio, once
  the Mac has reported `imported` and the current `details_rev`.
- On the Mac the details go into the session folder (`phone-details.json`,
  `photos/`) and onto the meeting: notes are the owner's notes on that
  meeting (shown and searchable, the meeting's sensitivity); attendees
  are the meeting's people and invitees for speaker naming; photos are
  kept with the meeting, labelled owner-only, and never sent to a hosted
  model. A later `rev` replaces the earlier details.
- How (2026-11-13): sync applies details once the recorder has the
  meeting saved, then reports `details_rev`; the session's
  `phone-details.json` (`writer: "musubi"`, the notes' text in it) is read
  by ingest as one record with `session.json` (the photo files are never
  read), so a new `rev` is a new raw version of the meeting. The notes become the meeting's last segments,
  the owner's, marked as notes (passages of their own, under the meeting's
  label); the people its attendees (after its speakers) and invitees in
  `ReadCalendarInvitees`; the photos stay out of the item entirely and
  are shown on the meeting page, in the owner's scope only.
  `[phone] details = false` restores the earlier behaviour.

### 2. The Mac side

**Phone sync** (in the Musubi app, `internal/phone`): off until the owner
turns on **Settings → Phone** (`[phone] enabled`). The Mac listens on no
port.

- **Finding phones**: while on, the Mac browses `_musubi-phone._tcp`
  (Bonjour, the system `dns-sd`), resolves each phone it sees and dials
  it if it is paired (by the advertised `fp` and then the pinned
  certificate) or advertising `pair=1`. It also dials each paired
  phone's last reported addresses every 10 minutes when it hasn't seen
  it on the local network. It dials private addresses only.
- **Certificate**: the Mac's self-signed ECDSA P-256 certificate (made on
  first use, kept in the Musubi data folder, key 0600) is its client
  certificate; it pins each phone's certificate at pairing and refuses
  any other.
- **A sync**: `hello` → `list` → for each recording the Mac lacks or
  hasn't finished: fetch missing chunks (`chunk`, checked against
  `meta.json`'s SHA-256 and size), join them into the recorder's inbox
  and ask the recorder to import it (control op `import`) → `state` for
  every recording whose state changed → `agenda` → `bye`. Chunks are
  kept in a staging folder until the import is done, so an interrupted
  sync resumes.
- **Agenda**: owner scope, the next 10 days (at most 14); each sync also
  starts drafting missing briefs in that window in the background (the
  same drafting as the weekly pass, not during a live meeting), so the
  next sync carries them.
- **Devices**: **Settings → Phone** lists paired phones (name, last
  sync, last addresses) with Unpair, shows whether the Mac is looking
  for phones, and the last sync's outcome.
- Pairing, unpairing, syncs and failures are logged without nonces,
  codes or certificate keys.

**Import**: the recorder imports a joined recording only when it isn't
recording a live meeting (state `waiting` until then).

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

### 3. Explore on the phone (owner's decision of 2026-11-13: reuse the
desktop screens; read and Ask only)

While the app is in the foreground on a network where the Mac can reach
it, the phone shows an **Explore** tab: Musubi's own Explore screens and
Ask, served by the Mac over the connection the Mac opened.

- The phone's advertisement adds `live=1` while the app is in the
  foreground. The Mac dials such a paired phone at once (after a sync if
  one is due) and sends `live_start`; the phone answers `{}` and the
  connection carries both directions from then on.
- From `live_start` every frame has an `id` (the sender's counter; a
  reply repeats it). Phone → Mac: `rpc {id, method, args: [...]}` →
  `{id, ok, result}`; `asset {id, path}` → `{id, ok, type}` + body.
  Mac → phone: `event {name, data}` (no reply) for the frontend's events
  (Ask's streaming, for example); `ping` → `{}` every 30 seconds. Phone →
  Mac `live_end` when the app leaves the foreground; either side may
  close. One live connection per phone; closed after 10 minutes without
  a request.
- **Allowlist** on the Mac: only the read methods the Explore screens
  call, plus `Ask`/`StopAsk`; every other method (settings, edits,
  judgements, to-do marks, recorder control, diagnostics) is refused
  `not_allowed`. Scopes as on the desktop (`owner`, `shared-screen`).
  Arguments are checked the way the desktop's calls are.
- **Assets**: the Mac serves its own built frontend (the files embedded
  in the app) by path, nothing else, so the screens always match the
  Mac's version.
- On the phone: a WebView on a private origin (`https://musubi.local/`)
  whose requests are answered from the live connection; a bridge object
  that stands in for the desktop's `window.go.gui.App` and
  `window.runtime` (events), present only on that origin; no other
  navigation, no file access, nothing cached to disk. The frontend opens
  on Explore in a phone mode (`?phone=1`: no desktop-only chrome, narrow
  layout).

## Trade-offs

- No hints on the phone, and its live text is a preview from a small
  model: the Mac's transcript is the record.
- Updates come from a public repo: the APK can be downloaded and read by
  anyone (no data or keys in it), as the Mac installers already can.
- The phone, not the Mac, accepts connections: in short windows, from
  private addresses, mutual TLS with the pinned Mac only. Its
  advertisement shows others on the same network (also on a conference
  or hotel network) that a Musubi phone is there, without a name.
- Sync in the background is as prompt as Android's job scheduling (about
  every 15 minutes, less when idle); opening the app syncs at once.
- Over Tailscale only when the Mac runs Tailscale too.
- Audio and briefs sit on the phone until the next sync. Protection is
  Android's storage encryption and the lock screen. Revoking a phone
  stops it syncing; it does not wipe what is already on it.
- Briefs are as fresh as the last sync.
