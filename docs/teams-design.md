# Teams transcripts and calendar through Microsoft Graph

Status: **approved by the owner and being built** (2026-11-09).
- Built:
  - the connection (Settings, CLI), the calendar, and meeting context for
    Tomoe;
  - Teams transcripts (both routes) and meeting chats (internal/m365).
- Next: people profiles.

The probe described below was run once by hand.

## Why

Tomoe records the meetings the owner is in. Teams adds two things it can't:

- a transcript of a meeting Tomoe missed or recorded only in part (the
  14 Oct crash; the 28 Oct weekly meeting);
- transcripts of meetings the owner was invited to but did not join.

The calendar comes from the same place, so it also gives meetings their
titles and attendees.

Until now this came from tools that are being retired: Bluedot, and a script
that copied the Teams web client's tokens out of the browser. The
architecture planned a `graph` connector "once an Entra app registration is
approved".

## Constraints (owner, 2026-11-09)

- Use only what IT has already enabled. **No new app registration.**
- **Don't depend on Claude.** The claude.ai Microsoft 365 connector would
  work, but it is not to be used for this.

## What the probe found

Microsoft's own **Microsoft Graph Command Line Tools** app is already
consented in the tenant, the app behind the Graph PowerShell module
(`Microsoft.Graph.Authentication`). The owner signed in once, interactively.
The token held these delegated scopes, among many others:

- `Calendars.Read`;
- `OnlineMeetings.Read`;
- `OnlineMeetingTranscript.Read.All`;
- `Mail.Read`, `Chat.Read`.

For one week (2–6 Nov):

| | |
|---|---|
| calendar events | 38 |
| with a Teams join link | 35 |
| meeting found and transcripts listed | 26 |
| with at least one transcript | 11 |
| "user does not have access to lookup meeting" (3003) | 7 |
| meeting not found (3004) / unknown error | 1 / 1 |

- A recurring series lists the transcripts of all its past occurrences, as
  many as 20.
- A sample transcript came back as WebVTT: 46k characters, 9 named speakers.
- 3003 comes from looking a meeting up by its join URL when the owner is
  invited only through a distribution list (one case checked: a 340-invitee
  company call).

**Meetings the owner did not join (tested 2026-11-09 on three known
cases).** Graph returns them as well; attendance doesn't matter, the
invitation does.
- Invited by name: `GET /me/onlineMeetings/{id}/transcripts` lists every
  occurrence's transcript, missed ones included. Two such transcripts came
  back whole: 416 turns and 16 speakers, and 398 turns and 11 speakers.
  The owner speaks 5 times in the first and not at all in the second. The
  meeting id can be built without a lookup: base64 of
  `1*<organizer object id>*0**<chat thread id>`, both of which are in the
  join URL.
- Invited through a distribution list: the lookup is refused, but the
  meeting chat is readable (`Chat.Read`).
  - Its `callRecordingEventMessageDetail` gives the recording's sharing URL.
    `/shares/{encoded}/driveItem` opens it (`Files.Read.All`).
  - SharePoint's own API, with a token for the SharePoint host from the
    same Microsoft app, lists the transcript attached to the recording:
    `/_api/v2.1/drives/{d}/items/{i}/media/transcripts`.
  - `…/streamContent?format=json` returns entries with
    `speakerDisplayName`: 136 entries, 10 speakers. `format=text/vtt` is
    refused, and the `temporaryDownloadUrl` gives the caption track
    without speakers.
- Fallback for a recording with no transcript: download it and transcribe
  it locally with Tomoe. The text is complete, but there are no names.

The calls:

- `GET /me/calendarView` gives events with `onlineMeeting.joinUrl`.
- `GET /me/onlineMeetings?$filter=JoinWebUrl eq '<url>'` gives the meeting.
  The filter value must be URL-encoded; unencoded, every call fails with
  400.
- `GET /me/onlineMeetings/{id}/transcripts` lists the transcripts.
- `GET …/transcripts/{tid}/content?$format=text/vtt` returns the text.

**The token is broad.** Entra issues every scope ever consented to this
app, not only the ones asked for. That includes directory, user and
SharePoint write scopes. A stored refresh token for this app is therefore a
powerful credential, even though Musubi only needs to read.

- The probe signed out afterwards (`Disconnect-MgGraph`).
- How it is held is the main design question, covered under "Security".

## Approach

### 1. Musubi signs in; what it reads goes through the inbox

Musubi signs in itself (`internal/m365`). It uses the standard browser flow
(authorization code with PKCE and a loopback redirect) with the Graph
Command Line Tools client id, the same flow the PowerShell module runs.
There is nothing to install. Entra records the sign-in exactly as it
records one from the module.

The probe's first plan was a PowerShell script around Microsoft's module.
The owner asked for a Connect button that installs whatever it needs, and
for an automatic sign-in again when a sign-in lapses. Signing in natively
does both without installing PowerShell and the module, and keeps one
Keychain entry under Musubi's control.

