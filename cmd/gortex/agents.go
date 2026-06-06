package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/agents"
)

// agents.go is the `gortex agents` command tree. Its primary job is the
// skill-render drift fence (`gortex agents render`): render every agent
// adapter's generated artifacts so drift across all platforms — not
// just the committed Claude plugin bundle — is reviewable.

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "Inspect and validate Gortex agent integrations",
}

var (
	agentsRenderTarget string
	agentsRenderCheck  bool
)

var agentsRenderCmd = &cobra.Command{
	Use:   "render",
	Short: "Render every adapter's generated artifacts (skill-render drift fence)",
	Long: "Render the artifacts every agent adapter would install into a " +
		"normalized, machine-independent manifest per adapter. Use --target " +
		"to dump them for review (or to refresh the drift-test goldens), and " +
		"--check as a CI gate that every adapter still renders a gortex " +
		"registration.",
	Args: cobra.NoArgs,
	RunE: runAgentsRender,
}

func init() {
	agentsRenderCmd.Flags().StringVar(&agentsRenderTarget, "target", "",
		"write each adapter's manifest to <target>/<adapter>.txt")
	agentsRenderCmd.Flags().BoolVar(&agentsRenderCheck, "check", false,
		"fail if any adapter renders nothing or drops its gortex registration")
	agentsCmd.AddCommand(agentsRenderCmd)
	rootCmd.AddCommand(agentsCmd)
}

func runAgentsRender(cmd *cobra.Command, _ []string) error {
	reg := buildRegistry()
	manifests, err := agents.RenderManifest(reg.All())
	if err != nil {
		return err
	}
	names := make([]string, 0, len(manifests))
	for n := range manifests {
		names = append(names, n)
	}
	sort.Strings(names)

	if agentsRenderTarget != "" {
		if err := os.MkdirAll(agentsRenderTarget, 0o755); err != nil {
			return err
		}
		for _, n := range names {
			path := filepath.Join(agentsRenderTarget, n+".txt")
			if err := os.WriteFile(path, []byte(manifests[n]), 0o644); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %d adapter manifests to %s\n", len(names), agentsRenderTarget)
	}

	if agentsRenderCheck {
		var problems []string
		for _, n := range names {
			m := manifests[n]
			if strings.TrimSpace(m) == "" {
				problems = append(problems, n+": rendered no artifacts")
				continue
			}
			if !agents.RenderContainsRegistration(m) {
				problems = append(problems, n+": rendered output carries no gortex registration")
			}
		}
		if len(problems) > 0 {
			for _, p := range problems {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "drift: %s\n", p)
			}
			return fmt.Errorf("agents render --check failed for %d adapter(s)", len(problems))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "OK: all %d adapters render a gortex registration\n", len(names))
		return nil
	}

	if agentsRenderTarget == "" {
		for _, n := range names {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-14s %d bytes\n", n, len(manifests[n]))
		}
	}
	return nil
}
