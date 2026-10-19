package main

import "testing"

func TestOperatorAcceptanceConfig(t *testing.T) {
	for _, tc := range []struct {
		name, path, uid, agent string
		bad                    bool
	}{
		{name: "disabled"},
		{name: "enabled", path: "/tmp/operator.sock", uid: "1000", agent: "/tmp/agent.sock"},
		{name: "root UID is valid", path: "/tmp/operator.sock", uid: "0"},
		{name: "UID required", path: "/tmp/operator.sock", bad: true},
		{name: "UID numeric", path: "/tmp/operator.sock", uid: "someone", bad: true},
		{name: "UID nonnegative", path: "/tmp/operator.sock", uid: "-1", bad: true},
		{name: "distinct sockets", path: "/tmp/same.sock", uid: "1000", agent: "/tmp/same.sock", bad: true},
		{name: "cleaned paths differ", path: "/tmp/sub/../same.sock", uid: "1000", agent: "/tmp/same.sock", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"COLLAB_OPERATOR_SOCKET_PATH": tc.path, "COLLAB_OPERATOR_UID": tc.uid, "COLLAB_SOCKET_PATH": tc.agent}
			// An empty value in this table means the variable is unset.
			cfg, e := loadConfig(func(key string) (string, bool) { return env[key], env[key] != "" })
			if (e != nil) != tc.bad {
				t.Fatalf("config error %v want rejected=%v", e, tc.bad)
			}
			if !tc.bad && cfg.OperatorSocketPath != tc.path {
				t.Fatal("operator enabled/disabled state changed")
			}
			if tc.name == "enabled" && cfg.OperatorUID != 1000 {
				t.Fatal("configured UID lost")
			}
		})
	}
}
