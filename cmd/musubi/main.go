// Command musubi is the CLI for the Musubi knowledge base.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/eval"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/live"
	"github.com/anielsud/musubi/internal/m365"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/bluedot"
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
	r.AddCommand(importItemsCmd(), extractLeanCmd(), updateCmd(), searchCmd(), graphCmd(), showCmd(), statsCmd(), forgetCmd(), evalCmd(), askCmd(), prepCmd(), threadsCheckCmd(), insertCheckCmd(), aliasesCmd(), liveCmd(),
		knowledgeCmd(), gateCmd(), claimsCmd(), topicsCmd(), topicCmd(), cardCmd(), judgeCmd(), threadLinksCmd(), m365Cmd(), nameMeetingsCmd(), peopleCmd(), mcpCmd(), vectorsCmd(), relabelCmd())
	return r
}

func load() (config.Config, *store.Store, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return cfg, nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err == nil {
		if err = st.SetVectorEncoding(cfg.Store.VectorEncoding); err != nil {
			st.Close()
			st = nil
		}
	}
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
	if d := cfg.SessionsDir(); d != "" {
		conns = append(conns, tomoe.Connector{Dir: d, Since: cfg.SessionsSince()})
	}
	if cfg.BluedotDir != "" {
		conns = append(conns, bluedot.Connector{Dir: cfg.BluedotDir, Since: cfg.BluedotSince, Known: func(ref string) bool {
			for _, src := range []string{"teams", "teams-elt"} {
				if _, ok, _ := st.ManifestHash(src, ref); ok {
					return true
				}
			}
			return false
		}})
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
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			cfg, st := e.Cfg, e.Store
			embs, err := parseEmbedders(embed, cfg)
			if err != nil {
				return err
			}
			r, err := runner(cfg, st, embs, cfg.InboxDir(), cfg.Registry())
			if err != nil {
				return err
			}
			start := time.Now()
			if rep, err := e.SyncM365(cmd.Context()); err != nil {
				fmt.Fprintf(os.Stderr, "m365: %v\n", err)
				if errors.Is(err, m365.ErrAuthRequired) {
					fmt.Fprintln(os.Stderr, "m365: run `musubi m365 connect` to sign in again")
				}
			} else if rep != "" {
				fmt.Fprintf(os.Stderr, "m365: %s\n", rep)
			}
			var stats pipeline.Stats
			if err := r.Ingest(cmd.Context(), &stats); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ingest: %d records, %d items changed, %d skipped\n", stats.Fetched, stats.Changed, stats.Skipped)
			if err := r.Process(cmd.Context(), &stats); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "process: %v in %s\n", stats.Ran, time.Since(start).Round(time.Millisecond))
			if n, err := e.WriteTomoeContext(); err != nil {
				fmt.Fprintf(os.Stderr, "tomoe calendar context: %v\n", err)
			} else if n > 0 {
				fmt.Fprintf(os.Stderr, "tomoe calendar context: %d recordings updated\n", n)
			}
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
			sc, err := sensitivity.ParseScope(scope, cfg.Owner.InternalDomains)
			if err != nil {
				return err
			}
			if sc.Name != "owner" {
				st.Audit(sc, "search", strings.Join(args, " "))
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
		// Back to a rune boundary: a cut through a character is invalid UTF-8.
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
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
			sc, err := sensitivity.ParseScope(scope, cfg.Owner.InternalDomains)
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
			h, used, err := knowledge.ForgetItem(st, args[0])
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
	var withKnowledge, record, trace bool
	var judge string
	var minWorth float64
	var keep string
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
				tmp := keep
				if tmp == "" {
					t, err := os.MkdirTemp("", "musubi-eval-")
					if err != nil {
						return err
					}
					defer os.RemoveAll(t)
					tmp = t
				} else {
					os.RemoveAll(tmp)
				}
				if cfg, err = config.Load(filepath.Join(corpus, "config.toml")); err != nil {
					return err
				}
				cfg.DataDir = tmp
				cfg.TomoeSessions = filepath.Join(corpus, "tomoe")
				if judge != "" {
					cfg.Knowledge.Judge = judge
				}
				if minWorth >= 0 {
					cfg.Knowledge.MinWorth = minWorth
				}
				if cases == "" {
					cases = filepath.Join(corpus, "cases.toml")
				}
			}
			if st, err = store.Open(cfg.DBPath()); err != nil {
				return err
			}
			defer st.Close()
			if err := st.SetVectorEncoding(cfg.Store.VectorEncoding); err != nil {
				return err
			}
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
				if withKnowledge {
					if err := corpusKnowledge(cmd.Context(), r, cfg, corpus, record, trace); err != nil {
						return err
					}
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
				h := &eval.Harness{Store: st, Embedder: e, Modes: ms, InternalDomains: cfg.Owner.InternalDomains, OwnerName: cfg.Owner.Name}
				if cfg.Owner.Email != "" {
					h.OwnerID = "person:" + strings.ToLower(cfg.Owner.Email)
				}
				if !withKnowledge {
					suite = withoutKnowledge(suite)
				}
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
	c.Flags().StringVar(&keep, "keep", "", "with --corpus: build the database in this directory and keep it")
	c.Flags().BoolVar(&withKnowledge, "knowledge", false, "also build and score the knowledge layer (corpus: replays <corpus>/llm-cache)")
	c.Flags().StringVar(&judge, "judge", "", "with --knowledge: override [knowledge] judge (json, decide)")
	c.Flags().Float64Var(&minWorth, "min-worth", -1, "with --knowledge: override [knowledge] min_worth")
	c.Flags().BoolVar(&trace, "trace", false, "with --knowledge: print each linking decision")
	c.Flags().BoolVar(&record, "record", false, "with --knowledge: call the real models for anything not in the cache, and save it")
	return c
}

func aliasesCmd() *cobra.Command {
	c := &cobra.Command{Use: "aliases", Short: "Learned aliases: load candidates, review them with their impact, confirm or reject"}
	open := func() (*engine.Engine, error) { return engine.Open(cfgPath) }
	c.AddCommand(&cobra.Command{
		Use:   "propose <candidates.json>",
		Short: "Load candidates (tools/entity-bench/aliases.py --json) as proposed aliases; vetoed ones are skipped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			var cands []map[string]any
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if err := json.Unmarshal(b, &cands); err != nil {
				return err
			}
			n := 0
			for _, c := range cands {
				score, _ := c["score"].(float64)
				if score <= 0 {
					continue
				}
				ev, _ := json.Marshal(c)
				if err := e.Store.ProposeAlias(store.LearnedAlias{Keep: fmt.Sprint(c["keep"]), Variant: fmt.Sprint(c["variant"]), Kind: "spelling",
					Evidence: string(ev), Score: score, By: "corpus"}); err != nil {
					return err
				}
				n++
			}
			fmt.Printf("%d aliases proposed\n", n)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Drop undecided proposals and propose again from the corpus (decisions are kept)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			if err := e.Store.DropProposedAliases(); err != nil {
				return err
			}
			n, err := knowledge.ProposeAliases(e.Store, e.Cfg.Owner.Name)
			if err != nil {
				return err
			}
			fmt.Printf("%d aliases proposed\n", n)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "list [proposed|confirmed|rejected]",
		Short: "List aliases with what each would touch",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			status := "proposed"
			if len(args) == 1 {
				status = args[0]
			}
			as, err := e.Store.LearnedAliases(status)
			if err != nil {
				return err
			}
			for _, a := range as {
				im, err := e.Store.ImpactOf(a)
				if err != nil {
					return err
				}
				fmt.Printf("%4d  %-14s → %-14s  %s, score %.1f: %d statements in %d items, %d threads (%d newly linked)\n",
					a.ID, a.Variant, a.Keep, a.Kind, a.Score, im.Statements, im.Items, len(im.Threads), len(im.NewLinks))
				for _, x := range im.Sample[:min(len(im.Sample), 2)] {
					fmt.Printf("        e.g. %s\n", clipText(x, 110))
				}
			}
			return nil
		},
	})
	for _, verb := range []string{"confirm", "reject"} {
		c.AddCommand(&cobra.Command{
			Use:   verb + " <id>...",
			Short: map[string]string{"confirm": "Confirm aliases (they apply when reading; nothing is rewritten)", "reject": "Reject aliases"}[verb],
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e, err := open()
				if err != nil {
					return err
				}
				defer e.Close()
				for _, a := range args {
					var id int64
					if _, err := fmt.Sscan(a, &id); err != nil {
						return err
					}
					if err := e.Store.DecideAlias(id, verb == "confirm", "owner"); err != nil {
						return err
					}
				}
				return nil
			},
		})
	}
	return c
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func insertCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "insert-check <item id>...",
		Short: "Take items out of the thread graph and insert them again, comparing with the replay (changes the database: use a copy)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			r, err := e.Runner(nil, os.Stderr)
			if err != nil {
				return err
			}
			for _, id := range args {
				res, err := r.InsertCheck(cmd.Context(), pipeline.KnowledgeOptions{LLM: e.Chat, Embedder: e.Embedder}, id)
				if err != nil {
					return err
				}
				pct := func(a, b int) float64 { return 100 * float64(a) / float64(max(b, 1)) }
				fmt.Printf("%s: %d statements; placed %d (replay %d); of %d whose thread survived the removal, %d back in it (%.0f%%); "+
					"%d new threads, %d near an existing one; current agrees with the replay on %d of %d memberships (%.1f%%)\n",
					id, res.Statements, res.Placed, res.PlacedBefore, res.Survived, res.SurvivedSame, pct(res.SurvivedSame, res.Survived),
					res.NewThreads, res.NearExisting, res.AgreeBefore, res.ThreadItems, pct(res.AgreeBefore, res.ThreadItems))
			}
			return nil
		},
	}
}

func threadsCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "threads-check",
		Short: "Derive each thread's current items from the relations and compare with what is stored",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			c, err := knowledge.CheckDerived(e.Store, time.Time{})
			if err != nil {
				return err
			}
			fmt.Printf("%d memberships; derived and stored agree on %d (%.1f%%)\n", c.Memberships, c.Agree, 100*float64(c.Agree)/float64(max(c.Memberships, 1)))
			fmt.Printf("stored current, derived retired: %d\n", c.StoredCurrentDerivedRetired)
			fmt.Printf("stored retired, derived current: %v\n", c.StoredRetiredDerivedCurrent)
			return nil
		},
	}
}

func prepCmd() *cobra.Command {
	var people []string
	var scope string
	var brief bool
	c := &cobra.Command{
		Use:   "prep [<item id of a past meeting or upcoming event>]",
		Short: "What to bring to a meeting: their subjects, what moved, what is open, what to share",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			sc, err := e.Scope(scope)
			if err != nil {
				return err
			}
			items, err := e.Store.AllItems()
			if err != nil {
				return err
			}
			req := knowledge.PrepRequest{People: people, QuietAfter: e.Cfg.Prep.QuietAfter(), MarkPrivate: e.Cfg.Prep.MarkPrivate}
			if len(args) == 1 {
				req.Template = args[0]
				for _, it := range items {
					if it.ID == req.Template && it.Kind == item.KindEvent {
						ms, err := e.ThreadsAbout(cmd.Context(), sc, knowledge.MeetingQuery(it, e.Cfg.Owner.Name, e.Cfg.Owner.InternalDomains))
						if err != nil {
							return err
						}
						for _, m := range ms {
							req.About = append(req.About, m.ID)
						}
					}
				}
			}
			v, err := knowledge.Prep(e.Store, sc, req, items, e.Cfg.Owner.Name)
			if err != nil {
				return err
			}
			fmt.Printf("%s — with %s; since %s; %d meetings with them\n", v.Title, strings.Join(v.People, ", "), v.Since.Format("2006-01-02"), v.Met)
			if len(v.New) > 0 {
				fmt.Printf("first meeting with: %s\n", strings.Join(v.New, ", "))
			}
			if v.Description != "" {
				fmt.Printf("description: %s\n", v.Description)
			}
			for _, s := range v.Subjects {
				mark := ""
				if s.Private {
					mark = "  [for you, not the room]"
				}
				fmt.Printf("\n# %s  (%s)%s\n", s.Name, s.Why, mark)
				if s.Status != nil {
					fmt.Printf("  now: %s\n", knowledge.StripIDs(s.Status.Now))
				}
				for _, it := range s.NewSince {
					fmt.Printf("  new %s: %s\n", it.SaidAt.Format("01-02"), it.Text)
				}
			}
			fmt.Println("\nOpen between you:")
			quiet := false
			for _, o := range v.Open {
				if o.Quiet && !quiet {
					fmt.Println("\nGone quiet (nothing said of it since; check before raising):")
					quiet = true
				}
				var tags []string
				tags = append(tags, "raised "+o.Raised.Format("02 Jan"))
				if o.Overdue {
					tags = append(tags, "overdue")
				}
				if o.Private {
					tags = append(tags, "for you")
				}
				fmt.Printf("  - %s [%s; %s] (%s; %s)\n", knowledge.StripIDs(o.Text), o.Who, o.Due, o.Name, strings.Join(tags, ", "))
			}
			fmt.Println("\nWorth sharing:")
			for _, s := range v.Share {
				fmt.Printf("  - %s %s: %s (from %s)\n", s.Item.SaidAt.Format("01-02"), s.Name, s.Item.Text, s.Item.ItemTitle)
			}
			if brief {
				fmt.Println("\nTalking points:")
				if _, err := ask.PrepBrief(cmd.Context(), e, v, func(t string) { fmt.Print(t) }); err != nil {
					return err
				}
				fmt.Println()
			}
			return nil
		},
	}
	c.Flags().StringSliceVar(&people, "people", nil, "the people you are meeting, without a meeting to start from")
	c.Flags().StringVar(&scope, "scope", "owner", "who the result is for")
	c.Flags().BoolVar(&brief, "brief", false, "also write talking points with the ask model")
	return c
}

