package main

import (
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/telemetry"
)

func envOn(k string) string {
	if k == telemetry.EnvTelemetry {
		return "on"
	}
	return ""
}

func envOff(string) string { return "" }

func TestCLIRecorderDim(t *testing.T) {
	root := &cobra.Command{Use: "gortex"}
	review := &cobra.Command{Use: "review"}
	daemon := &cobra.Command{Use: "daemon"}
	start := &cobra.Command{Use: "start"}
	root.AddCommand(review)
	root.AddCommand(daemon)
	daemon.AddCommand(start)

	if got := cliCommandDim(review); got != "review" {
		t.Errorf("top-level dim = %q, want review", got)
	}
	if got := cliCommandDim(start); got != "daemon.start" {
		t.Errorf("nested dim = %q, want daemon.start", got)
	}
	if got := cliCommandDim(root); got != "" {
		t.Errorf("bare-root dim = %q, want empty", got)
	}
}

func TestTelemetryCLIRecordsCommand(t *testing.T) {
	store := telemetry.NewStore(t.TempDir())
	root := &cobra.Command{Use: "gortex"}
	sub := &cobra.Command{Use: "review"}
	root.AddCommand(sub)

	recordCLIUsage(sub, store, envOn)

	roll, err := store.Load(telemetry.DayKey(time.Now()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if roll.Counts["cli_command:review"] != 1 {
		t.Errorf("cli_command:review = %d, want 1 (counts=%v)", roll.Counts["cli_command:review"], roll.Counts)
	}
}

func TestTelemetryCLIDisabledRecordsNothing(t *testing.T) {
	store := telemetry.NewStore(t.TempDir())
	root := &cobra.Command{Use: "gortex"}
	sub := &cobra.Command{Use: "index"}
	root.AddCommand(sub)

	recordCLIUsage(sub, store, envOff) // consent default off

	if days, _ := store.Days(); len(days) != 0 {
		t.Errorf("disabled CLI telemetry wrote days %v", days)
	}
}

func TestTelemetryCLIBareRootRecordsNothing(t *testing.T) {
	store := telemetry.NewStore(t.TempDir())
	root := &cobra.Command{Use: "gortex"}

	recordCLIUsage(root, store, envOn) // enabled, but no subcommand ran

	if days, _ := store.Days(); len(days) != 0 {
		t.Errorf("bare-root invocation recorded a command: %v", days)
	}
}
