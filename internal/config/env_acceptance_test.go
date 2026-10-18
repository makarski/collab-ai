package config_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/pairtask/pairtask/internal/config"
)

func TestEnvironmentPrecedenceAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name            string
		values          map[string]string
		value           string
		present, legacy bool
	}{
		{name: "absent"},
		{name: "canonical", values: map[string]string{"PAIRTASK_SOCKET_PATH": "/canonical"}, value: "/canonical", present: true},
		{name: "legacy", values: map[string]string{"COLLAB_SOCKET_PATH": "/legacy"}, value: "/legacy", present: true, legacy: true},
		{name: "both", values: map[string]string{"PAIRTASK_SOCKET_PATH": "/canonical", "COLLAB_SOCKET_PATH": "/legacy"}, value: "/canonical", present: true},
		{name: "empty canonical masks legacy", values: map[string]string{"PAIRTASK_SOCKET_PATH": "", "COLLAB_SOCKET_PATH": "/legacy"}, present: true},
		{name: "empty legacy remains present", values: map[string]string{"COLLAB_SOCKET_PATH": ""}, present: true, legacy: true},
		{name: "values are not trimmed", values: map[string]string{"PAIRTASK_SOCKET_PATH": " /with spaces "}, value: " /with spaces ", present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"PAIRTASK_SOCKET_PATH", "COLLAB_SOCKET_PATH"} {
				t.Setenv(key, "") // Register restoration before explicitly removing the key.
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range tc.values {
				t.Setenv(key, value)
			}
			value, present, legacy := config.Lookup("SOCKET_PATH")
			if value != tc.value || present != tc.present || legacy != tc.legacy {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, %v)", value, present, legacy, tc.value, tc.present, tc.legacy)
			}
		})
	}
}

func TestEnvironmentWarningsAcceptance(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		t.Run(fmt.Sprintf("canonical=%v", canonical), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestEnvironmentWarningChild$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "COLLAB_") && !strings.HasPrefix(entry, "PAIRTASK_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "PAIRTASK_ENV_ACCEPTANCE_CHILD=1")
			for _, name := range []string{"SOCKET_PATH", "DB_PATH", "BUDGET_DIR"} {
				cmd.Env = append(cmd.Env, "COLLAB_"+name+"=private-value-"+name)
				if canonical {
					cmd.Env = append(cmd.Env, "PAIRTASK_"+name+"=canonical-value-"+name)
				}
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v: %s", err, stderr.String())
			}
			if stdout.String() != "application output\n" {
				t.Fatalf("stdout polluted: %q", stdout.String())
			}
			if canonical {
				if stderr.Len() != 0 {
					t.Fatalf("unused legacy keys warned: %q", stderr.String())
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
			if len(lines) != 1 || !strings.Contains(strings.ToLower(lines[0]), "deprecat") || !strings.Contains(lines[0], "PAIRTASK_") {
				t.Fatalf("want one actionable deprecation line, got %q", stderr.String())
			}
			if strings.Contains(stderr.String(), "private-value") || strings.Contains(stderr.String(), "canonical-value") {
				t.Fatalf("warning exposed environment value: %q", stderr.String())
			}
		})
	}
}

func TestEnvironmentWarningChild(t *testing.T) {
	if os.Getenv("PAIRTASK_ENV_ACCEPTANCE_CHILD") != "1" {
		return
	}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for _, name := range []string{"SOCKET_PATH", "DB_PATH", "BUDGET_DIR"} {
				config.Lookup(name)
			}
		}()
	}
	workers.Wait()
	fmt.Println("application output")
	os.Exit(0)
}