func askCmd() *cobra.Command {
	var scope string
	var k int
	var asJSON, passagesOnly bool
	c := &cobra.Command{
		Use:   "ask <question>",
		Short: "Answer a question from the knowledge base, citing sources",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			sc, err := e.Scope(scope)
			if err != nil {
				return err
			}
			if passagesOnly {
				e.Cfg.Ask.Threads = false
			}
			onToken := func(t string) { fmt.Print(t) }
			if asJSON {
				onToken = nil
			}
			t0 := time.Now()
			a, err := ask.Ask(cmd.Context(), e, sc, strings.Join(args, " "), k, nil, onToken)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"text": a.Text, "sources": a.Sources, "threads": a.Threads, "seconds": time.Since(t0).Seconds(), "prompt_chars": a.PromptChars})
			}
			if len(a.Threads) > 0 {
				fmt.Println("\n\nsubjects:")
				for _, t := range a.Threads {
					fmt.Printf("  T%d %s (as of %s)\n", t.ID, t.Name, t.AsOf.Format("2006-01-02"))
				}
			}
			fmt.Println("\n\nsources:")
			for i, h := range a.Sources {
				fmt.Printf("  [%d] %s  %s  %s#%d\n", i+1, h.OccurredAt.Format("2006-01-02"), h.Title, h.ItemID, h.Seq)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the answer is for")
	c.Flags().IntVarP(&k, "k", "k", 8, "passages to read")
	c.Flags().BoolVar(&asJSON, "json", false, "print the answer, sources and threads as JSON")
	c.Flags().BoolVar(&passagesOnly, "passages-only", false, "answer from passages alone (ask.threads = false)")
	return c
}

