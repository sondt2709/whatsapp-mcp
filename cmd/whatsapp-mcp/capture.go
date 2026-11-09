package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sondt2709/whatsapp-mcp/internal/capture"
	"github.com/sondt2709/whatsapp-mcp/internal/keystore"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// captureCmd writes replay fixtures from the stored messages, with all
// personal content replaced (internal/capture). It is for development.
func captureCmd(o *options) *cobra.Command {
	var out string
	var per int
	cmd := &cobra.Command{
		Use:    "dev-capture",
		Short:  "Write anonymized replay fixtures from the stored messages (development)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			dir, err := filepath.Abs(o.dataDir)
			if err != nil {
				return err
			}
			key, err := keyStore().Get(keystore.Account(dir))
			if err != nil {
				return err
			}
			db, err := store.Open(ctx, dir, key, nil)
			if err != nil {
				return err
			}
			defer db.Close()
			dev, err := db.Container.GetFirstDevice(ctx)
			if err != nil || dev.ID == nil {
				return fmt.Errorf("the data folder is not paired")
			}
			a := capture.New(dev.ID.User, dev.LID.User)
			rows, err := db.Messages(ctx, store.Filter{Limit: 20000})
			if err != nil {
				return err
			}
			// Keep a few rows of each kind, so the fixtures cover the shapes.
			count := map[string]int{}
			var picked []store.Row
			for _, r := range rows {
				kind := r.MediaKind
				switch {
				case kind != "":
				case r.QuoteID != "":
					kind = "quote"
				case strings.HasSuffix(r.Chat, "@g.us"):
					kind = "group-text"
				default:
					kind = "text"
				}
				if r.FromMe {
					kind += "-me"
				}
				if count[kind] < per {
					count[kind]++
					picked = append(picked, r)
				}
			}
			if len(picked) == 0 {
				return fmt.Errorf("no stored messages")
			}
			sort.Slice(picked, func(i, j int) bool { return picked[i].Time.Before(picked[j].Time) })
			shift := capture.Base.Sub(picked[len(picked)-1].Time)
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			enc := json.NewEncoder(f)
			for _, r := range picked {
				e, err := a.Event(r, shift)
				if err != nil {
					return err
				}
				if err := enc.Encode(e); err != nil {
					return err
				}
			}
			kinds := make([]string, 0, len(count))
			for k, n := range count {
				kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
			}
			sort.Strings(kinds)
			fmt.Printf("wrote %d events to %s (%s)\n", len(picked), out, strings.Join(kinds, " "))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output file (JSON lines) (required)")
	cmd.Flags().IntVar(&per, "per-kind", 4, "rows of each kind")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}
