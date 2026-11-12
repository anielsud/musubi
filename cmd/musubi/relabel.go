package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/engine"
)

// relabelCmd puts a corrected speaker name on meetings' stored copies with
// no model calls (docs/recorder-screens.md, "Renaming a voice profile
// renames its speakers").
func relabelCmd() *cobra.Command {
	var profile, item, from, to, sessionFile, batch, undo string
	var undoMusubiOnly bool
	c := &cobra.Command{
		Use:   "relabel",
		Short: "Rename a speaker in meetings' labels (a renamed voice profile, or one meeting), with no model calls",
		Long: `Rename a speaker in meetings' labels, with no model calls.

  --profile MODEL/ID          the recorder relabels the meetings the voice profile
                              identified, then Musubi relabels its copies
  --item ID --from X --to Y   Musubi relabels one item's copies by speaker name
  --item ID --session FILE    ... from a session file the recorder rewrote
  --undo BATCH                reverses a relabel (the recorder's files too,
                              unless --musubi-only)

Prints what changed as JSON.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			ctx := cmd.Context()
			var res *engine.RelabelResult
			switch {
			case undo != "":
				res, err = e.UndoRelabel(ctx, os.Stderr, undo, !undoMusubiOnly)
			case profile != "":
				model, id, ok := strings.Cut(profile, "/")
				if !ok {
					return fmt.Errorf("--profile is MODEL/ID")
				}
				res, err = e.RelabelProfile(ctx, os.Stderr, model, id)
			case item != "" && sessionFile != "":
				b, rerr := os.ReadFile(sessionFile)
				if rerr != nil {
					return rerr
				}
				res, err = e.RelabelSession(ctx, os.Stderr, item, b, batchOr(batch))
			case item != "" && from != "" && to != "":
				res, err = e.RelabelByName(ctx, os.Stderr, item, from, to, batchOr(batch))
			default:
				return fmt.Errorf("give --profile, --item with --from/--to or --session, or --undo")
			}
			if res != nil {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				enc.Encode(res)
			}
			return err
		},
	}
	c.Flags().StringVar(&profile, "profile", "", "voice profile, MODEL/ID")
	c.Flags().StringVar(&item, "item", "", "item id (tomoe:<session>)")
	c.Flags().StringVar(&from, "from", "", "speaker name now")
	c.Flags().StringVar(&to, "to", "", "speaker name to give")
	c.Flags().StringVar(&sessionFile, "session", "", "rewritten session.json")
	c.Flags().StringVar(&batch, "batch", "", "batch id to record it under (for --undo)")
	c.Flags().StringVar(&undo, "undo", "", "batch to reverse")
	c.Flags().BoolVar(&undoMusubiOnly, "musubi-only", false, "with --undo: leave the recorder's files")
	return c
}

func batchOr(b string) string {
	if b != "" {
		return b
	}
	return "cli"
}
