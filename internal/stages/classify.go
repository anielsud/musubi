package stages

import (
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

const ClassifyVersion = "k2/" + sensitivity.RulesVersion

// broadcastSize is the audience size at which mail or a meeting counts as a
// broadcast (an all-hands, a company list) rather than a working group.
const broadcastSize = 15

// Classify labels an item and escalates individual chunks. linked is the
// calendar event a meeting was matched to, if any: its markings and title
// count as the meeting's, and its attendees set how widely the meeting was
// held, which decides the level.
//
// speakersOnly keeps the attendees out of the returned audience (who the
// content may be shown or sent to) for a recording: only the people heard
// speaking were certainly there. A recording can run on after most of the
// invitees have left; an invite list must not open that conversation to
// them. Silent attendees and unidentified voices lose access, which only
// narrows reads; broad meetings stay visible to them through their level.
func Classify(it item.Item, linked *item.Item, chunks []store.Chunk, reg *registry.Registry, internal []string, internalMinAudience int, speakersOnly bool) (sensitivity.Label, map[int]sensitivity.Label, []string) {
	aud := map[string]bool{}
	if p := reg.OwnerPrincipal(); p != "" {
		aud[p] = true
	}
	addParts := func(ps []item.Participant) {
		for _, p := range ps {
			if id := reg.Participant(p); id != "" {
				if pr := reg.Principal(id); pr != "" {
					aud[pr] = true
				}
			} else if p.Email != "" {
				aud[sensitivity.NormalizePrincipal(p.Email)] = true
			} else if p.Name != "" {
				aud[sensitivity.NormalizePrincipal(p.Name)] = true
			}
		}
	}
	// A speaker known only by name is matched to the invitee of that name,
	// so the audience holds their address, which reply scopes compare.
	invitee := map[string]string{}
	if linked != nil {
		for _, p := range linked.Participants {
			n := strings.ToLower(strings.TrimSpace(p.Name))
			if n == "" || p.Email == "" {
				continue
			}
			if _, dup := invitee[n]; dup {
				invitee[n] = "" // ambiguous: leave the name unresolved
				continue
			}
			invitee[n] = p.Email
		}
	}
	speakers := make([]item.Participant, 0, len(it.Participants))
	for _, p := range it.Participants {
		if p.Email == "" && !p.Owner {
			if e := invitee[strings.ToLower(strings.TrimSpace(p.Name))]; e != "" {
				p.Email = e
			}
		}
		speakers = append(speakers, p)
	}
	addParts(speakers)
	heard := sortedKeys(aud)
	native := map[string]string{}
	for k, v := range it.Native {
		native[k] = v
	}
	title := it.Title
	if linked != nil {
		addParts(linked.Participants)
		if s := linked.Native["sensitivity"]; s != "" {
			native["sensitivity"] = s
		}
		title = linked.Title + " " + title
	}
	audience := sortedKeys(aud)

	facts := sensitivity.ItemFacts{
		Kind: it.Kind, Title: title, Native: native, Audience: audience, InternalDomains: internal, InternalMinAudience: internalMinAudience,
		UnknownParticipants: it.Native["unknown_participants"] == "true" && linked == nil,
		Broadcast:           len(audience) >= broadcastSize || hasListAddress(&it, linked),
	}
	itemLabel := sensitivity.ClassifyItem(facts)
	chunkLabels := map[int]sensitivity.Label{}
	for _, c := range chunks {
		if l, ok := sensitivity.ClassifyText(c.Text); ok {
			chunkLabels[c.Seq] = l
		}
	}
	if speakersOnly && linked != nil && it.Kind == item.KindMeeting {
		audience = heard
	}
	return itemLabel, chunkLabels, audience
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasListAddress spots company-wide distribution lists by their usual names.
func hasListAddress(items ...*item.Item) bool {
	for _, it := range items {
		if it == nil {
			continue
		}
		for _, p := range it.Participants {
			local := strings.Split(strings.ToLower(p.Email), "@")[0]
			if p.Email != "" && (local == "all" || strings.HasPrefix(local, "all-") || strings.HasPrefix(local, "everyone")) {
				return true
			}
		}
	}
	return false
}
