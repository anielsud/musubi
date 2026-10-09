// Command musubi is the CLI for the Musubi knowledge base.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/eval"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

var cfgPath string

func root() *cobra.Command {
	r := &cobra.Command{Use: "musubi", Short: "Local-first personal knowledge base", SilenceUsage: true}
	r.PersistentFlags().StringVar(&cfgPath, "config", config.Path(), "config file")
	r.AddCommand(&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) { fmt.Println("musubi", version) }})
	r.AddCommand(updateCmd(), searchCmd(), graphCmd(), showCmd(), statsCmd(), forgetCmd(), evalCmd())
	return r
}

func load() (config.Config, *store.Store, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return cfg, nil, err
	}
	st, err := store.Open(cfg.DBPath())
	return cfg, st, err
}

// parseEmbedders reads "none", "hash", "ollama:<model>[,ollama:<model>]".
// Empty means the configured embedder.
func parseEmbedders(spec string, cfg config.Config) ([]provider.Embedder, error) {
	if spec == "" {
		e, err := provider.New(cfg.Embed)
		if e == nil || err != nil {
			return nil, err
		}
		return []provider.Embedder{e}, nil
	}
	var out []provider.Embedder
	for _, s := range strings.Split(spec, ",") {
		c := cfg.Embed
		prov, model, _ := strings.Cut(s, ":")
		c.Provider = prov
		if model != "" {
			c.Model = model
		}
		e, err := provider.New(c)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func runner(cfg config.Config, st *store.Store, embs []provider.Embedder, inbox, curated string) (*pipeline.Runner, error) {
	rs, err := raw.Open(cfg.RawDir())
	if err != nil {
		return nil, err
	}
	var conns []source.Connector
	if cfg.TomoeSessions != "" {
		conns = append(conns, tomoe.Connector{Dir: cfg.TomoeSessions})
	}
	conns = append(conns, mcpdrop.Connector{Dir: inbox})
	return &pipeline.Runner{Cfg: cfg, Store: st, Raw: rs, Connectors: conns, Embedders: embs, CuratedPath: curated, Log: os.Stderr}, nil
}

func updateCmd() *cobra.Command {
	var embed string
	c := &cobra.Command{
		Use:   "update",
		Short: "Fetch new data from every source and bring derived data up to date",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			embs, err := parseEmbedders(embed, cfg)
			if err != nil {
				return err
			}
			r, err := runner(cfg, st, embs, cfg.InboxDir(), cfg.Registry())
			if err != nil {
				return err
			}
			start := time.Now()
			var stats pipeline.Stats
			if err := r.Ingest(cmd.Context(), &stats); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ingest: %d records, %d items changed, %d skipped\n", stats.Fetched, stats.Changed, stats.Skipped)
			if err := r.Process(cmd.Context(), &stats); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "process: %v in %s\n", stats.Ran, time.Since(start).Round(time.Millisecond))
			return nil
		},
	}
	c.Flags().StringVar(&embed, "embed", "", `embedders: "none", "hash", "ollama:<model>" (comma-separated to keep several)`)
	return c
}

