# Scaling: from four meetings to forty thousand

Written 2026-10-16. Everything measured so far ran on four leadership
meetings (1,014 extracted items). This document says what the system costs
now, what breaks first as the corpus grows by 100×, 1,000× and 10,000×
and as email and chat arrive, and how we measure and improve it on the way.
Numbers come from `research-log.md`; estimates are marked as such.

## 1. Baseline (measured)

| Step | Unit | Hosted (GLM-5.3-Flash) | Local (Qwen3.8-27B, M5 Pro) |
|---|---|---|---|
| Extraction (lean prompt v2) | ELT-length meeting (~250 items) | ~2.5¢ | ~12 min |
| Embedding | statement or passage | — | ~ms |
| Threading (lean, t3) | ELT-length meeting | ~1.9¢ | ~8.5 min |
| Card | read | — | ~30 ms, no model |
| Search | query | — | <50 ms at 10³ passages |

Quality on the linking benchmark (lean threads, hosted): grouping B-cubed
precision 0.86–0.87, recall 0.51–0.53; 30–35% of the reference's
retiring updates retired; 1.5–2.5% of sampled live items wrongly retired;
84% of card lines on subject (local, with phrase lookup). Run-to-run
noise on the update measures is 5–10 points.

Over a 20-meeting replay, threading prompts stayed flat (assign 7.6–8.3k
tokens, reconcile 1.8–2.4k) at about 2.6¢ per meeting. Nothing else has
been run past four meetings.

## 2. Volumes (estimates)

| Horizon | Meetings | Extracted items | Notes |
|---|---|---|---|
| Today | 4 | 1,014 | benchmark |
| 400 | ~5 months of one person's calendar | ~40–100k | average meeting has far fewer items than an ELT meeting |
| 4,000 | ~4 years, or a small team | ~0.4–1M | |
| 40,000 | a department, or a decade | ~4–10M | |
| ~1.5M a year | the target: about 1,000 meetings a year for each of 1,500 people | ~10⁸ | shared meetings counted once per attendee |
| + email | a year: ~30–50k messages | most are not knowledge | |
| + chat | six months: ~50–200k messages | short, conversational | |

Passages for search run at roughly 30–50 per meeting, so 40,000 meetings
is about 1.5–2M passages before email and chat.

Constraints from the owner: no meeting type is skipped or assumed low value
(a 1:1 is in scope like an all-hands); spend model effort on the
high-value items within a meeting, not on choosing meetings; cutting tokens
comes first; and every variant reports tokens and cost per meeting next to
its quality.

## 3. What each component costs as it grows

| Component | Cost per new meeting | Grows with | Breaks around | Why |
|---|---|---|---|---|
| Extraction | linear in its length | nothing else | never (per item); total model time does | one pass per item |
| Threading, lean | ~flat | candidate list (bounded), per-thread state (capped) | not by prompt size; by **sequential throughput** and **thread quality** | items must be threaded in time order |
| Threading, original | grows | every thread in every assign prompt | ~120 threads (local 12k-token slots); ~6–8 meetings | full registry |
| Pairwise linker | grows | cosine scan over all claims per statement | ~10⁵ claims (minutes), ~10⁶ (hours) | O(n) scan per statement, O(n²) total |
| Topic clustering (HAC) | full rebuild | claims² | ~20k claims | average link is O(n²) memory and time |
| Card index | full rebuild at load | all statements and centroids in memory | ~10⁶ statements (GBs, seconds to load) | built in memory from `CardRows` |
| Thread links (`LinkThreads`) | full rebuild after threading | threads² (pairs compared) | ~10⁴ threads (10⁸ pairs; minutes) | all pairs of centroids; needs nearest-neighbour candidates |
| Topic map (Explore) | per view, when read | topics² in the browser layout, stored relations | ~1–2k topics on screen | all-pairs force layout; needs aggregation by cluster first |
| Passage search | per query | passages in scope | ~10⁵–10⁶ passages (exact scan in seconds) | exact cosine over float32 vectors |
| Storage | linear | vectors dominate | ~10M statements ≈ 30 GB of float32 vectors | 768 × 4 bytes each, plus passages |
| Rebuild on a rule change | all of history | everything | at 40,000 meetings: weeks locally, days hosted | rethreading is sequential |