The cost: Musubi's own code presents Microsoft's client id. Entra can't
tell the two apart, and IT consented to that client. If IT wants only
Microsoft's own module to use it, the fallback is the module route, with
the same inbox files. (Owner's decision, 2026-11-09.)

Everything read is written as inbox drops and ingested as any other drop.
The drop format already exists (`internal/item/drop.go`), as does its reader
(`internal/source/mcpdrop`).
- Calendar events: `inbox/m365-calendar/<hash of event id>.json`, kind
  `event`. Besides subject, organizer, invitees and times, they carry
  `joinUrl`, `iCalUId` and the owner's `response`.
- Transcripts: `inbox/teams/t-<hash of transcript id>.json`, kind `meeting`,
  source `teams`, ref `teams-<hash>`:

```json
{"kind": "meeting", "source": "teams", "ref": "<transcript id>",
 "record": {"title": "...", "start": "...", "end": "...",
            "attendees": [{"name": "...", "email": "..."}],
            "turns": [{"speaker": "...", "email": "", "offset": 31.9, "text": "..."}]}}
```

- `start` and `end` are the transcript's own times (`createdDateTime`,
  `endDateTime`), not the calendar's, so a meeting that ran over or started
  late is placed where it happened.
- Turns come from the VTT cues: `<v Name>` is the speaker, and the cue start
  is the offset.
- `ref` is the transcript id, which is stable. A file already in the inbox
  or in raw is never fetched again.

Duplicates follow the existing rules:
- `prefer_source = ["tomoe", "bluedot", "teams"]`;
- a Tomoe copy with less than 80% of the other copy's words gives way to
  it.

So a Teams transcript fills a gap and otherwise stays the second copy.

### Connect, sign out, sign in again

- **Settings → Microsoft 365** shows who is connected, the last sync and
  what it read, with these buttons:
  - **Connect to Microsoft 365**, which opens the sign-in in the browser;
  - **Sync now**;
  - **Sign out**, which deletes the Keychain entry. What was read stays.
- The CLI has the same: `musubi m365 connect|status|sync|signout`.
- **Every update syncs first** (calendar, then later transcripts), then
  ingests.
- **When a sync finds the sign-in lapsed** (Entra answers `invalid_grant`
  or `interaction_required`), Settings says so. With `m365.reconnect`,
  the app opens the sign-in page again by itself:
  - never while Tomoe is recording;
  - at most every six hours unless the owner presses the button.
- **How often a sign-in is needed** is set by the tenant's policy, not by
  Musubi. The refresh token rotates on each use and is kept, so an active
  connection stays signed in as long as the policy allows (Entra's default
  is 90 days of inactivity).

### Calendar linking and meeting names

- Each update reads the calendar from 30 days back to 14 days ahead.
- An occurrence deleted since the last read is rewritten as cancelled, so
  its link is dropped.
- When invitations overlap, linking now breaks ties:
  1. the event the recording's own title names;
  2. otherwise the one the owner organized or accepted over one left
     unanswered (or declined).
- A recording whose title names a different meeting (an ad-hoc call during
  a large event) isn't linked to that event.
- `m365.tomoe_context` writes `calendar.json` into the Tomoe session folder
  of every linked recording (writer `musubi`, subject, times, join link,
  organizer, invitees). Tomoe reads it to show the meeting's name (Tomoe
  PR), and later to know who could be speaking. Musubi removes only files
  it wrote, and never touches Tomoe's own files.

### 2. What is fetched

| Setting (`[m365]`) | Default | Does |
|---|---|---|
| `enabled` | true | sync once connected; false never syncs, even when connected |
| `calendar` | true | calendar events as `event` drops |
| `past_days`, `future_days` | 30, 14 | the calendar window |
| `reconnect` | true | open the sign-in again when it lapses |
| `tomoe_context` | true | `calendar.json` next to linked Tomoe recordings |
| `transcripts` | true | Teams transcripts of the owner's past meetings in the window |
| `unattended` | false | also meetings the owner did not join (else read once and passed over) |
| `meeting_chat` | true | what was written in a meeting's chat during it, as a chat drop |

`unattended` is off by default (the data is reachable: see above). Meetings the owner skipped are many,
often large, and of mixed value. When turned on, they come in labelled by
their invitee list like any meeting, and the digest and thread stages treat
them like any other meeting.

Mail and chat are in the same token (`Mail.Read`, `Chat.Read`). They unblock
the email and chat pilot in the backfill queue, but are not part of this
design.

### 3. When it runs

