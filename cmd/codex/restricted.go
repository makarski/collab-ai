package main

import "errors"

func validateRestrictedMode(enabled bool, budgetName string, terminal bool, relay string, args []string) error {
	if !enabled {
		return nil
	}
	if budgetName == "" {
		return errors.New("--restricted-operator requires --budget bound by the administrator")
	}
	if terminal || relay != "" || len(args) != 0 {
		return errors.New("--restricted-operator supports only stdio; terminal, MCP relay and forwarded arguments are not supported")
	}
	return nil
}
