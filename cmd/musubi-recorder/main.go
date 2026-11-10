// Command musubi-recorder is Musubi's meeting recorder. For now it only
// works offline, on audio Tomoe already saved, so its output can be
// compared with Tomoe's before it records anything itself.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "musubi-recorder",
	Short:         "Musubi's meeting recorder",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// sessionCmd groups the commands that work on saved sessions. Sessions are
// read from Tomoe's folder while Tomoe is the recorder of record.
var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Work on saved sessions",
}

func init() {
	rootCmd.AddCommand(sessionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "musubi-recorder:", err)
		os.Exit(1)
	}
}
