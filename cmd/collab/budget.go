package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"collab-ai/internal/budget"
)

const budgetUsage = "usage: collab budget create NAME --tokens N [--json]\n       collab budget status NAME [--json]"

type budgetOptions struct {
	action string
	name   string
	tokens int64
	asJSON bool
}

func parseBudgetOptions(args []string, stderr io.Writer) (budgetOptions, error) {
	var cfg budgetOptions
	if len(args) < 2 {
		return cfg, errors.New(budgetUsage)
	}
	cfg.action, cfg.name = args[0], args[1]
	if cfg.action != "create" && cfg.action != "status" {
		return cfg, errors.New(budgetUsage)
	}
	flags := flag.NewFlagSet("budget "+cfg.action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&cfg.asJSON, "json", false, "emit a versioned accounting snapshot")
	if cfg.action == "create" {
		flags.Int64Var(&cfg.tokens, "tokens", 0, "positive soft token cap")
	}
	if err := flags.Parse(args[2:]); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 {
		return cfg, errors.New("unexpected budget arguments")
	}
	return cfg, nil
}

func runBudget(args []string, stdout, stderr io.Writer) int {
	if wantsBudgetHelp(args) {
		fmt.Fprintln(stderr, budgetUsage)
		return 0
	}
	cfg, err := parseBudgetOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	store, err := budget.DefaultStore()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out, err := cfg.execute(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := cfg.write(stdout, out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func wantsBudgetHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[len(args)-1] {
	case "-h", "--help":
		return true
	default:
		return false
	}
}

func (cfg budgetOptions) execute(store budget.Store) (budget.Snapshot, error) {
	if cfg.action == "create" {
		return store.Create(cfg.name, cfg.tokens)
	}
	return store.Status(cfg.name)
}

func (cfg budgetOptions) write(w io.Writer, out budget.Snapshot) error {
	if cfg.asJSON {
		return json.NewEncoder(w).Encode(out)
	}
	_, err := fmt.Fprintf(w, "Budget %s (%s)\nSoft cap: %d\nReported tokens: %d\nRemaining from reports: %d\nReported overshoot: %d\nAccounting: %s\n",
		out.Name, out.State, out.Cap, out.ReportedTokens, out.RemainingTokens, out.OvershootTokens, accountingLabel(out))
	return err
}

func accountingLabel(out budget.Snapshot) string {
	if out.AccountingError != "" {
		return "unknown; " + out.AccountingError
	}
	if !out.UsageReported {
		return "no usage reported yet; actual spend is unknown"
	}
	if out.LastReportedAt == nil {
		return "saved observations; report time unknown"
	}
	return "last report " + out.LastReportedAt.Format(time.RFC3339) + "; later usage may be unreported"
}
