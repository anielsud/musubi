package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/engine"
)

func m365Cmd() *cobra.Command {
	c := &cobra.Command{Use: "m365", Short: "Microsoft 365 connection: connect, status, sync, signout"}
	open := func() (*engine.Engine, error) { return engine.Open(cfgPath) }
	c.AddCommand(&cobra.Command{
		Use:   "connect",
		Short: "Sign in to Microsoft 365 in the browser (Microsoft Graph Command Line Tools, read-only use)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			fmt.Fprintln(os.Stderr, "Opening the Microsoft sign-in page…")
			acc, err := e.M365Connect(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("connected as %s (%s)\n", acc.Username, acc.Name)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show the connection",
		RunE: func(*cobra.Command, []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			st := e.M365State()
			switch {
			case !st.Connected:
				fmt.Println("not connected (musubi m365 connect)")
			default:
				fmt.Printf("connected as %s since %s\n", st.Account.Username, st.Account.ConnectedAt.Format("2006-01-02 15:04"))
			}
			if !st.Enabled {
				fmt.Println("syncing is off ([m365] enabled = false)")
			}
			return nil
		},
	})
	var days int
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Read Microsoft 365 into the inbox now (then run update)",
		Long: `Read Microsoft 365 into the inbox now (then run update).

--days reads the calendar, and the Teams transcripts of its meetings, that
many days back for this run only: to bring in history from before the
first sync. The meetings it adds are older than what is threaded, so they
wait for a rethread (Status → Work queue, or musubi knowledge --redo threads).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			if days > 0 {
				e.Cfg.M365.PastDays = days
			}
			rep, err := e.SyncM365(cmd.Context())
			if rep != "" {
				fmt.Println(rep)
			}
			return err
		},
	}
	sync.Flags().IntVar(&days, "days", 0, "read this many days back, this run only (0: [m365] past_days)")
	c.AddCommand(sync)
	c.AddCommand(&cobra.Command{
		Use:   "signout",
		Short: "Forget the Microsoft 365 sign-in (Keychain)",
		RunE: func(*cobra.Command, []string) error {
			e, err := open()
			if err != nil {
				return err
			}
			defer e.Close()
			if err := e.M365SignOut(); err != nil {
				return err
			}
			fmt.Println("signed out; what was already read stays in Musubi")
			return nil
		},
	})
	return c
}

func nameMeetingsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "name-meetings",
		Short: "Name meetings that have no calendar event and only a generic title, from their digests (digest model; up to 60 per run)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			n, err := e.NameMeetings(cmd.Context(), os.Stderr)
			fmt.Printf("%d meetings named\n", n)
			return err
		},
	}
}

func peopleCmd() *cobra.Command {
	c := &cobra.Command{Use: "people", Short: "People the owner meets, most met first (owner only)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			rows, err := e.People()
			if err != nil {
				return err
			}
			for _, r := range rows[:min(40, len(rows))] {
				fmt.Printf("%4d  %-30s %-28s %s\n", r.Together, r.Name, r.Title, r.ID)
			}
			fmt.Printf("%d people\n", len(rows))
			return nil
		}}
	var headshotOut string
	show := &cobra.Command{Use: "show <entity id>", Args: cobra.ExactArgs(1), Short: "A person's page as JSON (the headshot shortened; --headshot saves it)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := engine.Open(cfgPath)
			if err != nil {
				return err
			}
			defer e.Close()
			pg, err := e.PersonPage(args[0])
			if err != nil || pg == nil {
				return err
			}
			if headshotOut != "" && pg.Headshot != "" {
				if b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(pg.Headshot, "data:image/jpeg;base64,")); err == nil {
					if err := os.WriteFile(headshotOut, b, 0o600); err != nil {
						return err
					}
				}
			}
			if len(pg.Headshot) > 60 {
				pg.Headshot = fmt.Sprintf("%s… (%d bytes)", pg.Headshot[:30], len(pg.Headshot))
			}
			b, _ := json.MarshalIndent(pg, "", "  ")
			fmt.Println(string(b))
			return nil
		}}
	show.Flags().StringVar(&headshotOut, "headshot", "", "write the headshot JPEG here")
	c.AddCommand(show)
	return c
}