func searchCmd() *cobra.Command {
	var scope, mode, embed, since string
	var k int
	c := &cobra.Command{
		Use:   "search <query>",
		Short: "Search under a scope (owner, shared-screen, cloud, reply:<email>)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			sc, err := sensitivity.ParseScope(scope)
			if err != nil {
				return err
			}
			q := store.SearchQuery{Text: strings.Join(args, " "), K: k, Mode: store.SearchMode(mode)}
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("--since takes a duration like 720h: %w", err)
				}
				q.Since = time.Now().Add(-d)
			}
			embs, err := parseEmbedders(embed, cfg)
			if err != nil {
				return err
			}
			if len(embs) > 0 && q.Mode != store.ModeKeyword {
				v, err := embs[0].Embed(cmd.Context(), []string{q.Text}, true)
				if err != nil {
					return err
				}
				q.Vec, q.Model = v[0], embs[0].Model()
			}
			hits, err := st.Search(sc, q)
			if err != nil {
				return err
			}
			for i, h := range hits {
				fmt.Printf("%2d. %s  %s  [%s %s]  %s#%d\n", i+1, h.OccurredAt.Format("2006-01-02 15:04"), h.Title, h.Label.Level, h.Label.Categories, h.ItemID, h.Seq)
				fmt.Printf("    %s\n\n", snippet(h.Text, 320))
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the results are for")
	c.Flags().StringVar(&mode, "mode", "hybrid", "hybrid, keyword or vector")
	c.Flags().StringVar(&embed, "embed", "", "query embedder (default: configured)")
	c.Flags().StringVar(&since, "since", "", "only items newer than this duration (e.g. 720h)")
	c.Flags().IntVarP(&k, "k", "k", 8, "results")
	return c
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func graphCmd() *cobra.Command {
	var scope string
	var limit int
	c := &cobra.Command{
		Use:   "graph <name or entity id>",
		Short: "Show an entity's items and the entities it is connected to",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			sc, err := sensitivity.ParseScope(scope)
			if err != nil {
				return err
			}
			ents, err := st.FindEntities(sc, strings.Join(args, " "))
			if err != nil {
				return err
			}
			if len(ents) == 0 {
				return fmt.Errorf("no visible entity matches %q", strings.Join(args, " "))
			}
			if len(ents) > 1 {
				fmt.Println("several entities match; showing the first. Others:")
				for _, e := range ents[1:] {
					fmt.Println("  ", e.ID, e.Name)
				}
			}
			var exclude []string
			if cfg.Owner.Email != "" {
				exclude = append(exclude, "person:"+strings.ToLower(cfg.Owner.Email))
			}
			g, err := st.EntityGraph(sc, ents[0].ID, exclude, limit)
			if err != nil || g == nil {
				return err
			}
			fmt.Printf("%s (%s, %s): %d items, last %s\n\nconnected:\n", g.Entity.Name, g.Entity.Type, g.Entity.ID, g.Entity.Items, g.Entity.LastSeen.Format("2006-01-02"))
			for _, n := range g.Neighbors {
				fmt.Printf("  %3d  %-28s %s  (last %s)\n", n.Items, n.Name, n.ID, n.LastSeen.Format("2006-01-02"))
			}
			fmt.Println("\nitems:")
			for _, it := range g.Items {
				role := ""
				if it.Participant {
					role = " (took part)"
				}
				fmt.Printf("  %s  %-7s %s  [%s]  %d mentions%s\n", it.OccurredAt.Format("2006-01-02"), it.Kind, it.Title, it.Label.Level, it.Mentions, role)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the results are for")
	c.Flags().IntVar(&limit, "limit", 25, "neighbours and items to show")
	return c
}

func showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <item id>",
		Short: "Show an item's chunks, labels and mentions (owner only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			chunks, err := st.Chunks(args[0])
			if err != nil {
				return err
			}
			labels, err := st.ItemLabels(args[0])
			if err != nil {
				return err
			}
			ms, err := st.Mentions(args[0])
			if err != nil {
				return err
			}
			_, aud, _ := st.ItemLabel(args[0])
			il := labels[-1]
			fmt.Printf("item %s: %s/%s (%s: %s)\naudience: %s\n\n", args[0], il.Level, il.Categories, il.Origin, il.Reason, strings.Join(aud, ", "))
			for _, c := range chunks {
				l := sensitivity.Combine(il, labels[c.Seq])
				extra := ""
				if cl, ok := labels[c.Seq]; ok {
					extra = " ← " + cl.Reason
				}
				fmt.Printf("── chunk %d [%s/%s]%s\n%s\n", c.Seq, l.Level, l.Categories, extra, c.Text)
				var mm []string
				for _, m := range ms {
					if m.ChunkSeq == c.Seq {
						id := m.EntityID
						if id == "" {
							id = "?" + strings.Join(m.Candidates, "|")
						}
						mm = append(mm, fmt.Sprintf("%s→%s", m.Surface, id))
					}
				}
				if len(mm) > 0 {
					fmt.Printf("   mentions: %s\n", strings.Join(mm, "; "))
				}
				fmt.Println()
			}
			return nil
		},
	}
}

func statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Counts of items, labels, entities and mentions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			s, err := st.Stats()
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func forgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <item id>",
		Short: "Delete an item, everything derived from it, and its raw record; never re-ingest it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			h, used, err := st.Forget(args[0])
			if err != nil {
				return err
			}
			if !used {
				rs, err := raw.Open(cfg.RawDir())
				if err != nil {
					return err
				}
				if err := rs.Delete(h); err != nil {
					return err
				}
			}
			fmt.Println("forgotten:", args[0], "(the original still exists in its source)")
			return nil
		},
	}
}

func evalCmd() *cobra.Command {
	var cases, corpus, embed, jsonOut, baseline, modes string
	c := &cobra.Command{
		Use:   "eval",
		Short: "Score retrieval, leakage, classification, resolution and graph cases",
		Long: `Builds a fresh database from --corpus (a directory with config.toml,
registry.toml, tomoe/ and inbox/) or uses the configured database, then runs
the cases. Exits non-zero on any leak.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			var st *store.Store
			if corpus != "" {
				tmp, err := os.MkdirTemp("", "musubi-eval-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(tmp)
				if cfg, err = config.Load(filepath.Join(corpus, "config.toml")); err != nil {
					return err
				}
				cfg.DataDir = tmp
				cfg.TomoeSessions = filepath.Join(corpus, "tomoe")
				if cases == "" {
					cases = filepath.Join(corpus, "cases.toml")
				}
			}
			if st, err = store.Open(cfg.DBPath()); err != nil {
				return err
			}
			defer st.Close()
			embs, err := parseEmbedders(embed, cfg)
			if err != nil {
				return err
			}
			if corpus != "" {
				r, err := runner(cfg, st, embs, filepath.Join(corpus, "inbox"), filepath.Join(corpus, "registry.toml"))
				if err != nil {
					return err
				}
				r.Log = nil
				var stats pipeline.Stats
				if err := r.Ingest(cmd.Context(), &stats); err != nil {
					return err
				}
				if err := r.Process(cmd.Context(), &stats); err != nil {
					return err
				}
			}
			suite, err := eval.Load(cases)
			if err != nil {
				return err
			}
			var ms []store.SearchMode
			for _, m := range strings.Split(modes, ",") {
				ms = append(ms, store.SearchMode(m))
			}
			var base map[string]float64
			if baseline != "" {
				b, err := os.ReadFile(baseline)
				if err != nil {
					return err
				}
				json.Unmarshal(b, &base)
			}
			if len(embs) == 0 {
				embs = []provider.Embedder{nil}
			}
			all := map[string]float64{}
			leaks := 0
			for _, e := range embs {
				name := "keyword-only"
				if e != nil {
					name = e.Model()
				}
				fmt.Printf("=== %s ===\n", name)
				h := &eval.Harness{Store: st, Embedder: e, Modes: ms}
				rep, err := h.Run(cmd.Context(), suite)
				if err != nil {
					return err
				}
				prefixed := map[string]float64{}
				for k, v := range rep.Summary {
					prefixed[name+":"+k] = v
					all[name+":"+k] = v
				}
				rep.Summary = prefixed
				rep.Print(os.Stdout, base)
				leaks += rep.Leaks()
				fmt.Println()
			}
			if jsonOut != "" {
				keys := make([]string, 0, len(all))
				for k := range all {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				b, _ := json.MarshalIndent(all, "", "  ")
				if err := os.WriteFile(jsonOut, b, 0o644); err != nil {
					return err
				}
			}
			if leaks > 0 {
				return fmt.Errorf("%d leaks", leaks)
			}
			return nil
		},
	}
	c.Flags().StringVar(&cases, "cases", "", "cases.toml (default: <corpus>/cases.toml)")
	c.Flags().StringVar(&corpus, "corpus", "", "build a fresh database from this corpus directory")
	c.Flags().StringVar(&embed, "embed", "", `embedders to compare, e.g. "hash" or "ollama:nomic-embed-text,ollama:qwen3-embedding:0.6b"`)
	c.Flags().StringVar(&modes, "modes", "hybrid,keyword,vector", "search modes to score")
	c.Flags().StringVar(&jsonOut, "json", "", "write metrics as JSON (use as a later --baseline)")
	c.Flags().StringVar(&baseline, "baseline", "", "metrics JSON from an earlier run to compare against")
	return c
}