**What breaks first, in order:** the card index and passage search
(both near 10⁵–10⁶ rows), then sequential threading throughput (a
backfill of thousands of meetings), then thread coherence (duplicates,
umbrellas, drift over years), then rebuild cost.

## 4. Coherence at scale

More data doesn't only cost more; it makes grouping harder. The risks, and
the mechanisms that address them:

1. **Duplicate threads.** The same subject started twice, because the
   right thread wasn't among the candidates or the model didn't recognise
   it. Grows with the number of threads.
   - Measure: candidate recall (was the reference thread offered?), and
     the share of new threads that later look like old ones (name and
     centroid similarity).
   - Fix: a periodic **merge pass** over similar thread pairs (a cheap
     similarity filter, then one model call per pair), recorded as a
     thread event.
2. **Umbrella threads.** A thread that keeps absorbing items on different
   specifics becomes a theme (seen: a 65-item "AI platform positioning"
   thread; the old catch-all topic had 118 claims).
   - Measure: thread size, spread across meetings and time, and the spread
     of its members' vectors.
   - Fix: a **split pass** for threads over a size or spread bound, and
     threads anchored to entities (below).
3. **Drift and staleness.** A thread's current list grows if nothing
   retires it (in the local run 830 of 978 statements stayed current).
   - Fix: a **thread state**: a short summary of where the subject stands,
     maintained by reconcile and shown in place of old items; current
     statements age out after a bound unless they are open questions,
     commitments or rules.
4. **Lifecycle.** Threads go dormant and come back.
   - Fix: dormant threads drop out of the recent candidates but stay
     findable by embedding, terms and entities; a thread untouched for a
     long time is marked dormant, and closed when its last commitment is
     done or its last question answered.
5. **Structure above threads.** Thousands of threads need a way to be
   navigated: customers, projects, programmes and people are entities
   already. Threads link to the entities they name, and a card or answer
   can start from an entity ("everything on this account"). Hierarchy
   stays shallow: entity → threads → items.
6. **Cross-source subjects.** An email thread and a meeting about the same
   deal must land in the same thread. Candidate threads already work across
   sources; what's missing is a reference that contains both (see §5).

## 5. Measuring effectiveness as it grows

Four meetings can't show what happens at four thousand. Measurement has to
come in tiers, each with a job:

| Tier | What | Size | Use |
|---|---|---|---|
| Synthetic | planted failures, invented company (`testdata/synthetic/link-bench`) | 250 items | every change, minutes, cents |
| Real, whole meetings | the first two ELT meetings, complete | ~475 items | quick real check (a hand-picked subset misleads: research log 2026-10-16) |
| Real, full benchmark | the four ELT meetings, Opus reference | 1,014 items | before a default changes; two runs each |
| Long real history | a run of real meetings (and later mail) chosen with the owner, long enough for subjects to start, recur, go quiet, return, be renamed and end; reference by Opus as for the four meetings | 50–100 meetings plus mail | coherence over time |
| Real, new types | one reference per kind: a 1:1, a customer call, an email thread, a chat channel | a few hundred items each | source mix |
| Production signals | no reference: invariants and the owner's feedback | everything | drift |

Next additions to the benchmark itself:
1. **More in-meeting updates in the synthetic set.** Real data has 64% of
   updates inside one meeting; the synthetic set has 20%, which is why two
   cost cuts looked harmless there and failed on real data.
2. **Candidate recall** as a scored measure: for each item whose reference
   thread already existed, was it among the candidates?
3. **The long real history** (real meetings, Opus reference; the owner
   chose real data over a generated year on 2026-10-16), scored on
   duplicate threads, umbrella threads, purity over time, and
   update recall across long gaps.
4. **Email and chat references**: one real email thread and one chat
   channel that overlap a referenced meeting's subjects, labelled by Opus
   like the meetings.
5. **Deterministic or repeated runs** for any decision closer than 10
   points: two runs per variant at the default temperature, or
   temperature 0 where the model doesn't loop.

Production signals (no labels needed), tracked per week:
- new threads per meeting (should fall as the corpus matures), thread
  size distribution (catch-all detection), similar-thread pairs
  (duplicates), current-list length per thread (staleness);
- retirements per meeting, open questions and commitments by age;
- model calls, tokens and dollars per item, by step and source;
- card latency and lines per card;
- the owner's feedback: cards opened, dismissed or marked wrong, and
  verdicts on claims (verified, disputed, false).

