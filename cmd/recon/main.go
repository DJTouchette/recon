package main

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
	flagHuman bool
	flagRoot  string
)

func main() {
	root := &cobra.Command{
		Use:   "recon",
		Short: "Fast, deterministic repo intelligence",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagRoot == "" {
				var err error
				flagRoot, err = os.Getwd()
				if err != nil {
					fatal("get working directory: %v", err)
				}
			}
		},
	}

	root.PersistentFlags().BoolVar(&flagHuman, "human", false, "human-readable output")
	root.PersistentFlags().StringVar(&flagRoot, "root", "", "repo root (default: cwd)")

	root.AddCommand(overviewCmd())
	root.AddCommand(relatedCmd())
	root.AddCommand(testsCmd())
	root.AddCommand(symbolsCmd())
	root.AddCommand(changesCmd())
	root.AddCommand(refreshCmd())
	root.AddCommand(rebuildCmd())
	root.AddCommand(versionCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func overviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "overview",
		Short: "Structured repo summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			start := time.Now()
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			ov, err := r.Overview()
			if err != nil {
				return err
			}

			if flagHuman {
				printOverviewHuman(ov, time.Since(start))
				return nil
			}
			return outputJSON(ov)
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
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			results, err := r.Related(args[0], recon.WithMaxResults(maxResults))
			if err != nil {
				return err
			}

			if flagHuman {
				printRelatedHuman(results)
				return nil
			}
			return outputJSON(results)
		},
	}
	cmd.Flags().IntVarP(&maxResults, "max", "n", 20, "max results")
	return cmd
}

func testsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tests <path>",
		Short: "Find test files for a path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			tests, err := r.Tests(args[0])
			if err != nil {
				return err
			}

			if flagHuman {
				printTestsHuman(tests)
				return nil
			}
			return outputJSON(tests)
		},
	}
}

func symbolsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "symbols [query]",
		Short: "Search symbols (functions, classes, types). Use 'file:path' to list symbols in a file.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			symbols, err := r.Symbols(query)
			if err != nil {
				return err
			}

			if flagHuman {
				printSymbolsHuman(symbols)
				return nil
			}
			return outputJSON(symbols)
		},
	}
}

func changesCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "changes",
		Short: "Recent change summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			changes, err := r.RecentChanges(since)
			if err != nil {
				return err
			}

			if flagHuman {
				printChangesHuman(changes)
				return nil
			}
			return outputJSON(changes)
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
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			if err := r.Refresh(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "refreshed in %v\n", time.Since(start))
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
			r, err := recon.New(flagRoot)
			if err != nil {
				return err
			}
			defer r.Close()

			if err := r.Rebuild(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "rebuilt in %v\n", time.Since(start))
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Version info",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("recon v0.1.0")
		},
	}
}

// Output helpers

func outputJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printOverviewHuman(ov *recon.Overview, elapsed time.Duration) {
	fmt.Printf("Repository: %s\n", ov.Root)
	fmt.Printf("Files: %d (tests: %d)\n", ov.FileCount, ov.TestCount)
	fmt.Printf("Scanned in: %v\n\n", elapsed)

	if len(ov.Languages) > 0 {
		fmt.Println("Languages:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, l := range ov.Languages {
			fmt.Fprintf(w, "  %s\t%d files\t%.1f%%\t%s\n",
				l.Name, l.FileCount, l.Percentage, strings.Join(l.Extensions, ", "))
		}
		w.Flush()
		fmt.Println()
	}

	if len(ov.Frameworks) > 0 {
		fmt.Println("Frameworks:")
		for _, f := range ov.Frameworks {
			fmt.Printf("  %s (%s) — %s\n", f.Name, f.Language, f.Evidence)
		}
		fmt.Println()
	}

	if len(ov.Structure) > 0 {
		fmt.Println("Structure:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, d := range ov.Structure {
			langs := ""
			if len(d.Languages) > 0 {
				langs = strings.Join(d.Languages, ", ")
			}
			fmt.Fprintf(w, "  %s/\t%d files\t[%s]\t%s\n",
				d.Path, d.FileCount, d.Purpose, langs)
		}
		w.Flush()
		fmt.Println()
	}

	if len(ov.Entrypoints) > 0 {
		fmt.Println("Entrypoints:")
		for _, e := range ov.Entrypoints {
			fmt.Printf("  %s (%s)\n", e.Path, e.Kind)
		}
		fmt.Println()
	}
}

func printRelatedHuman(results []recon.RelatedFile) {
	if len(results) == 0 {
		fmt.Println("No related files found.")
		return
	}
	fmt.Println("Related files:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range results {
		fmt.Fprintf(w, "  %.2f\t%s\t[%s]\n", r.Score, r.Path, strings.Join(r.Signals, ", "))
	}
	w.Flush()
}

func printSymbolsHuman(symbols []recon.SymbolInfo) {
	if len(symbols) == 0 {
		fmt.Println("No symbols found.")
		return
	}
	fmt.Printf("Symbols (%d):\n", len(symbols))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range symbols {
		fmt.Fprintf(w, "  %s\t%s\t%s:%d\t%s\n", s.Kind, s.Name, s.File, s.Line, s.Signature)
	}
	w.Flush()
}

func printTestsHuman(tests []recon.TestFile) {
	if len(tests) == 0 {
		fmt.Println("No test files found.")
		return
	}
	fmt.Println("Test files:")
	for _, t := range tests {
		fmt.Printf("  %s (%s)", t.Path, t.Kind)
		if t.ForFile != "" {
			fmt.Printf(" — covers %s", t.ForFile)
		}
		fmt.Println()
	}
}

func printChangesHuman(changes []recon.ChangeSet) {
	if len(changes) == 0 {
		fmt.Println("No recent changes.")
		return
	}
	fmt.Printf("Recent changes (%d commits):\n\n", len(changes))
	for _, c := range changes {
		fmt.Printf("  %s %s (%s)\n", c.Hash[:8], c.Message, c.Author)
		fmt.Printf("    %s — areas: %s\n", c.Date, strings.Join(c.Areas, ", "))
		if len(c.Files) <= 5 {
			for _, f := range c.Files {
				fmt.Printf("      %s\n", f)
			}
		} else {
			for _, f := range c.Files[:3] {
				fmt.Printf("      %s\n", f)
			}
			fmt.Printf("      ... and %d more\n", len(c.Files)-3)
		}
		fmt.Println()
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "recon: "+format+"\n", args...)
	os.Exit(1)
}
