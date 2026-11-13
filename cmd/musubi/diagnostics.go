package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/diagnostics"
	"github.com/anielsud/musubi/internal/engine"
)

func diagnosticsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnostics",
		Short: "Print a report to paste to support: versions, counts, connections, settings, recent errors; no meeting content, names or email addresses",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				// Still say what can be said: a database this build refuses
				// is when a report is most needed.
				cfg, _ := config.Load(cfgPath)
				in := diagnostics.Input{App: version, Cfg: cfg, Errors: []string{"opening the dataset: " + err.Error()}}
				fmt.Print(diagnostics.Build(in))
				return nil
			}
			defer e.Close()
			in := e.DiagnosticsInput(cmd.Context(), version)
			if q, err := e.Queue(); err == nil {
				in.Queue = &q
			}
			fmt.Print(diagnostics.Build(in))
			return nil
		},
	}
}
