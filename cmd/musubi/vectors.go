package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/store"
)

func vectorsCmd() *cobra.Command {
	c := &cobra.Command{Use: "vectors", Short: "Stored embedding vectors"}
	var db string
	var vacuum bool
	enc := &cobra.Command{
		Use:   "encode f16|f32",
		Short: "Rewrite every stored vector in an encoding",
		Long: `Rewrites the stored vectors (passages, statements, claim and thread
centroids) as f16 (half precision, half the space) or f32. New vectors are
written as store.vector_encoding says; this converts what is already there.

f32 → f16 rounds every component. f16 → f32 does not bring the original
precision back, only widens the rounded values: for the exact vectors,
re-embed (musubi update --embed ...). Works on --db (default: the
configured database, or the one of --config), which is backed up first
([backup] before_changes; a --db elsewhere is not). --vacuum
(default) then rebuilds the file so it shrinks; that needs no other
process writing to it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != store.VecF16 && args[0] != store.VecF32 {
				return fmt.Errorf("encoding must be f16 or f32, not %q", args[0])
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			path := db
			if path == "" {
				path = cfg.DBPath()
			}
			if _, err := os.Stat(path); err != nil {
				return err
			}
			st, err := store.Open(path)
			if err != nil {
				return err
			}
			defer st.Close()
			if filepath.Clean(path) == filepath.Clean(cfg.DBPath()) {
				if err := backupBefore(cfg, st, "musubi vectors encode "+args[0]); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(os.Stderr, "not backed up: --db isn't the dataset's database")
			}
			stats, err := st.ReencodeVectors(args[0], func(s store.EncodeStats) {
				fmt.Fprintf(os.Stderr, "\r%s: %d read, %d rewritten", s.Table, s.Rows, s.Rewritten)
			})
			fmt.Fprintln(os.Stderr)
			for _, s := range stats {
				fmt.Printf("%-15s %7d vectors, %7d rewritten\n", s.Table, s.Rows, s.Rewritten)
			}
			if err != nil {
				return err
			}
			if vacuum {
				fmt.Fprintln(os.Stderr, "vacuuming…")
				if err := st.Vacuum(); err != nil {
					return fmt.Errorf("vacuum (the vectors are rewritten; run it again when nothing else has the database open): %w", err)
				}
			}
			return nil
		},
	}
	enc.Flags().StringVar(&db, "db", "", "database file (default: the configured one)")
	enc.Flags().BoolVar(&vacuum, "vacuum", true, "rebuild the file afterwards so it shrinks")
	c.AddCommand(enc)
	return c
}