func liveCmd() *cobra.Command {
	var replay, events string
	var stepped, cardsOnly bool
	var until float64
	var speed float64
	var duration time.Duration
	c := &cobra.Command{
		Use:   "live",
		Short: "Print the live panel's events: from Tomoe's feed, or --replay a saved session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), duration)
			defer cancel()
			// Every event in full, one JSON line each, for measuring the
			// panel (what it showed, and when).
			var evw *json.Encoder
			if events != "" {
				f, err := os.Create(events)
				if err != nil {
					return err
				}
				defer f.Close()
				evw = json.NewEncoder(f)
			}
			var evMu sync.Mutex
			var lv *live.Live
			l := live.New(e, func(ev string, data any) {
				if evw != nil {
					evMu.Lock()
					rec := map[string]any{"at": time.Now(), "event": ev, "data": data}
					if stepped && lv != nil {
						rec["clock"] = lv.StepClock()
					}
					evw.Encode(rec)
					evMu.Unlock()
				}
				switch ev {
				case "live:segment":
					return
				case "live:brief":
					b := data.(live.Brief)
					if b.State == "loading" {
						return
					}
					fmt.Printf("%s BRIEF %q [%s, %s] %s\n", time.Now().Format("15:04:05"), b.Query, b.Kind, b.State, b.Summary)
					for _, h := range b.Hits {
						fmt.Printf("      %s  %s\n", h.OccurredAt.Format("Jan 2"), h.Title)
					}
					return
				}
				j, _ := json.Marshal(data)
				fmt.Printf("%s %s %s\n", time.Now().Format("15:04:05"), ev, snippet(string(j), 600))
			}, nil)
			lv = l
			if replay != "" && stepped {
				if err := l.Reload(); err != nil {
					return err
				}
				l.CardsOnly = cardsOnly
				t0 := time.Now()
				err := l.ReplayStepped(ctx, replay, until)
				fmt.Fprintf(os.Stderr, "stepped replay: %s\n", time.Since(t0).Round(time.Second))
				return err
			}
			if replay != "" {
				if err := l.Reload(); err != nil {
					return err
				}
				if err := l.Replay(ctx, replay, speed); err != nil {
					return err
				}
			}
			l.Run(ctx)
			return nil
		},
	}
	c.Flags().StringVar(&replay, "replay", "", "Tomoe session id to replay")
	c.Flags().Float64Var(&speed, "speed", 4, "replay speed")
	c.Flags().StringVar(&events, "events", "", "write every panel event, in full, to this file (JSON lines)")
	c.Flags().BoolVar(&stepped, "stepped", false, "with --replay: line by line on the meeting's clock, each line's work done before the next (speed-independent; --speed ignored)")
	c.Flags().Float64Var(&until, "until", 0, "with --stepped: stop at this many seconds into the meeting")
	c.Flags().BoolVar(&cardsOnly, "cards-only", false, "with --stepped: only what topic cards need (lookups put topics forward without searching or summarising; suggestions on complete lines only)")
	c.Flags().DurationVar(&duration, "for", 2*time.Minute, "how long to run")
	return c
}

