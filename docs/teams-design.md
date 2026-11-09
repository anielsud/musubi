# Teams transcripts and calendar through Microsoft Graph

Status: **proposed** (2026-11-09). Nothing is built. The probe described
below was run once by hand.

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
- Which meetings return 3003 is not yet known. It may be large
  organisation-wide calls.

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

### 1. Fetch with Microsoft's tool; Musubi reads the inbox

A PowerShell script, `tools/teams/teams-pull.ps1`, uses the Graph module to:

- list calendar events since the last run;
- for each Teams meeting, find its transcripts;
- write each new transcript as one inbox drop,
  `<inbox>/teams/<transcript id>.json`.

The drop format already exists (`internal/item/drop.go`, `dropMeeting`), as
does its reader (`internal/source/mcpdrop`):

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

Musubi ingests the drop as it does any other. Duplicates follow the existing
rules:

- `prefer_source = ["tomoe", "bluedot", "teams"]`;
- a Tomoe copy with less than 80% of the other copy's words gives way to
  it.

So a Teams transcript fills a gap and otherwise stays the second copy.

Why a script around Microsoft's module rather than Musubi signing in itself
with the same app id:

- The tool is what IT consented to. Musubi presenting Microsoft's client id
  from its own code would pass Entra's checks too, but it would be
  impersonating Microsoft's tool.
- If IT later approves an app registration of Musubi's own, the script is
  replaced by the planned `graph` connector. Nothing else changes.

### 2. What is fetched

| Setting | Default | Fetches |
|---|---|---|
| `teams.enabled` | false | nothing until turned on |
| `teams.gaps` | true | transcripts of meetings with no complete Tomoe copy |
| `teams.unattended` | false | also meetings the owner did not join |
| `teams.calendar` | true | calendar events as `event` drops (titles, attendees) for calendar linking |
| `teams.since` | 14 days | how far back the first run looks |

`teams.unattended` is off by default. Meetings the owner skipped are many,
often large, and of mixed value. When turned on, they come in labelled by
their invitee list like any meeting, and the digest and thread stages treat
them like any other meeting.

Mail and chat are in the same token (`Mail.Read`, `Chat.Read`). They unblock
the email and chat pilot in the backfill queue, but are not part of this
design.

### 3. When it runs

- Every 30 minutes on weekdays, from a launch agent, while the Mac is awake.
  A transcript appears a few minutes after a meeting ends.
- After Musubi has been idle, never while Tomoe records (the same check the
  installers use).
- It runs in the background at low priority. It is network-bound, and
  transcripts are small.

## Security

1. **Read only.**
   - The script calls only `GET` on an allow-list of paths: `/me/calendarView`,
     `/me/onlineMeetings`, `…/transcripts`, `…/content`.
   - Anything else is refused before the request is made.
   - A test asserts the allow-list.
2. **The token.**
   - The Graph module keeps its MSAL cache under the user account. Where and
     how it is protected on macOS (Keychain or a file) has to be verified
     before this is turned on.
   - If it is a file, the script runs with `-ContextScope Process` and a
     Keychain-held refresh token instead, or the design stops here.
   - The owner can revoke it at any time with `Disconnect-MgGraph` or from
     the account's sign-in sessions.
3. **No secrets in Musubi.** Musubi never sees a token, only the files the
   script writes.
4. **Logging.** The script logs counts and errors, never text. Its inbox
   files are raw data, under the same rules as other raw.
5. **Visibility.** Sign-ins show in Entra as "Microsoft Graph Command Line
   Tools" from the owner's account. A note to IT that this is how it is
   used is worth sending; the owner's call.

## Config

```toml
[teams]
enabled    = false
gaps       = true
unattended = false
calendar   = true
since      = "336h"
```

Turning `enabled` off restores today's behaviour exactly: the script isn't
run, and drops already ingested stay.

## Open questions

1. Where the Graph module's token cache lives on macOS, and whether it is
   protected (blocks turning it on).
2. Which meetings return 3003, and whether they matter.
3. Whether `teams.unattended` should start on for a small set (named
   series) rather than all or nothing.
4. Speaker names: Teams names every speaker. Tomoe could use a Teams copy
   to correct its own speaker labels after the meeting. That is Tomoe's
   job, and a later design.

## Tests (when built)

- VTT → turns: merged cues, multi-line cues, speaker-less cues (unknown).
- Recurring series: each transcript becomes its own drop, dated by its own
  times.
- Duplicate with a complete Tomoe copy: Tomoe is kept, and Teams is
  retired. With a partial Tomoe copy (< 80%), Teams is kept.
- The allow-list refuses any non-GET call and any path outside it.
