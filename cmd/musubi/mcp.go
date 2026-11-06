package main

import (
	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/mcpserver"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the knowledge base to a local MCP client (Claude Code) on stdin/stdout",
		Long: `Serve Musubi's knowledge to the owner's assistant over MCP (stdio).
It reads in the owner's scope and labels every result with its sensitivity
and who may hear it, rather than hiding it; see docs/mcp-design.md.

Register it once with Claude Code:
  claude mcp add --scope user musubi -- <path to musubi> mcp`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			return mcpserver.Run(cmd.Context(), e, version)
		},
	}
}