// corpusKnowledge builds the knowledge layer for a test corpus from its
// recorded model replies (and, with record, the real models).
func corpusKnowledge(ctx context.Context, r *pipeline.Runner, cfg config.Config, corpus string, record, trace bool) error {
	var innerLLM provider.LLM
	var innerEmb provider.Embedder
	if record {
		chat, err := provider.NewChat(config.DefaultConfig().LLM)
		if err != nil {
			return err
		}
		innerLLM = chat
		innerEmb, err = provider.New(config.DefaultConfig().Embed)
		if err != nil {
			return err
		}
	}
	llm, err := provider.NewCachedLLM(filepath.Join(corpus, "llm-cache", "chat.json"), config.DefaultConfig().LLM.Model, innerLLM, record)
	if err != nil {
		return err
	}
	emb, err := provider.NewCachedEmbedder(filepath.Join(corpus, "llm-cache", "embed.json"), config.DefaultConfig().Embed.Model, innerEmb, record)
	if err != nil {
		return err
	}
	o := pipeline.KnowledgeOptions{LLM: llm, Embedder: emb, Workers: 2}
	if trace {
		o.LinkTrace = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  link: "+f+"\n", a...) }
	}
	ks, err := r.Knowledge(ctx, o)
	if record {
		if serr := llm.Save(); serr != nil && err == nil {
			err = serr
		}
		if serr := emb.Save(); serr != nil && err == nil {
			err = serr
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "knowledge: %d items, %d statements, %d claims new, %d joined, %d relations, %d topics\n",
		ks.Items, ks.Statements, ks.Link.New, ks.Link.Joined, ks.Link.Related, ks.Topics)
	return nil
}

func withoutKnowledge(s *eval.Suite) *eval.Suite {
	out := *s
	out.Cases = nil
	for _, c := range s.Cases {
		if c.Kind != "statement" && c.Kind != "claim" && c.Kind != "topic" && c.Kind != "card" {
			out.Cases = append(out.Cases, c)
		}
	}
	return &out
}

func importItemsCmd() *cobra.Command {
	var itemID, file, tag string
	c := &cobra.Command{
		Use:   "import-items",
		Short: "Load knowledge items extracted outside Musubi as an item's statements (experiments); set knowledge.extract = \"none\" so they are kept",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			id, err := e.Store.ResolveItemID(itemID)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			var in struct {
				Items []knowledge.Imported `json:"items"`
			}
			if err := json.Unmarshal(b, &in); err != nil {
				return err
			}
			r, err := e.Runner(nil, os.Stderr)
			if err != nil {
				return err
			}
			n, skipped, err := r.ImportItems(id, in.Items, tag)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s: %d statements imported, %d skipped\n", id, n, skipped)
			return nil
		},
	}
	c.Flags().StringVar(&itemID, "item", "", "item id or prefix")
	c.Flags().StringVar(&file, "file", "", `JSON file {"items": [{"kind","status","text","who","lines"}]}`)
	c.Flags().StringVar(&tag, "tag", "", "label for where the items came from (model, prompt)")
	c.MarkFlagRequired("item")
	c.MarkFlagRequired("file")
	return c
}

