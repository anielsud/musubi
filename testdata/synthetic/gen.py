#!/usr/bin/env python3
"""Generates the synthetic eval corpus: a fictional company (Northwind Labs)
whose meetings, mail, chats and calendar exercise each scenario in
cases.toml. Every name here is invented.

Run from this directory: python3 gen.py
Outputs tomoe/<id>/session.json and inbox/<source>/*.json.
"""
import json, os, shutil, uuid
from datetime import datetime, timedelta, timezone

TZ = timezone(timedelta(hours=-4))
D = "northwind.example"
P = {
    "jordan": ("Jordan Pike", f"jordan.pike@{D}"),
    "dana": ("Dana Reyes", f"dana.reyes@{D}"),
    "priya": ("Priya Natarajan", f"priya.natarajan@{D}"),
    "lee": ("Lee Chen", f"lee.chen@{D}"),
    "samo": ("Sam Okafor", f"sam.okafor@{D}"),
    "samw": ("Sam Whitfield", f"sam.whitfield@{D}"),
    "marta": ("Marta Kowalski", f"marta.kowalski@{D}"),
    "olu": ("Olu Adeyemi", f"olu.adeyemi@{D}"),
    "will": ("Will Harper", f"will.harper@{D}"),
    "tom": ("Tom Becker", "tom.becker@acmefreight.example"),
    "nina": ("Nina Patel", "nina.patel@bluegill.example"),
}
ALL = f"all@{D}"

here = os.path.dirname(os.path.abspath(__file__))
for d in ("tomoe", "inbox"):
    shutil.rmtree(os.path.join(here, d), ignore_errors=True)


def at(day, hh, mm=0):
    return datetime(2026, 9, day, hh, mm, tzinfo=TZ)


def session(sid, start, lines, platform="Teams", title=None):
    """lines: (speaker label as Tomoe writes it, text)."""
    t, segs = 2.0, []
    for i, (spk, text) in enumerate(lines):
        dur = max(1.5, len(text.split()) * 0.4)
        segs.append({"id": f"seg-{i+1}", "speaker": spk, "text": text, "start_time": t, "end_time": t + dur, "source": "monitor"})
        t += dur + 0.6
    s = {
        "id": sid, "title": title or f"{platform} Meeting {start:%Y-%m-%d %H:%M}", "platform": platform, "language": "en",
        "created_at": start.isoformat(), "ended_at": (start + timedelta(seconds=t)).isoformat(), "duration": t,
        "sources": ["mic", "monitor"], "segments": segs,
    }
    d = os.path.join(here, "tomoe", sid)
    os.makedirs(d, exist_ok=True)
    json.dump(s, open(os.path.join(d, "session.json"), "w"), indent=1)


def drop(source, kind, ref, record):
    d = os.path.join(here, "inbox", source)
    os.makedirs(d, exist_ok=True)
    json.dump({"kind": kind, "source": source, "ref": ref, "record": record}, open(os.path.join(d, ref + ".json"), "w"), indent=1)


