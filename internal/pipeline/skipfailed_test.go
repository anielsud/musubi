package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

// cutOffLLM extracts like labelLLM, but its extraction reply is cut off
// mid-JSON (as at the token limit) for every passage of the item titled bad,
// or of every item when bad is "".
type cutOffLLM struct {
	labelLLM
	bad string
}

func (c cutOffLLM) Complete(ctx context.Context, msgs []provider.Message, o provider.ChatOptions, f func(string)) (string, error) {
	extract := len(msgs) == 2 && strings.HasPrefix(msgs[0].Content, "You pick out what is worth remembering")
	if extract && (c.bad == "" || strings.HasPrefix(msgs[1].Content, "Source: "+c.bad+"\n")) {
		return `{"statements":[{"kind":"fact","text":"The pricing`, nil
	}
	return c.labelLLM.Complete(ctx, msgs, o, f)
}

// failingItem picks an item with a title no other item has and passages to
// extract, so the fake fails exactly that one. Not a meeting linked to an
// event: extraction shows the event's title instead.
func failingItem(t *testing.T, st *store.Store) item.Item {
	t.Helper()
	items, _ := st.AllItems()
	linked, _ := st.LinksFrom("occurred_as")
	n := map[string]int{}
	for _, it := range items {
		n[it.Title]++
	}
	for _, it := range items {
		if _, ok := linked[it.ID]; ok || it.Title == "" || n[it.Title] > 1 || it.Kind == item.KindEvent {
			continue
		}
		if cs, _ := st.Chunks(it.ID); len(cs) > 0 {
			return it
		}
	}
	t.Skip("fixture has no uniquely titled item with passages")
	return item.Item{}
}

func skipFailedRunner(t *testing.T) (*Runner, *store.Store) {
	r, st := build(t)
	r.Cfg.Knowledge.Extract, r.Cfg.Knowledge.Gate, r.Cfg.Knowledge.MinWorth = "statements", 0, 0
	return r, st
}

// Off (the default): one item failing every passage fails the run after
// extraction, before embedding and linking, as before.
func TestEveryPassageFailedFailsRunByDefault(t *testing.T) {
	r, st := skipFailedRunner(t)
	bad := failingItem(t, st)
	ks, err := r.Knowledge(context.Background(), KnowledgeOptions{LLM: cutOffLLM{bad: bad.Title}, Embedder: provider.Hash{Dim: 64}})
	if err == nil || !strings.Contains(err.Error(), "every passage failed") {
		t.Fatalf("err = %v, want every passage failed", err)
	}
	if ks.Embedded != 0 || ks.Link.New != 0 || ks.Skipped != 0 {
		t.Fatalf("stages after extraction ran: %d embedded, %d claims, %d skipped", ks.Embedded, ks.Link.New, ks.Skipped)
	}
}

// On: the item is skipped and left unstamped, the rest are embedded and
// linked, and the next run tries it again.
func TestSkipFailedItems(t *testing.T) {
	r, st := skipFailedRunner(t)
	r.Cfg.Knowledge.SkipFailedItems = true
	bad := failingItem(t, st)
	llm := cutOffLLM{bad: bad.Title}
	ks := knowledgeRun(t, r, KnowledgeOptions{LLM: llm})
	if ks.Skipped != 1 || ks.Items < 1 {
		t.Fatalf("skipped %d, extracted %d", ks.Skipped, ks.Items)
	}
	if ks.Embedded == 0 || ks.Link.New == 0 {
		t.Fatalf("stages after extraction didn't run: %d embedded, %d claims", ks.Embedded, ks.Link.New)
	}
	if s, _ := st.StageStamp("extract", bad.ID); s != "" {
		t.Fatalf("skipped item stamped %q", s)
	}
	// Still failing: tried again, and since nothing in this run extracted,
	// the run fails as a broken model would.
	if _, err := r.Knowledge(context.Background(), KnowledgeOptions{LLM: llm, Embedder: provider.Hash{Dim: 64}}); err == nil {
		t.Fatal("retry of the failing item alone: no error")
	}
	// Fixed (another model, say): extracted and stamped.
	ks = knowledgeRun(t, r, KnowledgeOptions{LLM: cutOffLLM{bad: "no such title"}})
	if ks.Items != 1 || ks.Skipped != 0 {
		t.Fatalf("retry: extracted %d, skipped %d", ks.Items, ks.Skipped)
	}
	if s, _ := st.StageStamp("extract", bad.ID); s == "" {
		t.Fatal("retried item not stamped")
	}
}

// On, but every item fails: a broken model or cache still fails the run.
func TestSkipFailedItemsEveryItemFailed(t *testing.T) {
	r, _ := skipFailedRunner(t)
	r.Cfg.Knowledge.SkipFailedItems = true
	ks, err := r.Knowledge(context.Background(), KnowledgeOptions{LLM: cutOffLLM{}, Embedder: provider.Hash{Dim: 64}})
	if err == nil || !strings.Contains(err.Error(), "no item extracted") {
		t.Fatalf("err = %v, want no item extracted", err)
	}
	if ks.Skipped < 2 || ks.Items != 0 || ks.Embedded != 0 {
		t.Fatalf("skipped %d, extracted %d, embedded %d", ks.Skipped, ks.Items, ks.Embedded)
	}
}