func knowledgeCmd() *cobra.Command {
	var items, redo []string
	var limit, workers, newest int
	var verbose, plan, noThreads, digestOnly bool
	c := &cobra.Command{
		Use:   "knowledge",
		Short: "Extract statements, link them into claims, and build topics (uses the local model)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			for _, s := range redo {
				if s != "extract" && s != "links" && s != "threads" && s != "label" && s != "digest" {
					return fmt.Errorf("--redo takes extract, links, threads, label or digest (got %q)", s)
				}
			}
			o := pipeline.KnowledgeOptions{Items: items, Limit: limit, Workers: workers,
				Redo: redo, RedoNewest: newest, Plan: plan, NoThreads: noThreads, DigestOnly: digestOnly}
			if verbose {
				o.LinkTrace = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  link: "+f+"\n", a...) }
			}
			start := time.Now()
			ks, err := e.Knowledge(cmd.Context(), os.Stderr, o)
			defer printOutdated(ks.Outdated, e.Cfg.Knowledge.Updates)
			if plan {
				p := ks.Planned
				if digestOnly {
					fmt.Fprintf(os.Stderr, "plan: read %d meetings into summaries and to-dos\n", p.Digest)
					return err
				}
				fmt.Fprintf(os.Stderr, "plan: extract %d items; %s; thread %d items%s\n", p.Extract,
					map[bool]string{true: "rebuild every claim", false: "link new statements"}[p.RebuildLinks], p.Thread,
					map[bool]string{true: " (every thread cleared first)", false: ""}[p.RebuildThreads])
				return err
			}
			per := time.Duration(0)
			if ks.Chunks > 0 {
				per = ks.ExtractTime / time.Duration(ks.Chunks)
			}
			fmt.Fprintf(os.Stderr, "knowledge: %d items (%d passages, %d gated, extraction %s, %s per passage), %d statements (%d embedded, %d scored, %d below min_worth), claims %d new / %d joined / %d judged (linking %s), %d relations, %d topics; %d items still to extract; %s total\n",
				ks.Items, ks.Chunks, ks.Gated, ks.ExtractTime.Round(time.Second), per.Round(100*time.Millisecond), ks.Statements, ks.Embedded, ks.Scored, ks.Unworthy,
				ks.Link.New, ks.Link.Joined, ks.Link.Judged, ks.LinkTime.Round(time.Second), ks.Link.Related, ks.Topics, ks.Pending, time.Since(start).Round(time.Second))
			if ks.Skipped > 0 {
				fmt.Fprintf(os.Stderr, "knowledge: %d items skipped (every passage failed); tried again next run\n", ks.Skipped)
			}
			return err
		},
	}
	c.Flags().StringSliceVar(&items, "items", nil, "only these item ids or prefixes")
	c.Flags().IntVar(&limit, "limit", 0, "extract at most this many items this run")
	c.Flags().IntVar(&workers, "workers", 2, "parallel model requests")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "print each linking decision")
	c.Flags().StringSliceVar(&redo, "redo", nil, "bring these stages up to their current version: extract, links (rethreads too, in thread mode), threads, label (relabel every statement with the configured labeller), digest (meeting summaries and to-dos)")
	c.Flags().IntVar(&newest, "newest", 0, "with --redo extract: only this many items, newest first")
	c.Flags().BoolVar(&noThreads, "no-threads", false, "stop after linking (threads applied from elsewhere)")
	c.Flags().BoolVar(&digestOnly, "digest-only", false, "only write meeting summaries and to-dos (knowledge.digest): no extraction, linking or threading")
	c.Flags().BoolVar(&plan, "plan", false, "say what a run would do, and what is on an earlier version, without doing it")
	return c
}

