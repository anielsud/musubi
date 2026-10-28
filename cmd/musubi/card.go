package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
)

func cardCmd() *cobra.Command {
	var scope string
	var terms, list bool
	var at string
	c := &cobra.Command{
		Use:   "card <words or topic id>",
		Short: "Show the card the live panel would show for what was said",
		Args:  cobra.MinimumNArgs(0),
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
			asOf, before := time.Now(), time.Time{}
			if at != "" {
				if asOf, err = time.ParseInLocation("2006-01-02", at, time.Local); err != nil {
					return err
				}
				before = asOf
			}
			cs, err := e.LoadCardsBefore(before)
			if err != nil {
				return err
			}
			if list {
				for _, t := range cs.Index.AllTerms() {
					fmt.Printf("%-24s topic %-4d → %s\n", t.Term, t.Topic, t.Anchor.Key())
				}
				return nil
			}
			text := strings.Join(args, " ")
			var a knowledge.Anchor
			how := "topic id"
			if id, err := strconv.ParseInt(text, 10, 64); err == nil {
				a.Topic = id
			} else if a, how, err = cs.Find(context.Background(), text, terms); err != nil {
				return err
			}
			if a.IsZero() {
				return fmt.Errorf("no card for %q", text)
			}
			card, err := cs.Card(sc, a, asOf)
			if err != nil {
				return err
			}
			if card == nil {
				fmt.Printf("%s (%s): nothing visible in scope %s\n", a.Key(), how, sc.Name)
				return nil
			}
			printCard(card, how)
			return nil
		},
	}
	c.Flags().StringVar(&scope, "scope", "owner", "who the card is for")
	c.Flags().BoolVar(&terms, "terms-only", false, "only trigger terms, as the live fast lane")
	c.Flags().BoolVar(&list, "terms", false, "list every trigger term and its topic")
	c.Flags().StringVar(&at, "at", "", "as of this date (YYYY-MM-DD): what was known then, due dates relative to it")
	return c
}

func printCard(c *knowledge.Card, how string) {
	lock := ""
	if c.Collapsed {
		lock = "  [collapsed: " + c.Label.Level.String() + " " + c.Label.Categories.String() + "]"
	}
	fmt.Printf("%s  (%s, %s)%s\n", c.Title, c.Anchor.Key(), how, lock)
	fmt.Printf("last discussed %s · %d meeting(s) · terms %v · topics %v\n", c.Last.Format("2 Jan"), c.Meetings, c.Terms, c.Topics)
	for _, s := range c.Sections {
		fmt.Printf("\n%s\n", strings.ToUpper(s.Name))
		for _, l := range s.Lines {
			extra := ""
			if l.Due != "" {
				extra += " [" + l.Due + "]"
			}
			if l.Note != "" {
				extra += " [" + l.Note + "]"
			}
			fmt.Printf("  - %s%s\n      %s · %s · %s · claim %d · %.2f\n", l.Text, extra, l.Kind, l.Who, l.Said.Format("2 Jan"), l.ClaimID, l.Salience)
		}
	}
	if len(c.People) > 0 {
		fmt.Printf("\nPEOPLE  %s\n", strings.Join(c.People, ", "))
	}
}
