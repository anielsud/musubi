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
- `MediaRecorder`, AAC in ADTS (`OutputFormat.AAC_ADTS`), 48 kHz mono,
  96 kbps, source `VOICE_RECOGNITION` (falls back to `MIC`). A new file
  every 5 minutes (`setNextOutputFile`), so a crash or a dead battery
  loses at most the last seconds: ADTS is readable up to the last frame.
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
- **Pairing**: the owner presses **Pair a phone**; Musubi shows the
  addresses, an 8-digit code (valid 5 minutes, single use) and the
  certificate fingerprint's first 16 hex digits. The phone shows the
  fingerprint it sees; the owner checks they match and enters the code.
  Five wrong codes end the pairing window.
- **Devices**: pairing issues a 32-byte random token. The Mac stores only
  its SHA-256 and the device's name and dates. **Settings → Phone** lists
  devices and revokes them. Tokens travel in `Authorization: Bearer`,
  never in a URL. Every response is `Cache-Control: no-store`.
- Pairing, revocation, upload completion and failures are logged without
  tokens or codes.

**API** (JSON unless noted; every call but `pair` needs the token)

| Call | Body → reply |
|---|---|
| `POST /v1/pair` | `{code, device_name}` → `{device_id, token}` |
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
meeting (speaker labels, voice profiles). The session is marked
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

- No live transcript or hints on the phone. Accepted: no Mac on site.
- The Mac gains a network listener, off by default, private addresses
  only, paired devices only. It is the one new attack surface.
- Audio and briefs sit on the phone until the next sync. Protection is
  Android's storage encryption and the lock screen. Revoking a phone
  stops it syncing; it does not wipe what is already on it.
- Briefs are as fresh as the last sync.