// printOutdated says what was done under an earlier version and kept, and
// how to bring it up to date.
func printOutdated(o pipeline.Outdated, mode string) {
	if mode == "auto" {
		return
	}
	if o.Extract > 0 {
		fmt.Fprintf(os.Stderr, "older version: %d items were extracted under an earlier extraction version (musubi knowledge --redo extract [--newest N])\n", o.Extract)
	}
	if o.Links {
		fmt.Fprintln(os.Stderr, "older version: claims were built under earlier linking rules (musubi knowledge --redo links)")
	}
	if o.Threads > 0 {
		fmt.Fprintf(os.Stderr, "older version: %d items were threaded under earlier rules (musubi knowledge --redo threads)\n", o.Threads)
	}
	if o.Digest > 0 {
		fmt.Fprintf(os.Stderr, "older version: %d meetings were digested under an earlier digest version (musubi knowledge --redo digest)\n", o.Digest)
	}
	if o.Backfilled > 0 {
		fmt.Fprintf(os.Stderr, "out of order: %d items older than ones already threaded were threaded after them (musubi knowledge --redo threads)\n", o.Backfilled)
	}
}

func gateCmd() *cobra.Command {
	var items []string
	c := &cobra.Command{
		Use:   "gate",
		Short: "Score passages with the extraction gate without extracting (tab-separated: item, passage, p, ms)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			if e.Chat == nil {
				return fmt.Errorf("needs [llm] configured")
			}
			all, err := e.Store.AllItems()
			if err != nil {
				return err
			}
			links, err := e.Store.LinksFrom("occurred_as")
			if err != nil {
				return err
			}
			titles := map[string]string{}
			for _, it := range all {
				titles[it.ID] = it.Title
			}
			for _, it := range all {
				if !slices.ContainsFunc(items, func(p string) bool { return strings.HasPrefix(it.ID, p) }) {
					continue
				}
				title := it.Title
				if ev, ok := links[it.ID]; ok && titles[ev] != "" {
					title = titles[ev]
				}
				chunks, err := e.Store.Chunks(it.ID)
				if err != nil {
					return err
				}
				for _, ch := range chunks {
					lines := knowledge.SplitLines(ch.Text)
					if len(lines) == 0 {
						continue
					}
					t := time.Now()
					p, err := knowledge.Worth(cmd.Context(), e.Chat, title, lines)
					if err != nil {
						return err
					}
					fmt.Printf("%s\t%d\t%.3f\t%d\n", it.ID, ch.Seq, p, time.Since(t).Milliseconds())
				}
			}
			return nil
		},
	}
	c.Flags().StringSliceVar(&items, "items", nil, "item ids or prefixes (required)")
	c.MarkFlagRequired("items")
	return c
}

func views(scope string) (*engine.Engine, knowledge.Views, error) {
	e, err := engine.Open(cfgPath)
	if err != nil {
		return nil, knowledge.Views{}, err
	}
	sc, err := e.Scope(scope)
	if err != nil {
		e.Close()
		return nil, knowledge.Views{}, err
	}
	return e, knowledge.Views{Store: e.Store, Scope: sc, Now: time.Now()}, nil
}

func printClaim(c knowledge.ClaimView) {
	fmt.Printf("#%d [%s, %s %.2f] %s\n", c.ID, c.Kind, c.Belief.Status, c.Belief.Score, c.Text)
	fmt.Printf("     %s · %d statement(s), %d holder(s) · %s\n", c.Belief.Why, len(c.Statements), c.Belief.Holders, c.Belief.Last.Format("2006-01-02"))
	for _, r := range c.Relations {
		arrow := "→"
		if r.Direction == "in" {
			arrow = "←"
		}
		fmt.Printf("     %s %s #%d [%s] %s\n", arrow, r.Rel, r.ClaimID, r.Status, snippet(r.Text, 90))
	}
}