On every update: the app's update every 30 minutes, the update after Tomoe
saves a meeting, Sync now, and `musubi update`. It is network-bound and
small; an unchanged event costs nothing (its file isn't rewritten).

## Security

1. **Read only.**
   - The client (`m365.Allowed`) makes only `GET`s, on an allow-list:
     - `/me`, `/me/calendarView`, `/me/onlineMeetings`, `…/transcripts`,
       `…/content`;
     - a meeting chat's messages, a shared item's driveItem, and one user,
       their manager or their photo;
     - SharePoint's `media/transcripts` on a recording.
   - Anything else is refused before the request is made.
   - A test asserts the allow-list.
2. **The token.**
   - Only the refresh token and the account name are kept: one generic
     password in the login Keychain (`Musubi.M365.v2`).
   - It is written and read through Apple's `security` tool, the way
     Musubi's other keys are kept, so the item's access list names that
     tool and the app and CLI read it without a prompt across rebuilds.
   - The first build wrote it through the Security framework. The item
     then named the creating binary, and the locally signed app was asked
     on every launch.
   - Access tokens stay in memory.
   - A Graph token and a SharePoint token are minted from the same refresh
     token, so the SharePoint step needs no second sign-in.
   - Sign out deletes the entry. The owner's Microsoft account sign-in
     sessions page ends the session at Microsoft's end.
3. **No secrets elsewhere.** No token is logged, written to the inbox, or
   shown in the app.
4. **Logging.** The sync reports counts and errors, never text. Inbox files
   are written 0600 and are raw data, under the same rules as other raw.
5. **Visibility.** Sign-ins show in Entra as "Microsoft Graph Command Line
   Tools" from the owner's account. A note to IT that this is how it is
   used is worth sending; the owner's call.

## Config

```toml
[m365]
enabled       = true
calendar      = true
past_days     = 30
future_days   = 14
reconnect     = true
tomoe_context = true
```

Until the owner connects, nothing changes. `enabled = false` never syncs,
even when connected.

## Open questions

1. ~~Where the token is cached.~~ Resolved: Musubi keeps it in the Keychain
   (see "Security"). One refresh token mints both Graph and SharePoint
   tokens, so there is one sign-in, not one per call.
2. Whether the SharePoint step (distribution-list meetings) is worth its
   second token, or those meetings wait for the recording fallback.
3. Whether `teams.unattended` should start on for a small set (named
   series) rather than all or nothing.
4. Speaker names: Teams names every speaker. Tomoe could use a Teams copy
   to correct its own speaker labels after the meeting. That is Tomoe's
   job, and a later design.

### Transcripts: how a meeting is read

For each past, not-cancelled, not-declined calendar occurrence with a join
link:

1. **Look the meeting up** by its join link
   (`/me/onlineMeetings?$filter=JoinWebUrl eq …`).
   - If Graph refuses, build the meeting id from the link:
     `base64("1*<organizer>*0**<thread>")`.
   - A series lists the transcripts of all its occurrences once. Each
     occurrence takes those that started from an hour before it to three
     hours after.
2. **Read each new transcript** as VTT and turn it into speaker turns.
   - Keep it if the owner attended: a Tomoe recording covers five minutes
     of it, or the owner is among the speakers.
   - Otherwise keep it only with `unattended`. A transcript passed over is
     put on `inbox/teams/.skipped` (not a `.json` file, so the inbox reader
     ignores it) and isn't fetched again. `m365.ClearSkipped` resets the
     list after turning `unattended` on.
3. **Invited through a list:** when Graph refuses both lookups and the owner
   attended (or `unattended`):
   - the meeting chat's recording event names the recording;
   - `/shares/…/driveItem` opens it;
   - SharePoint's `media/transcripts` → `streamContent?format=json` gives
     the entries with speaker names.
4. **Meeting chat** (`meeting_chat`): the chat's messages from an hour
   before to an hour after, people's messages only, HTML stripped. They're
   written as one chat drop per occurrence. Read only for meetings the
   owner attended, or with `unattended`.

First run on all-t4 (2026-11-09), 30 days back:
- 97 meetings: 28 transcripts kept, 3 passed over (not attended), 45
  meeting chats.
- Of the 28: 2 meetings had no Tomoe copy and 3 had a shorter one (the
  gaps). The other 23 are second copies with every speaker named, input
  for the speaker work.

## Tests

- VTT → turns: merged cues, multi-line cues, speaker-less cues (unknown).
- Recurring series: each transcript becomes its own drop, dated by its own
  times.
- Duplicate with a complete Tomoe copy: Tomoe is kept, and Teams is
  retired. With a partial Tomoe copy (< 80%), Teams is kept.
- The allow-list refuses any non-GET call and any path outside it, before
  sending (built).
- Sign-in with PKCE against a fake Entra, survival across restarts, refresh
  token rotation, SharePoint token from the same sign-in, sign out, and a
  lapsed sign-in reported as ErrAuthRequired (built).
- Calendar: two pages, rooms left out, emails lower-cased, unchanged events
  not rewritten, deleted occurrences cancelled, files 0600 (built).
- Linking: title first, then the owner's response; an ad-hoc call titled
  for someone else isn't linked (built).
- Transcripts:
  - VTT → turns (merged cues, multi-line cues, speaker-less cues);
  - SharePoint JSON → turns;
  - join link parts;
  - invited by name: attended kept, unattended passed over and not
    refetched, kept once `unattended` is on;
  - invited through a list: nothing read from SharePoint unless attended or
    `unattended`, then the recording's transcript;
  - meeting chat: messages during the meeting only, HTML stripped
    (built).
- Keychain round trip of a 10 KB value (`-tags keychain`, writes to the real
  Keychain; built).