def event(ref, subject, start, minutes, people, summary="", sensitivity="", extra=()):
    g = lambda t: {"dateTime": t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.0000000"), "timeZone": "UTC"}
    drop("calendar", "event", ref, {
        "subject": subject, "summary": summary, "organizer": P[people[0]][1],
        "attendees": [P[p][1] for p in people] + list(extra), "start": g(start), "end": g(start + timedelta(minutes=minutes)),
        "isCancelled": False, "showAs": "busy", "sensitivity": sensitivity, "location": "Microsoft Teams Meeting",
    })


def who(k):
    return f"Person {list(P).index(k) + 1} ({P[k][0]})"


# T1: internal eng sync. Decision, owners, a customer ask.
event("e1", "Eng weekly sync", at(14, 10), 30, ["jordan", "dana", "priya", "lee"])
session("11111111-0000-4000-8000-000000000001", at(14, 10, 1), [
    ("You", "Morning everyone. First item is the search latency regression. Priya, what did you find?"),
    (who("priya"), "The p95 went from about three hundred milliseconds to nine hundred after the index migration. The new index writes one document at a time, so every update pays a full round trip and the queue backs up during peak hours."),
    (who("priya"), "And to be clear, it is not the load balancer. We ruled that out on Friday."),
    (who("priya"), "My proposal is batching the writes. We collect updates for fifty milliseconds and send them together. In staging that brought p95 back to two hundred eighty."),
    (who("dana"), "That sounds right. Are there risks with ordering? Some customers depend on updates landing in sequence."),
    (who("priya"), "Ordering is preserved inside a batch and batches are sequential, so no change there."),
    ("You", "Good. Decision then: we ship the batching fix on September 22. Priya owns it, Dana reviews."),
    (who("lee"), "On my side, the Acme Freight webhook retries keep failing. Their endpoint times out after five seconds and we retry immediately, which makes it worse. I'll add exponential backoff this week."),
    (who("dana"), "One more thing from accounts. Bluegill Bank wants single sign-on before they roll out to their branches, ideally by Q4. Sam is tracking the request."),
    ("You", "Let's scope SSO next week then. Lee, take the webhook backoff. Thanks all."),
])

# T2: 1:1 with a direct report; the second half is compensation.
event("e2", "Jordan / Dana 1:1", at(15, 9), 30, ["jordan", "dana"])
session("11111111-0000-4000-8000-000000000002", at(15, 9, 2), [
    ("You", "How is the team doing this week?"),
    (who("dana"), "Pretty well. Lee's onboarding is going smoothly. He already shipped two fixes and he's picking up the webhook work. The hiring plan for the platform team is on track, we have two final interviews next week and one offer out."),
    (who("dana"), "The Bluegill single sign-on estimate is about three sprints if we reuse the identity service. Priya thinks two if we skip the admin console for the first release."),
    ("You", "Two sprints without the console seems fine for a first release. Let's propose that to them and keep the console as a follow-up item in the roadmap."),
    (who("dana"), "Okay, I'll write that up and send it to Sam so he can share it with the customer before the end of the month."),
    ("You", "Great. Now, I have news on your side. I put in your raise to 152k and the promotion to senior manager. It should land in the October cycle. Olu signed off yesterday."),
    (who("dana"), "That's wonderful, thank you. Should I keep this quiet until it's announced?"),
    ("You", "Yes, keep it between us until the all-hands. Marta will send the formal letter."),
])

# T3: customer call with an external attendee; deal terms.
event("e3", "Acme Freight renewal", at(15, 14), 45, ["jordan", "samo"], extra=[P["tom"][1]])
session("11111111-0000-4000-8000-000000000003", at(15, 14, 0), [
    (who("samo"), "Thanks for joining, Tom. We wanted to cover the webhook problems and then the renewal."),
    (who("tom"), "Appreciated. The webhook retries have been failing for two weeks. Our operations team noticed missing shipment updates."),
    ("You", "We found the cause. Our retries were hitting your endpoint too quickly. Lee is adding backoff this week, so it should be resolved by Friday."),
    (who("tom"), "Good. On the renewal, our contract value is up for review in November and finance wants a better number."),
    (who("samo"), "We can do a 15% discount if you sign a two-year renewal. That keeps the same feature set and adds priority support."),
    (who("tom"), "I'll take that to finance. A two-year term is probably fine if the webhook issue is really fixed."),
])

# T4: ad-hoc call with people Tomoe couldn't name and no calendar entry.
session("11111111-0000-4000-8000-000000000004", at(16, 15, 30), [
    ("You", "Quick one on Bluegill Bank onboarding. What's the risk?"),
    ("Person 2", "Their security review flagged our data retention policy. They want customer data deleted after ninety days instead of a year."),
    ("Person 3", "Nina from Bluegill said it's a hard requirement from their compliance team, so we can't negotiate it away."),
    ("You", "Then we need a per-tenant retention setting. Let's write that up before their onboarding call."),
], platform=None, title="Session 2026-09-16 15:30")

# An all-day focus block over the ad-hoc call (T4): must never be linked, or
# the call would inherit its tiny internal attendee list and look internal.
g = lambda t: {"dateTime": t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.0000000"), "timeZone": "UTC"}
drop("calendar", "event", "e9", {"subject": "Focus day: no internal meetings", "organizer": P["jordan"][1],
     "attendees": [P["jordan"][1], P["dana"][1], P["priya"][1]], "start": g(at(16, 0)), "end": g(at(17, 0)),
     "isAllDay": True, "isCancelled": False, "showAs": "tentative", "sensitivity": ""})

# A customer kickoff, so the customer contact is known from the calendar.
event("e5", "Bluegill onboarding kickoff", at(11, 13), 45, ["jordan", "samo"], extra=[P["nina"][1]],
      summary="Kickoff for the Bluegill Bank rollout: timeline, data retention requirements, SSO.")

# T5: private executive meeting about an acquisition.
event("e4", "Project Heron", at(17, 11), 60, ["jordan", "olu", "dana"], sensitivity="private")
session("11111111-0000-4000-8000-000000000005", at(17, 11, 0), [
    (who("olu"), "Project Heron update. Due diligence on Kestrel Analytics is complete and the numbers hold up."),
    (who("olu"), "The term sheet is at forty million with a two-year earn-out for the founders. The board meeting to approve it is on October 2."),
    ("You", "Engineering-wise, their pipeline would replace our ingestion layer within two quarters. Dana's team would absorb theirs."),
    (who("dana"), "That's feasible, but we'd need to pause the Bluegill single sign-on work for a sprint during integration."),
])

# T6: all-hands; the promotion is now public.
event("e6", "Northwind all-hands", at(21, 16), 45, ["olu", "jordan"], extra=[ALL])
session("11111111-0000-4000-8000-000000000006", at(21, 16, 0), [
    (who("olu"), "Welcome everyone. A few announcements. Congratulations to Dana Reyes on her promotion to senior manager."),
    (who("olu"), "The Q4 kickoff will be on October 20 in the main office. Please block the whole day."),
    ("You", "From engineering: search latency is back under three hundred milliseconds, and the Bluegill Bank launch is on schedule."),
])

# T7: later eng sync; the earlier decision changed.
event("e7", "Eng weekly sync", at(21, 10), 30, ["jordan", "dana", "priya", "lee"])
session("11111111-0000-4000-8000-000000000007", at(21, 10, 1), [
    ("You", "Status on the batching fix?"),
    (who("priya"), "It slipped. The batching fix will now ship on September 29 because we had to roll back the index migration on Thursday and re-run it."),
    (who("lee"), "The Acme Freight webhook backoff is live and their failures dropped to zero since Tuesday."),
])

# T8: lower-case, unpunctuated text like a live transcript.
event("e8", "Sales sync", at(22, 13), 30, ["jordan", "samo", "will"])
session("11111111-0000-4000-8000-000000000008", at(22, 13, 0), [
    (who("samo"), "acme freight wants the sso work done before they renew and nina at bluegill asked for the same thing"),
    (who("samo"), "honestly i bet acme freight renews for three years not two"),
    ("You", "we will follow up with both of them after the scoping meeting next week"),
    (who("will"), "i can draft the sso one pager for acme freight by monday"),
])

# Mail.
def mail(ref, frm, to, subject, body, sent, cc=(), sensitivity=""):
    drop("mail", "email", ref, {
        "subject": subject, "from": {"name": P[frm][0], "email": P[frm][1]},
        "to": [x if "@" in x else {"name": P[x][0], "email": P[x][1]} for x in to],
        "cc": [{"name": P[x][0], "email": P[x][1]} for x in cc],
        "sentAt": sent.isoformat(), "body": body, "threadId": ref, "sensitivity": sensitivity,
    })

mail("m1", "marta", ["jordan"], "Performance review for Lee Chen",
     "Hi Jordan,\n\nLee's performance review is due Friday. Please flag any performance concerns before the calibration meeting.\n\nThanks,\nMarta", at(16, 9))
mail("m2", "samo", ["jordan"], "Acme Freight renewal pricing",
     "Tom asked for a 15% discount on a two-year renewal. List price stays at 120k ACV. I think we should accept if they commit to two years.\n\nOn Tue, Tom Becker wrote:\n> can you share pricing options?", at(15, 16), cc=["priya"])
mail("m3", "olu", [ALL], "Q4 kickoff",
     "Team,\n\nQ4 kickoff is October 20 in the main office. Agenda: product roadmap, the Bluegill Bank launch, and hiring plans.\n\nOlu", at(18, 8))
mail("m5", "tom", ["jordan", "samo"], "Renewal decision",
     "Hi Jordan, hi Sam,\n\nWe have decided to sign a two-year renewal at the 15% discount. Finance approved it this morning.\n\nTom", at(23, 10))
mail("m4", "tom", ["jordan", "samo"], "Webhook retry failures",
     "Hi both,\n\nOur operations team is still seeing missing shipment updates from the webhooks. Can you confirm when the fix ships?\n\nTom", at(14, 17))

# Chats.
def chat(ref, members, topic, msgs, day):
    drop("chat", "chat", ref, {
        "chatId": ref, "topic": topic, "members": [{"name": P[m][0], "email": P[m][1]} for m in members],
        "messages": [{"from": {"name": P[f][0], "email": P[f][1]}, "sentAt": at(day, 11, i).isoformat(), "text": t} for i, (f, t) in enumerate(msgs)],
    })

chat("c1", ["jordan", "priya"], "", [
    ("priya", "The batching fix PR is up for review."),
    ("jordan", "Nice. Can we get Sam Whitfield to review the auth changes in it?"),
    ("priya", "Sure. Also ask Sam about the Acme renewal call, I missed it."),
], 18)
# A hypothesis that the eng sync (T1) later rules out.
chat("c3", ["jordan", "lee"], "", [
    ("lee", "I think the search latency regression might be caused by the new load balancer config. The timing lines up."),
    ("jordan", "Could be. Can you check with Priya before the sync?"),
], 12)
# A value made more precise: "by Q4" becomes "mid November".
chat("c4", ["jordan", "samo"], "", [
    ("samo", "Nina confirmed Bluegill Bank needs single sign-on live by mid November, not just sometime in Q4."),
    ("jordan", "Good to know. That fits the two-sprint plan."),
], 24)
chat("c2", ["jordan", "samw", "priya"], "security", [
    ("samw", "We found a vulnerability in the token refresh endpoint. CVE request is pending."),
    ("priya", "Is it exploitable from outside?"),
    ("samw", "Only with a valid session. Patch is ready, deploying tonight."),
], 19)

print("corpus written")
