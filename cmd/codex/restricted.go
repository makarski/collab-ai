package main

import "errors"

type restrictedMode struct {
	enabled    bool
	budgetName string
}

func (mode restrictedMode) validate(terminal bool, relay string, args []string) error {
	if !mode.enabled {
		return nil
	}
	if mode.budgetName == "" {
		return errors.New("--restricted-operator requires --budget bound by the administrator")
	}
	return restrictedStdio(terminal, relay, args)
}

func restrictedStdio(terminal bool, relay string, args []string) error {
	if terminal || relay != "" {
		return errors.New("--restricted-operator supports only stdio; terminal and MCP relay are not supported")
	}
	if len(args) != 0 {
		return errors.New("--restricted-operator does not accept forwarded arguments")
	}
	return nil
}
