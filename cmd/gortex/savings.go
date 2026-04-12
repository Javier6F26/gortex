package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/savings"
)

var (
	savingsModel    string
	savingsJSON     bool
	savingsReset    bool
	savingsCacheDir string
)

var savingsCmd = &cobra.Command{
	Use:   "savings",
	Short: "Show cumulative token savings + cost avoided across sessions",
	Long: `Shows cumulative token-savings totals persisted across server restarts.
Savings accumulate every time a source-reading MCP tool (get_symbol_source,
batch_symbols, get_editing_context, get_file_summary, smart_context) avoids
a full-file read. Cost is computed against list prices for popular models.

The underlying store lives at ~/.cache/gortex/savings.json (or --cache-dir/savings.json).
Override pricing by exporting GORTEX_MODEL_PRICING_JSON.`,
	RunE: runSavings,
}

func init() {
	savingsCmd.Flags().StringVar(&savingsModel, "model", "", "highlight a single model in text output (default: show all)")
	savingsCmd.Flags().BoolVar(&savingsJSON, "json", false, "emit machine-readable JSON")
	savingsCmd.Flags().BoolVar(&savingsReset, "reset", false, "wipe cumulative totals and exit")
	savingsCmd.Flags().StringVar(&savingsCacheDir, "cache-dir", "", "override graph cache directory (savings.json lives here)")
	rootCmd.AddCommand(savingsCmd)
}

func runSavings(_ *cobra.Command, _ []string) error {
	path := savings.DefaultPath()
	if savingsCacheDir != "" {
		path = filepath.Join(savingsCacheDir, "savings.json")
	}

	store, err := savings.Open(path)
	if err != nil {
		return fmt.Errorf("open savings store: %w", err)
	}

	if savingsReset {
		if err := store.Reset(); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[gortex savings] reset cumulative totals at %s\n", path)
		return nil
	}

	snap := store.Snapshot()

	if savingsJSON {
		return emitSavingsJSON(snap, path)
	}
	emitSavingsText(snap, path)
	return nil
}

func emitSavingsJSON(snap savings.File, path string) error {
	out := map[string]any{
		"path":             path,
		"first_seen":       snap.FirstSeen.Format(time.RFC3339),
		"last_updated":     snap.LastUpdated.Format(time.RFC3339),
		"tokens_saved":     snap.Totals.TokensSaved,
		"tokens_returned":  snap.Totals.TokensReturned,
		"calls_counted":    snap.Totals.CallsCounted,
		"cost_avoided_usd": savings.CostAvoidedAll(snap.Totals.TokensSaved),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func emitSavingsText(snap savings.File, path string) {
	fmt.Printf("Gortex Token Savings\n")
	fmt.Printf("====================\n\n")
	fmt.Printf("Store:          %s\n", path)
	if !snap.FirstSeen.IsZero() {
		fmt.Printf("Tracking since: %s\n", snap.FirstSeen.Format("2006-01-02 15:04"))
	}
	if !snap.LastUpdated.IsZero() {
		fmt.Printf("Last updated:   %s\n", snap.LastUpdated.Format("2006-01-02 15:04"))
	}
	fmt.Println()

	if snap.Totals.CallsCounted == 0 {
		fmt.Println("No source-reading tool calls recorded yet.")
		fmt.Println("Run `gortex serve` and use get_symbol_source / batch_symbols / smart_context.")
		return
	}

	fmt.Printf("Calls counted:   %d\n", snap.Totals.CallsCounted)
	fmt.Printf("Tokens returned: %s\n", humanInt(snap.Totals.TokensReturned))
	fmt.Printf("Tokens saved:    %s\n", humanInt(snap.Totals.TokensSaved))
	if snap.Totals.TokensReturned > 0 {
		ratio := float64(snap.Totals.TokensSaved+snap.Totals.TokensReturned) / float64(snap.Totals.TokensReturned)
		fmt.Printf("Efficiency:      %.1fx\n", ratio)
	}
	fmt.Println()

	costs := savings.CostAvoidedAll(snap.Totals.TokensSaved)
	fmt.Println("Cost avoided (tokens saved × input-price, USD):")
	if savingsModel != "" {
		// Highlight a single model.
		amount := savings.CostAvoided(snap.Totals.TokensSaved, savingsModel)
		fmt.Printf("  %-20s $%.4f\n", savingsModel, amount)
		return
	}
	// Stable ordering so output is diffable.
	names := make([]string, 0, len(costs))
	for n := range costs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-20s $%.4f\n", n, costs[n])
	}

	if len(snap.PerRepo) > 0 {
		fmt.Println()
		fmt.Println("Per-repo totals:")
		repos := make([]string, 0, len(snap.PerRepo))
		for r := range snap.PerRepo {
			repos = append(repos, r)
		}
		sort.Strings(repos)
		for _, r := range repos {
			t := snap.PerRepo[r]
			fmt.Printf("  %s\n    tokens_saved=%s calls=%d\n",
				r, humanInt(t.TokensSaved), t.CallsCounted)
		}
	}
}

// humanInt renders a number with thousands separators so big totals are readable.
func humanInt(n int64) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas every 3 digits from the right.
	out := make([]byte, 0, len(s)+len(s)/3)
	prefix := len(s) % 3
	if prefix > 0 {
		out = append(out, s[:prefix]...)
		if len(s) > prefix {
			out = append(out, ',')
		}
	}
	for i := prefix; i < len(s); i += 3 {
		out = append(out, s[i:i+3]...)
		if i+3 < len(s) {
			out = append(out, ',')
		}
	}
	return string(out)
}
