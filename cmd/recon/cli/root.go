// Package cli provides recon's cobra command tree, usable both as a standalone
// CLI and when embedded in other tools (e.g. rivet).
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/djtouchette/recon/pkg/recon"
	"github.com/spf13/cobra"
)

var (
	flagHuman    bool
	flagRoot     string
	flagCacheDir string
)

// NewRootCmd returns the fully constructed recon command tree.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "recon",
		Short: "Fast, deterministic repo intelligence",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagRoot == "" {
				var err error
				flagRoot, err = os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "recon: get working directory: %v\n", err)
					os.Exit(1)
				}
			}
		},
	}

	root.PersistentFlags().BoolVar(&flagHuman, "human", false, "human-readable output")
	root.PersistentFlags().StringVar(&flagRoot, "root", "", "repo root (default: cwd)")
	root.PersistentFlags().StringVar(&flagCacheDir, "cache-dir", "", "cache directory (default: <root>/.recon/)")

	root.AddCommand(overviewCmd())
	root.AddCommand(relatedCmd())
	root.AddCommand(testsCmd())
	root.AddCommand(symbolsCmd())
	root.AddCommand(searchCmd())
	root.AddCommand(contextCmd())
	root.AddCommand(hotspotsCmd())
	root.AddCommand(changesCmd())
	root.AddCommand(refreshCmd())
	root.AddCommand(rebuildCmd())
	root.AddCommand(grepCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Version info",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "recon %s\n", version)
		},
	})

	return root
}

// newRecon creates a Recon instance with the current flag values.
func newRecon() (*recon.Recon, error) {
	var opts []recon.Option
	if flagCacheDir != "" {
		opts = append(opts, recon.WithCacheDir(flagCacheDir))
	}
	return recon.New(flagRoot, opts...)
}

func overviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "overview",
		Short: "Structured repo summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			start := time.Now()
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			ov, err := r.Overview()
			if err != nil {
				return err
			}

			if flagHuman {
				printOverviewHuman(cmd, ov, time.Since(start))
				return nil
			}
			return outputJSON(cmd, ov)
		},
	}
}

func relatedCmd() *cobra.Command {
	var maxResults int
	cmd := &cobra.Command{
		Use:   "related <path>",
		Short: "Find related files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			results, err := r.Related(args[0], recon.WithMaxResults(maxResults))
			if err != nil {
				return err
			}

			if flagHuman {
				printRelatedHuman(cmd, results)
				return nil
			}
			return outputJSON(cmd, results)
		},
	}
	cmd.Flags().IntVarP(&maxResults, "max", "n", 20, "max results")
	return cmd
}

func testsCmd() *cobra.Command {
	var maxResults int
	cmd := &cobra.Command{
		Use:   "tests <path>",
		Short: "Find test files for a path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			tests, err := r.Tests(args[0], maxResults)
			if err != nil {
				return err
			}

			if flagHuman {
				printTestsHuman(cmd, tests)
				return nil
			}
			return outputJSON(cmd, tests)
		},
	}
	cmd.Flags().IntVarP(&maxResults, "max", "n", 20, "max results")
	return cmd
}

func symbolsCmd() *cobra.Command {
	var maxResults int
	cmd := &cobra.Command{
		Use:   "symbols [query]",
		Short: "Search symbols (functions, classes, types). Use 'file:path' to list symbols in a file.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			symbols, err := r.Symbols(query, maxResults)
			if err != nil {
				return err
			}

			if flagHuman {
				printSymbolsHuman(cmd, symbols)
				return nil
			}
			return outputJSON(cmd, symbols)
		},
	}
	cmd.Flags().IntVarP(&maxResults, "max", "n", 30, "max results")
	return cmd
}

func searchCmd() *cobra.Command {
	var maxResults int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Unified search across symbols, file paths, and file previews",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			results, err := r.Search(args[0], maxResults)
			if err != nil {
				return err
			}

			if flagHuman {
				printSearchHuman(cmd, results)
				return nil
			}
			return outputJSON(cmd, results)
		},
	}
	cmd.Flags().IntVarP(&maxResults, "max", "n", 30, "max results")
	return cmd
}

func contextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context <path>",
		Short: "File context: preview, owners, metrics, nearby configs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			ctx, err := r.Context(args[0])
			if err != nil {
				return err
			}

			if flagHuman {
				printContextHuman(cmd, ctx)
				return nil
			}
			return outputJSON(cmd, ctx)
		},
	}
}

func hotspotsCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "hotspots",
		Short: "Top files by hotspot score (fan-in * churn) — risky to change",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			spots, err := r.Hotspots(n)
			if err != nil {
				return err
			}

			if flagHuman {
				printHotspotsHuman(cmd, spots)
				return nil
			}
			return outputJSON(cmd, spots)
		},
	}
	cmd.Flags().IntVarP(&n, "max", "n", 20, "max results")
	return cmd
}

func changesCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "changes",
		Short: "Recent change summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			changes, err := r.RecentChanges(since)
			if err != nil {
				return err
			}

			if flagHuman {
				printChangesHuman(cmd, changes)
				return nil
			}
			return outputJSON(cmd, changes)
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "time range (e.g., 7d, 2w, 1m)")
	return cmd
}

func refreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Incremental cache update",
		RunE: func(cmd *cobra.Command, args []string) error {
			start := time.Now()
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			if err := r.Refresh(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "refreshed in %v\n", time.Since(start))
			return nil
		},
	}
}

func rebuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Full rescan from scratch",
		RunE: func(cmd *cobra.Command, args []string) error {
			start := time.Now()
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			if err := r.Rebuild(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "rebuilt in %v\n", time.Since(start))
			return nil
		},
	}
}

// --- Output helpers ---
// All output goes through cmd.OutOrStdout() so embedded execution captures it.

func outputJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printOverviewHuman(cmd *cobra.Command, ov *recon.Overview, elapsed time.Duration) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Repository: %s\n", ov.Root)
	fmt.Fprintf(w, "Files: %d (tests: %d)\n", ov.FileCount, ov.TestCount)
	fmt.Fprintf(w, "Scanned in: %v\n\n", elapsed)

	if len(ov.Languages) > 0 {
		fmt.Fprintln(w, "Languages:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, l := range ov.Languages {
			fmt.Fprintf(tw, "  %s\t%d files\t%.1f%%\t%s\n",
				l.Name, l.FileCount, l.Percentage, strings.Join(l.Extensions, ", "))
		}
		tw.Flush()
		fmt.Fprintln(w)
	}

	if len(ov.Frameworks) > 0 {
		fmt.Fprintln(w, "Frameworks:")
		for _, f := range ov.Frameworks {
			fmt.Fprintf(w, "  %s (%s) — %s\n", f.Name, f.Language, f.Evidence)
		}
		fmt.Fprintln(w)
	}

	if len(ov.Structure) > 0 {
		fmt.Fprintln(w, "Structure:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, d := range ov.Structure {
			langs := strings.Join(d.Languages, ", ")
			fmt.Fprintf(tw, "  %s/\t%d files\t[%s]\t%s\n",
				d.Path, d.FileCount, d.Purpose, langs)
		}
		tw.Flush()
		fmt.Fprintln(w)
	}

	if len(ov.Entrypoints) > 0 {
		fmt.Fprintln(w, "Entrypoints:")
		for _, e := range ov.Entrypoints {
			fmt.Fprintf(w, "  %s (%s)\n", e.Path, e.Kind)
		}
		fmt.Fprintln(w)
	}
}

func printRelatedHuman(cmd *cobra.Command, results []recon.RelatedFile) {
	w := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintln(w, "No related files found.")
		return
	}
	fmt.Fprintln(w, "Related files:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range results {
		fmt.Fprintf(tw, "  %.2f\t%s\t[%s]\n", r.Score, r.Path, strings.Join(r.Signals, ", "))
	}
	tw.Flush()
}

func printSymbolsHuman(cmd *cobra.Command, symbols []recon.SymbolInfo) {
	w := cmd.OutOrStdout()
	if len(symbols) == 0 {
		fmt.Fprintln(w, "No symbols found.")
		return
	}
	fmt.Fprintf(w, "Symbols (%d):\n", len(symbols))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range symbols {
		fmt.Fprintf(tw, "  %s\t%s\t%s:%d\t%s\n", s.Kind, s.Name, s.File, s.Line, s.Signature)
	}
	tw.Flush()
}

func printSearchHuman(cmd *cobra.Command, results []recon.SearchResult) {
	w := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintln(w, "No results found.")
		return
	}
	fmt.Fprintf(w, "Results (%d):\n", len(results))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range results {
		ctx := r.Context
		if len(ctx) > 100 {
			ctx = ctx[:100] + "..."
		}
		fmt.Fprintf(tw, "  %.2f\t[%s]\t%s\t%s\n", r.Score, r.MatchType, r.Path, ctx)
	}
	tw.Flush()
}

func printContextHuman(cmd *cobra.Command, ctx *recon.FileContext) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "File: %s\n", ctx.Path)
	if ctx.ContentHash != "" {
		fmt.Fprintf(w, "Hash: %s\n", ctx.ContentHash)
	}
	fmt.Fprintln(w)

	if ctx.Preview != "" {
		fmt.Fprintln(w, "Preview:")
		for _, line := range strings.Split(ctx.Preview, "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
		fmt.Fprintln(w)
	}

	if len(ctx.Owners) > 0 {
		fmt.Fprintf(w, "Owners: %s\n", strings.Join(ctx.Owners, ", "))
	}

	fmt.Fprintf(w, "Fan-in: %d  Fan-out: %d  Churn: %d  Hotspot: %.2f\n",
		ctx.FanIn, ctx.FanOut, ctx.Churn, ctx.HotspotScore)

	if len(ctx.NearbyConfigs) > 0 {
		fmt.Fprintln(w, "\nNearby configs:")
		for typ, path := range ctx.NearbyConfigs {
			fmt.Fprintf(w, "  %-20s %s\n", typ, path)
		}
	}
}

