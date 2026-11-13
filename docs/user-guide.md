# Musubi user guide

Musubi records your meetings on your Mac, writes down what was said, and
keeps track of what each subject has come to: decisions, open questions,
who owes what. During a meeting it shows what it knows about the subjects
being discussed. Between meetings you can ask it questions, and it answers
with sources.

Everything stays on your Mac, except the text you choose to send to a
hosted model (see [Models](#models)).

The screenshots in this guide use invented demo data: Northwind and its
customers are made up.

- [Quickstart](#quickstart)
- [A tour of the app](#a-tour-of-the-app)
- [Who said what: speaker labels](#who-said-what-speaker-labels)
- [Models](#models), and [what to expect](#what-to-expect)
- [Diagnostics](#diagnostics)
- [Use it from Claude Code](#use-it-from-claude-code)
- [Privacy](#privacy)
- [Updating](#updating)
- [When something isn't working](#when-something-isnt-working)

## Quickstart

### 1. What you need

- A Mac with Apple silicon (M1 or later), macOS 14 or later.
- [Ollama](https://ollama.com), installed and opened once. Musubi starts
  it when needed and downloads the local models it uses (about 5.5 GB).
  16 GB of memory or more is best.
- Access to this repository, to download the app.

### 2. Install

1. Download the latest `Musubi-<version>.dmg` from
   [Releases](https://github.com/anielsud/musubi/releases).
2. Open it and drag **Musubi** to **Applications**.
3. Open Musubi from Applications. The app isn't signed by Apple yet, so
   the first time macOS says it can't check it:
   - click **Done**;
   - open **System Settings → Privacy & Security**, scroll down, and
     click **Open Anyway** next to the line about Musubi;
   - confirm with your password or Touch ID.

   You only do this once per version.

### 3. First launch

When Musubi opens for the first time it sets itself up:

- **The recorder.** Musubi installs its recorder, a background app
  (*Musubi Recorder*) that starts when you log in. On its first start it
  downloads its speech models (about 1 GB); **Status** shows the
  progress.
- **Permissions.** macOS asks to let *Musubi Recorder* use the
  **microphone** and **record the screen**. Allow both:
  - the microphone records you;
  - screen recording lets it hear the meeting app's audio (the other
    people) and read speakers' names from the meeting window.

  If you missed a prompt, turn the recorder on in **System Settings →
  Privacy & Security → Microphone** and **→ Screen Recording** (on
  macOS 15 and later, **Screen & System Audio Recording**).

If the red **Recorder off** button by the tabs stays red for more than a
few minutes, see [When something isn't working](#when-something-isnt-working).

### 4. Tell Musubi who you are

**Settings → Accounts → You**: your name and work email. Musubi uses them
to recognise you in calendar invites and transcripts, and to tell your
own commitments from other people's.

![Settings → Accounts](images/user-guide/settings-accounts.png)

### 5. Connect Microsoft 365

Still in **Settings → Accounts**, click **Connect to Microsoft 365**
and sign in with your work account in the browser window that opens.

The first sync reads the last **six weeks** of your calendar and the
Teams transcripts of the meetings you were in, recorded by Musubi or not.
After that it keeps up on its own. Musubi only reads: it never sends,
changes or deletes anything in Microsoft 365.

To bring in more history before the first sync, raise **First sync,
days back** first. After it, see [upgrades.md](upgrades.md#5-bring-in-older-history-and-rethread).

### 6. Your first meeting

Join a Teams or Zoom call as usual. Recording starts by itself when a
meeting app uses the microphone and speakers. To record something it
didn't notice, press **● Record** by the tabs; press it again to stop.

When the meeting ends, the recording is saved and Musubi reads it: a
summary, to-dos and subjects appear under **Meetings** within a few
minutes.

## A tour of the app

### Live

![Live](images/user-guide/live.png)

During a meeting, Live shows the conversation as it happens and briefs
you on the subjects it recognises: where each one stands and what is
still open. While it records, click a speaker's name to rename them.

At any time you can **ask a question** in the box. Answers start from
where each subject stands now, then the record itself, and every answer
links to its sources.

![An answer in Live](images/user-guide/ask.png)

The letters by the name show what's connected: **L**, the live feed from
the recorder; **M**, the local models. Green is connected.

### Meetings

![Meetings](images/user-guide/meetings.png)

Pick a day in the calendar, or filter by words or people. A meeting shows:

- a short summary, and the subjects it touched;
- what was said, subject by subject, with **where it was said**;
- to-dos from the meeting, and their state now;
- who spoke, with **rename** when a name is wrong;
- **Copy notes** and **Download transcript**.

**Prep for the next one** gathers what to bring to your next meeting:
its subjects, what moved since, and what is still open.

### To-Do

![To-Do](images/user-guide/todo.png)

What you agreed to do (**You owe**), what others agreed to do for you
(**Waiting on**), and **Everything**. Each to-do links to the meeting it
came from. Going through new ones, mark each **done** or **not a to-do**;
Musubi keeps your word over what it inferred.

### Explore

![Explore → Topics](images/user-guide/explore-topics.png)

- **Topics**: the subjects Musubi follows across meetings, email and
  chat, with the latest movement first. Open one to see its history and
  where it stands.
- **People**: the people you meet, with what you've discussed.
- **Voices**: the voice profiles the recorder has learnt, so it can name
  speakers the meeting window doesn't (turn on **Voice profiles** in
  Settings → Recording first). **Who's this?** lists voices it
  couldn't name for you to name; **Not them** removes a wrong sample.
- **Search**: everything, by words or meaning.
- **Corrections**: the changes you've made, each with undo.

![Explore → People](images/user-guide/explore-people.png)

### Status

![Status](images/user-guide/status.png)

Whether the recorder is running and recording, how much Musubi holds,
what it's working on, and its connections (Microsoft 365, models). When
something needs you, it says so here first.

### Settings

- **Accounts**: you, and Microsoft 365.
- **AI providers**: where models run: Ollama on this Mac, the recorder's
  speech models, and hosted providers such as the AI Gateway.
- **Models** and **Processing**: which model does each job.
- **Recording**: microphone, meeting audio, reading the meeting window,
  voices and transcription.
- **Dataset**: where your data is, its size, **Back up now**, and your
  backups, each with **Restore…**. Musubi also backs up on its own: once
  a day, and before anything that deletes or rewrites a lot (deleting a
  meeting, Catch up, Rethread).

![Settings → Recording](images/user-guide/settings-recording.png)

## Who said what: speaker labels

Musubi names speakers on your Mac, from the recording itself, rather
than taking them from the meeting platform:

- **Voices are told apart** as the meeting goes (regrouped every 10
  seconds) and settled when it ends.
- **The meeting window.** About once a second the recorder looks at the
  Teams or Zoom window: who is highlighted as speaking, and their name.
  It needs screen recording (and, for Zoom, Accessibility) permission,
  and the meeting window open, not minimised. A misread name is matched
  to the closest invitee.
- **Your microphone** is "you". With **Other voices on your
  microphone** on (the default), someone speaking beside you is told
  apart.
- **Voice profiles** (Settings → Recording → **Voice profiles**, off by
  default): Musubi learns each person's voice, as fingerprints (no
  audio kept), from moments their tile was lit or you named them, and
  then names them where the window can't: in a meeting room, on the
  phone, or with no window. On saved meetings it named 112 of 119
  voices, all correctly.
- **Meeting rooms** (Settings → Recording → **Split meeting rooms into
  their people**, off by default) separates the people sharing a room's
  microphone.

**Making labels better:**
- On a meeting's page, **rename** a wrong speaker; **split by voice…**
  when one label covers two people. Profiles learn from both.
- In **Explore → Voices**, work through **Who's this?** (voices nobody
  named in the last 90 days), **Same person?** and **Window or voice?**.
  On a profile, **Not them** removes a meeting that wasn't them, and
  **Junk** stops it learning from a bad stretch.
- Renaming a profile renames that person in past meetings too, without
  any model call, and can be undone.

Teams transcripts name speakers too, but Musubi doesn't use their names
on its recordings yet: it uses Teams transcripts to fill in meetings,
or parts of meetings, it didn't record.

## Models

Out of the box every model runs on your Mac with Ollama: nothing you
record leaves it. Local models are slower, and their summaries and
answers are simpler.

For better results, connect a hosted model:

1. **Settings → AI providers**: add the AI Gateway with your token (it
   needs both "AI Gateway Run" and "Workers AI Read"), or another
   provider with **Add a provider**: a name, an address and a key
   (OpenAI- or Anthropic-compatible). Keys go in your Keychain, never in
   a file.
2. **Settings → Processing → Use recommended**. It sets the models below
   and turns on what they're for: meetings read into subjects, meeting
   summaries with to-dos, and Live's briefs. Your other settings stay.

**Settings → Processing** lists each job, its model, a fallback used
when that model fails, and how hard it thinks. **Models** lists every
model with its price and what uses it, and downloads local ones.

| Job | Recommended | Without a hosted model |
|---|---|---|
| Reading meetings and linking them into subjects | Claude Haiku 5.5, through the AI Gateway (GLM-5.3-Flash as fallback) | local model, simpler output |
| Statuses, meeting summaries, Live briefs | Claude Haiku 5.5 | statuses on the local model; no summaries or briefs |
| Ask | Claude Haiku 5.5; Qwen3.8-27B on your Mac as fallback | qwen3:8b on your Mac |
| Sensitivity labels, Live relevance | Jev (a classifier) through the gateway | the local model's yes or no |
| Live suggestions, search | qwen3:8b and nomic-embed-text, on your Mac | the same |
| Transcription, speakers | on your Mac, in the recorder | the same |

The configuration behind this lives in `~/.config/musubi/config.toml`;
[setup.md](setup.md) has every key.

### What to expect

Measured on real meetings (dates and methods in
[research-log.md](research-log.md)):

| | |
|---|---|
| Cost, with the recommended models | about $0.06 a meeting hour: $0.04 to read it into subjects, $0.02 for statuses and summaries; under 1¢ a question (2026-11-11, not counting thinking) |
| Live | about 2–3.5 s from words said to a lookup on screen; suggestions start within 0.7 s (2026-10-29) |
| Ask | a few seconds with a hosted model; a minute or more on the local fallback |
| Transcription | against Tomoe on a live call: 3.6% of words different, the same speaker for 100% of words (2026-11-12) |
| The recorder | about 30% of one core and 2.5 GB of memory while recording (2026-11-12) |
| Local models | Ollama's qwen3:8b needs about 11 GB of memory while loaded; the optional 27B fallback about 16 GB |
| Disk | a year of meetings, about 1,300 of them: a 420 MB database, plus the recordings' audio |

## Diagnostics

- **Status** says first what needs you: the recorder's state and its
  health (each problem with its fix: a missing permission, a model to
  download), what Musubi holds, its work queue, and its connections.
- **⚙︎ (top right)** opens how Musubi is thinking: what it heard, what it
  looked up, each model call and how long it took, what the recorder saw
  of the meeting window, and **resources**: CPU and memory for Musubi,
  the recorder and Ollama.
- The letters by the name: **L**, the live feed from the recorder; **M**,
  local models. Green is connected.
- **Logs**: `~/Library/Logs/Musubi/musubi-gui.log` (the app) and
  `~/.local/share/musubi/recorder/logs/recorder.log` (the recorder).

## Use it from Claude Code

Claude Code can read what Musubi knows, with every result labelled by
who it may be shared with ([mcp-design.md](mcp-design.md)). Register it
once:

```sh
claude mcp add --scope user musubi -- /Applications/Musubi.app/Contents/MacOS/musubi mcp
```

Then ask Claude things like "what's open with Acme Freight?" or "prep me
for my 2pm". It can search, read threads, meetings and to-dos, and
prepare for a meeting; it can also record your corrections and to-do
marks, each with undo, when you ask it to. It reads the default
dataset; to use another, add `--config <path to its config.toml>` before
`mcp`.

## Privacy

- Recordings, transcripts and everything derived from them stay in
  `~/.local/share/musubi` on your Mac. Nothing is uploaded except the text
  sent to a hosted model you connected.
- Musubi labels what it learns by sensitivity: what is fine for anyone,
  for colleagues, only for the people who were there, or only for you
  (personal details).
- **◐ private** (top right) switches to screen-safe: while you share your
  screen, Musubi shows only what's fine to share.
- Delete a meeting from its page (**Delete meeting…**): it and everything
  learnt from it are removed and never read again. Backups made before
  it (`~/.local/share/musubi/backups`, also on your Mac) still hold it
  until they are removed: daily ones after a week, those made before a
  change after five more changes; those made by hand or before a restore
  stay until you delete them.

## Updating

Download the new DMG from
[Releases](https://github.com/anielsud/musubi/releases), quit Musubi, and
drag the new Musubi to Applications, replacing the old one. Don't do it
during a meeting: the recorder restarts with the new version.

Because the app isn't signed by Apple yet, after an update:

- confirm the first open again (**Open Anyway**, as in
  [Install](#2-install));
- macOS may ask again for the microphone and screen recording. If
  recordings come out silent, turn *Musubi Recorder* off and on again in
  **System Settings → Privacy & Security** under both.

Before updating, **Settings → Dataset → Back up now**. A new version
that changes how your data is stored also first copies your database to
`backups/` in the data folder. If you have been using an earlier Musubi,
or Tomoe, see [upgrades.md](upgrades.md).

## When something isn't working

| What you see | What to do |
|---|---|
| **Recorder off** stays red | Click it to start the recorder. On a new install, wait for the speech models to download (Status). If it still won't start, the reason shows when you hover over the button and on Status. |
| A meeting wasn't recorded | Check **Status → Recording** for the sources. If the other people are missing, allow screen recording for Musubi Recorder (Quickstart, step 3). |
| Speakers are named wrong | Rename them on the meeting's page; the recorder learns their voices. |
| **M** is grey | Ollama isn't running or isn't installed. Open Ollama once. |
| Microsoft 365 says the sign-in has lapsed | **Settings → Accounts → Sign in again**. |
| Meetings, subjects or voices are gone or wrong after a change | **Settings → Dataset → Backups**: **Restore…** the one from before it. What you have now is backed up first, so you can go back. |

Logs, if someone asks for them: `~/Library/Logs/Musubi/musubi-gui.log`
(the app) and `~/.local/share/musubi/recorder/logs/recorder.log` (the
recorder).

For setting Musubi up from source, the command line and configuration,
see [setup.md](setup.md).
