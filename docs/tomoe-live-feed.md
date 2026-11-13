# Tomoe live feed (protocol v1)

How Musubi receives Tomoe's transcript while a meeting is in progress. Tomoe
serves it; Musubi only reads it. The protocol is the whole interface between
the two: no code is shared (Tomoe is GPLv3, Musubi proprietary).

## Transport

- Unix domain socket at `~/.local/share/tomoe/live.sock`, mode 0600 (only
  the user can connect).
- Enabled by Tomoe's `[meeting] live_feed = true` (off by default).
- Newline-delimited JSON, one message per line, server to client only.
- Any number of clients. Each has a bounded buffer; a client that falls
  behind is disconnected rather than ever slowing transcription. Clients
  reconnect and receive a fresh snapshot.

## Messages

Every message has `"v": 1` and a `type`.

```json
{"v":1,"type":"hello","app":"tomoe","version":"<build version>"}
```
First message on every connection.

```json
{"v":1,"type":"session","state":"started","session":{"id":"…","title":"…","platform":"Teams","created_at":"2026-10-10T09:00:00-04:00"}}
```
`state` is `started`, `stopped` (recording ended; final decoding may
continue) or `saved` (the session file is complete; Musubi ingests it then).

```json
{"v":1,"type":"segment","session_id":"…","segment":{ …Tomoe's session.Segment JSON… }}
```
Sent for every new line and every revision of a line, with the same `id`
(revisions replace earlier versions). `segment.status` says how final it is:

| status | meaning |
|---|---|
| `live` | pass-1 draft while the person is still speaking: low latency, lower accuracy, often lower case |
| `pending` | utterance finished, waiting for the final decode |
| `""` (empty) | final text |
| `removed` | withdrawn (merged into another line); drop it |

Speaker relabels (diarization, the meeting window's name) arrive as revised
segments too.

## Snapshot on connect

If a session is active when a client connects, the server sends `hello`,
the `session` `started` message, and then every current segment of that
session in order, before live messages.

## Musubi's recorder (2026-11-12)

Musubi's own recorder (`internal/recorder/livefeed`) serves the same
protocol at `live.sock` in its folder (`[recorder] data`, default
`~/.local/share/musubi/recorder`), when its `[meeting] live_feed` is on
(`[recorder.meeting] live_feed` in Musubi's config, or Tomoe's config
while the recorder reads it; its built-in default is off). Its `hello`
also says `"app":"tomoe"`. It adds `status`, `audio`, `diag` and `look`
messages, which readers that don't know them ignore
([recorder-screens.md](recorder-screens.md), "How Musubi talks to the
recorder"). Live follows the feed of the recorder of record (`[recorder]
of_record`); the app follows the recorder's feed for its state either
way.