func printHotspotsHuman(cmd *cobra.Command, spots []recon.HotspotInfo) {
	w := cmd.OutOrStdout()
	if len(spots) == 0 {
		fmt.Fprintln(w, "No hotspots found.")
		return
	}
	fmt.Fprintf(w, "Hotspots (%d):\n", len(spots))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", "SCORE", "FAN-IN", "CHURN", "FAN-OUT", "FILE")
	for _, s := range spots {
		fmt.Fprintf(tw, "  %.2f\t%d\t%d\t%d\t%s\n", s.HotspotScore, s.FanIn, s.Churn, s.FanOut, s.Path)
	}
	tw.Flush()
}

func printTestsHuman(cmd *cobra.Command, tests []recon.TestFile) {
	w := cmd.OutOrStdout()
	if len(tests) == 0 {
		fmt.Fprintln(w, "No test files found.")
		return
	}
	fmt.Fprintln(w, "Test files:")
	for _, t := range tests {
		fmt.Fprintf(w, "  %s (%s)", t.Path, t.Kind)
		if t.ForFile != "" {
			fmt.Fprintf(w, " — covers %s", t.ForFile)
		}
		fmt.Fprintln(w)
	}
}

func grepCmd() *cobra.Command {
	var (
		maxFiles   int
		typeFilter string
	)
	cmd := &cobra.Command{
		Use:   "grep <pattern>",
		Short: "Enriched grep — matches grouped by file with definitions, metrics, and classification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRecon()
			if err != nil {
				return err
			}
			defer r.Close()

			result, err := r.Grep(args[0], recon.GrepOptions{
				MaxFiles:   maxFiles,
				TypeFilter: typeFilter,
			})
			if err != nil {
				return err
			}

			if flagHuman {
				printGrepHuman(cmd, result)
				return nil
			}
			return outputJSON(cmd, result)
		},
	}
	cmd.Flags().IntVarP(&maxFiles, "max", "n", 20, "max files to show")
	cmd.Flags().StringVarP(&typeFilter, "type", "t", "", "filter by match type: definition, reference, test, comment")
	return cmd
}

func printGrepHuman(cmd *cobra.Command, result *recon.GrepResult) {
	w := cmd.OutOrStdout()
	s := result.Summary
	if s.Total == 0 {
		fmt.Fprintln(w, "No matches.")
		return
	}

	fmt.Fprintf(w, "%d matches across %d files", s.Total, s.Files)
	fmt.Fprintf(w, " (%d def, %d ref, %d test, %d comment)", s.Definitions, s.References, s.Tests, s.Comments)
	if s.Truncated > 0 {
		fmt.Fprintf(w, " — showing %d files, %d more with --max %d", len(result.Files), s.Truncated, s.Files)
	}
	fmt.Fprintln(w)

	for _, f := range result.Files {
		fmt.Fprintf(w, "\n  %s  (fan-in: %d, hotspot: %.2f)\n", f.Path, f.FanIn, f.HotspotScore)
		for _, m := range f.Matches {
			text := m.Text
			if len(text) > 90 {
				text = text[:90] + "..."
			}
			suffix := ""
			if m.Similar > 0 {
				suffix = fmt.Sprintf("  ... +%d similar", m.Similar)
			}
			fmt.Fprintf(w, "    %4d  %-10s  %s%s\n", m.Line, m.MatchType, text, suffix)
		}
	}
}

func printChangesHuman(cmd *cobra.Command, changes []recon.ChangeSet) {
	w := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintln(w, "No recent changes.")
		return
	}
	fmt.Fprintf(w, "Recent changes (%d commits):\n\n", len(changes))
	for _, c := range changes {
		fmt.Fprintf(w, "  %s %s (%s)\n", c.Hash[:8], c.Message, c.Author)
		fmt.Fprintf(w, "    %s — areas: %s\n", c.Date, strings.Join(c.Areas, ", "))
		if len(c.Files) <= 5 {
			for _, f := range c.Files {
				fmt.Fprintf(w, "      %s\n", f)
			}
		} else {
			for _, f := range c.Files[:3] {
				fmt.Fprintf(w, "      %s\n", f)
			}
			fmt.Fprintf(w, "      ... and %d more\n", len(c.Files)-3)
		}
		fmt.Fprintln(w)
	}
}
