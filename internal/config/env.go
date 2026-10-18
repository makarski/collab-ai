// Package config reads pairtask settings from the environment.
//
// Settings are PAIRTASK_<NAME>. The deprecated COLLAB_<NAME> still applies
// when the canonical variable is absent, with one warning per process.
// Presence decides the source: a canonical variable set to an empty value
// masks the legacy one.
package config

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

const (
	canonicalPrefix = "PAIRTASK_"
	legacyPrefix    = "COLLAB_"
)

// LookupFunc reports a variable's value and whether it is set, like os.LookupEnv.
type LookupFunc func(string) (string, bool)

var warnLegacy sync.Once

// Lookup reads the setting name (without prefix) from the process environment.
func Lookup(name string) (value string, present, legacyUsed bool) {
	return LookupIn(os.LookupEnv, name)
}

// LookupIn reads the setting name through lookup. It warns on stderr, once
// per process and without the value, when only the legacy variable is set.
func LookupIn(lookup LookupFunc, name string) (value string, present, legacyUsed bool) {
	if value, ok := lookup(canonicalPrefix + name); ok {
		return value, true, false
	}
	if value, ok := lookup(legacyPrefix + name); ok {
		warnLegacy.Do(func() {
			fmt.Fprintln(os.Stderr, "pairtask: COLLAB_* environment variables are deprecated; rename them to PAIRTASK_*")
		})
		return value, true, true
	}
	return "", false, false
}

// Required is a setting that has a default: absent, or an empty legacy
// variable (its old meaning), gives fallback; an empty canonical variable is
// an error rather than a silent default.
func Required(lookup LookupFunc, name, fallback string) (string, error) {
	value, present, legacyUsed := LookupIn(lookup, name)
	switch {
	case !present, legacyUsed && value == "":
		return fallback, nil
	case value == "":
		return "", errors.New(Name(name) + " is set but empty; unset it to use the default")
	}
	return value, nil
}

// Name is the canonical variable name of a setting, for messages.
func Name(name string) string {
	return canonicalPrefix + name
}
