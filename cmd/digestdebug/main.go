// Throwaway: replays a digest's calls on one item and prints the raw replies.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/store"
)

type tee struct{ provider.LLM }

func (t tee) Complete(ctx context.Context, m []provider.Message, o provider.ChatOptions, f func(string)) (string, error) {
	r, err := t.LLM.Complete(ctx, m, o, f)
	show := r
	if len(show) > 1800 {
		show = show[:1800] + " …"
	}
	fmt.Printf("=== %s (prompt %d chars, reply %d chars, err %v)\n%s\n", o.Kind, len(m[1].Content), len(r), err, show)
	return r, err
}

func main() {
	cfg, _ := config.Load(os.Args[1])
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		panic(err)
	}
	id := os.Args[2]
	items, _ := st.AllItems()
	var it *item.Item
	for i := range items {
		if strings.HasPrefix(items[i].ID, id) {
			it = &items[i]
		}
	}
	chunks, _ := st.Chunks(it.ID)
	labels, _ := st.ItemLabels(it.ID)
	llm, err := provider.NewLLM(cfg.Knowledge.Threads.Model)
	if err != nil {
		panic(err)
	}
	m := knowledge.NewMeeting(it.Title, it.OccurredAt, "", cfg.Owner.Name, chunks)
	d, err := (&knowledge.Digester{LLM: tee{llm}, Owner: cfg.Owner.Name, Parallel: true, Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}).Digest(context.Background(), m, labels, it.OccurredAt)
	fmt.Println("RESULT", len(d.Sections), "sections", len(d.Todos), "todos", err)
}