## 6. Making it faster and cheaper

Ordered by expected payoff against risk. Each is measured on the tiers
above before it becomes a default.

**Threading**
1. **Gate reconcile.** Reasoning is 85–90% of GLM's output and reconcile is
   two thirds of threading's cost. Most reconcile replies are expected to
   be empty; a cheap first question (one forward pass, or a small model:
   "does any new item change an earlier one?") would skip the expensive
   call when the answer is no. Measure first how many replies are empty.
2. **Batch small reconciles.** Threads touched by one or two items can
   share a call.
3. **Prompt caching.** Cached input costs about a fifth on Baseten. Keep
   the system prompt and the stable part of the candidate list as the
   prefix and the batch at the end.
4. **Partitioned backfill.** Threading is sequential, so a backfill of
   thousands of meetings is slow even when each is cheap. Thread
   independent periods in parallel (each month on its own), then merge
   threads across periods with the merge pass. Incremental threading of
   new items stays sequential.
5. **Fold assign into extraction.** The extractor already reads the
   meeting; given the candidate threads it can tag each item as it writes
   it, removing the assign pass. Reconcile stays separate (asking assign
   for effects failed: research log 2026-10-16).
6. **Smaller models where they suffice.** Assign is classification. Try
   small local models on it, and distil: GLM's assignments on real data
   are training data for a small model. The 1.7B pairwise judge failed
   badly, so every candidate goes through the benchmark.

**Extraction**
7. **Extract and thread each meeting once.** At organisation scale most
   meetings have several attendees with Musubi. A shared meeting is
   extracted and threaded once and read by each attendee through their own
   scope (sensitivity and audience are already per read), which divides
   the largest cost by the average number of attendees.
8. **Triage email and chat** before extraction: automated mail,
   newsletters and FYI chatter are indexed for search but not extracted;
   quoted history is stripped; a conversation (thread or channel day) is
   extracted as one unit, not message by message.
9. **Lazy extraction for the long tail** (designed in architecture.md,
   "Cost and scale"): extract when something is first retrieved or a
   thread needs it.
10. **Reasoning effort** per step, as for assign: measured, not assumed
   (low effort on extraction is already the lean prompt's setting).

**Storage and reads**
11. **Incremental card index.** Persist terms, thread centroids and
    membership (membership already is), update them as items are threaded,
    and load nothing at startup.
12. **Search at scale.** int8 or binary vectors with a scope-filtered
    prefilter and an exact re-rank; and exact hits first for short queries
    (the same weakness the card phrase lookup fixed).
13. **Vector storage.** Keep float32 for recent and active items, int8 for
    the rest; nothing is lost that can't be re-derived.

**Rebuilds**
14. **Forward-only changes by default.** A prompt or rule change applies
    to new items; a full rethread is a deliberate background job,
    partitioned as in (4). Thread-level changes (merge, split, state) are
    events, not rebuilds.

## 7. Rough economics (estimates)

Hosted, at today's per-item prices (extraction ~2.5¢ and threading ~1.9¢
per ELT-length meeting; average meetings are smaller):

| Corpus | Extraction + threading | Notes |
|---|---|---|
| 400 meetings | ~$5–18 | |
| 4,000 meetings | ~$50–180 | |
| 40,000 meetings | ~$500–1,800 | before the savings in §6 |
| + a year of email, triaged | ~$10–50 | most mail indexed, not extracted |
| + six months of chat | ~$10–40 | extracted per conversation |
| Organisation, per year (~1.5M meeting-attendances) | ~$5–20k before sharing; a few thousand dollars with each meeting processed once | needs §6 (1), (3), (7) |

Locally, the constraint is time, not money: an ELT-length meeting takes
about 20 minutes of model time end to end on the M5 Pro. Twenty meetings
a week fit overnight; a backfill of thousands does not (4,000 average
meetings ≈ several hundred hours). Backfill therefore runs hosted (allowed
since 2026-10-16) or lazily, with local models for incremental work where
they keep up.

## 8. Order of work