func claimsCmd() *cobra.Command {
	var scope string
	var k int
	var verbose bool
	c := &cobra.Command{
		Use:   "claims <words>",
		Short: "Find claims and show how much they are believed",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, v, err := views(scope)
			if err != nil {
				return err
			}
			defer e.Close()
			cs, err := v.SearchClaims(strings.Join(args, " "), k)
			if err != nil {
				return err
			}
			for _, c := range cs {
				printClaim(c)
				if verbose {
					for _, st := range c.Statements {
						val := ""
						if st.Value.Type != "none" && st.Value.Raw != "" {
							val = fmt.Sprintf(", %s %q", st.Value.Type, st.Value.Raw)
						}
						fmt.Printf("       · %s %s (%s, %.0f%% sure%s, %s) %s#%d\n", st.SaidAt.Format("Jan 2"), st.Holder, st.Kind, st.Certainty*100, val, st.Label.Level, st.ItemID, st.ChunkSeq)
						fmt.Printf("         %s\n", st.Text)
					}
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the results are for")
	c.Flags().IntVarP(&k, "k", "k", 10, "claims")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "show each statement")
	return c
}

func topicsCmd() *cobra.Command {
	var scope string
	c := &cobra.Command{
		Use:   "topics",
		Short: "List topics, largest first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, v, err := views(scope)
			if err != nil {
				return err
			}
			defer e.Close()
			ts, err := v.Topics()
			if err != nil {
				return err
			}
			sort.Slice(ts, func(i, j int) bool { return ts[i].Claims > ts[j].Claims })
			for _, t := range ts {
				hidden := ""
				if t.Hidden {
					hidden = " (label hidden in this scope)"
				}
				fmt.Printf("%4d  %-44s %3d claims  %v  last %s%s\n", t.ID, snippet(t.Label, 44), t.Claims, t.Statuses, t.Last.Format("Jan 2"), hidden)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the results are for")
	return c
}

func topicCmd() *cobra.Command {
	var scope string
	c := &cobra.Command{
		Use:   "topic <id>",
		Short: "Show a topic: its claims, people and related topics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, v, err := views(scope)
			if err != nil {
				return err
			}
			defer e.Close()
			var id int64
			fmt.Sscan(args[0], &id)
			t, err := v.Topic(id)
			if err != nil || t == nil {
				if t == nil && err == nil {
					return fmt.Errorf("no visible topic %d", id)
				}
				return err
			}
			fmt.Printf("%s\n%s\n\n", t.Label, t.Summary)
			for _, c := range t.ClaimList {
				printClaim(c)
			}
			fmt.Println("\npeople:")
			for _, p := range t.People {
				fmt.Printf("  %-28s %d claims\n", p.Name, p.Claims)
			}
			fmt.Println("\nrelated:")
			for _, r := range t.Related {
				un := ""
				if r.Unconnected {
					un = "  (never discussed together)"
				}
				fmt.Printf("  %4d  %-40s sim %.2f, %d shared items%s\n", r.ID, snippet(r.Label, 40), r.Sim, r.Shared, un)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the results are for")
	return c
}

func judgeCmd() *cobra.Command {
	var note string
	return &cobra.Command{
		Use:   "judge <statement id> <verified|disputed|false|clear>",
		Short: "Record your verdict on the claim a statement belongs to",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, err := load()
			if err != nil {
				return err
			}
			defer st.Close()
			var id int64
			fmt.Sscan(args[0], &id)
			v := args[1]
			if v == "clear" {
				v = ""
			}
			return st.Judge(id, v, note)
		},
	}
}

func threadLinksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "thread-links",
		Short: "Rebuild the threads' centroids, entities and relations from what is stored (no model calls)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			r, err := e.Runner(nil, os.Stderr)
			if err != nil {
				return err
			}
			n, err := r.LinkThreads()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%d threads linked\n", n)
			return nil
		},
	}
}

func extractLeanCmd() *cobra.Command {
	var itemID, out string
	c := &cobra.Command{
		Use:   "extract-lean",
		Short: "Extract one meeting with the lean prompt (knowledge.lean) and write the items to a file; stores nothing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			if !e.Cfg.Knowledge.AllowHosted {
				return fmt.Errorf("knowledge.allow_hosted is off")
			}
			llm, err := provider.NewLLM(e.Cfg.Knowledge.Lean.Model)
			if err != nil || llm == nil {
				return fmt.Errorf("knowledge.lean.model: %v", err)
			}
			id, err := e.Store.ResolveItemID(itemID)
			if err != nil {
				return err
			}
			r, err := e.Runner(nil, os.Stderr)
			if err != nil {
				return err
			}
			began := time.Now()
			items, sts, err := r.LeanDryRun(cmd.Context(), llm, id)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(map[string]any{"item": id, "items": items}, "", " ")
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s: %d items, %d statements, %.0fs\n", id, len(items), len(sts), time.Since(began).Seconds())
			return nil
		},
	}
	c.Flags().StringVar(&itemID, "item", "", "item id or prefix")
	c.Flags().StringVar(&out, "out", "", "where to write the items")
	c.MarkFlagRequired("item")
	c.MarkFlagRequired("out")
	return c
}
