package stages

import (
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/registry"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

const ClassifyVersion = "k1/" + sensitivity.RulesVersion

// broadcastSize is the audience size at which mail or a meeting counts as a
// broadcast (an all-hands, a company list) rather than a working group.
const broadcastSize = 15

// Classify labels an item and escalates individual chunks. linked is the
// calendar event a meeting was matched to, if any: its attendees join the
// audience and its markings and title count as the meeting's.
func Classify(it item.Item, linked *item.Item, chunks []store.Chunk, reg *registry.Registry, internal []string, internalMinAudience int) (sensitivity.Label, map[int]sensitivity.Label, []string) {
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
	addParts(it.Participants)
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
	audience := make([]string, 0, len(aud))
	for p := range aud {
		audience = append(audience, p)
	}
	sort.Strings(audience)

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
	return itemLabel, chunkLabels, audience
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