Cost first (the owner's call, 2026-10-16), so every later refinement runs
on the cheaper pipeline. Each item says whether later work is likely to
replace or redo it.

**A. Cost**

1. **Measure what the cost steps depend on.** *Done 2026-10-16
   (research log): reconcile reasoning is the cost; empty reconciles are
   17% of it, caching about 6%; candidate recall needs the long history.*
   Empty-reconcile rate;
   tokens and dollars per step, split into reasoning and answer; how
   often assign's candidates include the right thread (candidate recall);
   cached-prefix share of each prompt. A few small hosted runs on the
   full benchmark.
   *Lasting:* the measures are reused by everything below.
2. **Reasoning effort per step** (§6.10). *Reconcile at low measured
   2026-10-16: 35–37% cheaper, update recall about 3 points lower on
   average; adoption is the owner's call.* Then **prompt caching** (§6.3,
   worth about 6% at four meetings):
   stable system prompt and candidate list first, the batch last.
   *Done for extraction (2026-10-16): 42% of segment input is a prefix
   shared within a meeting; with Baseten's `x-session-affinity` header
   and the first segment sent alone, 21% of it is served from cache
   (segments 6% cheaper). Reconcile's shared start is too short to
   cache. Reconcile's first attempt now has room enough not to be cut off
   (3% of threading). Low reconcile effort and dropping the brief were
   measured cheaper but lose quality: not adopted (owner, 2026-10-16).*
   *Lasting, cheap to redo:* any prompt change (C2's thread state, A6's
   folded extraction) means re-checking effort and re-laying the cache
   prefix, which is small work.
3. **Gate reconcile** (§6.1): a cheap check skips the full call when no
   new item changes an earlier one. *Measured: no-effect calls are the
   small ones, 17% of cost at default reasoning and less at low. Low
   priority.*
   *Mostly lasting:* C2's thread state changes what reconcile reads, so
   the gate is re-measured then, but the mechanism stays.
4. **Batch small reconciles** (§6.2). *Measured: calls with 1–2 new
   items cost $0.003 of $0.082. Low priority.*
   *Likely made smaller by (3):* if most single-item reconciles are
   gated away, few are left to batch. Decide after (3).
5. *Done 2026-10-16 (`knowledge.updates = "explicit"`, architecture.md
   "Derive stages").* **Forward-only rule changes** (§6.14): a prompt or rule change applies
   to new items; a full rethread is a deliberate background job. We
   change rules often, and today each change rethreads everything.
   *Lasting.* Partitioned rethreads (C4) build on it.
6. *Parked 2026-10-16: assign is about 15% of the bill ($0.025–0.028 of
   ~$0.18 per four meetings); folding adds the candidate list (~2.7k
   tokens) to every extraction call, a thread code per item, and a merge
   of new threads started by parallel segments, for a net ~4–7%. Proving
   no extraction loss would take several graded runs and a new threading
   score for re-extracted items.*
   **Fold assign into extraction** (§6.5), or else **a small or distilled
   model for assign** (§6.6). Pick one by the benchmark.
   *Folding replaces* the small assign model and assign's own cache
   layout from (2). Needs good candidate recall from (1) first, because
   the extractor then sees the candidates. Riskier on quality than (2)
   to (5).
7. **Lazy extraction for the long tail** (§6.9).
   *Lasting, but later in practice:* threading must see items in time
   order, so an item extracted late needs the out-of-order handling of
   C4. It matters only once there's a backfill.

Cost items deferred by the owner or by missing data, not replaced:
- extract and thread each shared meeting once (§6.7): not a priority on
  one person's data;
- email and chat triage (§6.8): when those sources arrive (C3).

Coherence passes (C2) add model calls; budget them against what A saved.

**B. Reads at scale**

1. Incremental card index; search prefilter and exact-hit priority.
2. Nearest-neighbour candidates for thread links and the topic map, which
   compare every pair today.

**C. Quality and coherence**

1. The long real history (§5): real meetings chosen with the owner,
   referenced by Opus, scored on duplicates, umbrellas, purity over time
   and update recall across long gaps.
2. Merge, split, thread state and dormancy (§4).
3. Email and chat: triage, conversation-level extraction, and their
   references.
4. Partitioned backfill (§6.4).

Decided by the owner (2026-10-16):
- the approved hosted endpoints may read anything, meeting text included,
  so backfill and heavy steps can run hosted;
- shared meetings, chats and emails may be processed once for all the
  people who were party to them. Not a priority while Musubi works on one
  person's data.
