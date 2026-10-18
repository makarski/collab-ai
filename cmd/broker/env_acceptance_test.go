package main

import "testing"

func TestBrokerEnvironmentPrecedenceAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		env                        map[string]string
		socket, database, operator string
		uid                        int
		bad                        bool
	}{
		{name: "defaults retained", socket: "/tmp/collab-ai.sock", database: "./collab-ai.db"},
		{name: "canonical overrides legacy", env: map[string]string{
			"PAIRTASK_SOCKET_PATH": "/canonical.sock", "COLLAB_SOCKET_PATH": "/legacy.sock",
			"PAIRTASK_DB_PATH": "/canonical.db", "COLLAB_DB_PATH": "/legacy.db"},
			socket: "/canonical.sock", database: "/canonical.db"},
		{name: "legacy retained", env: map[string]string{"COLLAB_SOCKET_PATH": "/legacy.sock", "COLLAB_DB_PATH": "/legacy.db"},
			socket: "/legacy.sock", database: "/legacy.db"},
		{name: "empty canonical socket rejects instead of fallback", env: map[string]string{"PAIRTASK_SOCKET_PATH": "", "COLLAB_SOCKET_PATH": "/legacy.sock"}, bad: true},
		{name: "empty canonical database rejects instead of fallback", env: map[string]string{"PAIRTASK_DB_PATH": "", "COLLAB_DB_PATH": "/legacy.db"}, bad: true},
		{name: "root operator UID", env: map[string]string{"PAIRTASK_OPERATOR_SOCKET_PATH": "/operator.sock", "PAIRTASK_OPERATOR_UID": "0"},
			socket: "/tmp/collab-ai.sock", database: "./collab-ai.db", operator: "/operator.sock", uid: 0},
		{name: "mixed source fields", env: map[string]string{"PAIRTASK_OPERATOR_SOCKET_PATH": "/operator.sock", "COLLAB_OPERATOR_UID": "1001"},
			socket: "/tmp/collab-ai.sock", database: "./collab-ai.db", operator: "/operator.sock", uid: 1001},
		{name: "empty canonical optional fields mask legacy", env: map[string]string{
			"PAIRTASK_OPERATOR_SOCKET_PATH": "", "PAIRTASK_OPERATOR_UID": "",
			"COLLAB_OPERATOR_SOCKET_PATH": "/legacy-operator.sock", "COLLAB_OPERATOR_UID": "1001"},
			socket: "/tmp/collab-ai.sock", database: "./collab-ai.db"},
		{name: "disabled endpoint cannot retain a UID", env: map[string]string{
			"PAIRTASK_OPERATOR_SOCKET_PATH": "", "COLLAB_OPERATOR_SOCKET_PATH": "/legacy-operator.sock", "COLLAB_OPERATOR_UID": "1001"}, bad: true},
		{name: "invalid canonical UID cannot use legacy", env: map[string]string{
			"PAIRTASK_OPERATOR_SOCKET_PATH": "/operator.sock", "PAIRTASK_OPERATOR_UID": "not-a-number", "COLLAB_OPERATOR_UID": "1001"}, bad: true},
		{name: "empty canonical UID cannot use legacy", env: map[string]string{
			"PAIRTASK_OPERATOR_SOCKET_PATH": "/operator.sock", "PAIRTASK_OPERATOR_UID": "", "COLLAB_OPERATOR_UID": "1001"}, bad: true},
		{name: "negative UID rejected", env: map[string]string{"PAIRTASK_OPERATOR_SOCKET_PATH": "/operator.sock", "PAIRTASK_OPERATOR_UID": "-1"}, bad: true},
		{name: "canonical sockets must remain distinct", env: map[string]string{
			"PAIRTASK_SOCKET_PATH": "/tmp/agent.sock", "PAIRTASK_OPERATOR_SOCKET_PATH": "/tmp/sub/../agent.sock", "PAIRTASK_OPERATOR_UID": "1001"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(func(key string) (string, bool) { value, exists := tc.env[key]; return value, exists })
			if (err != nil) != tc.bad {
				t.Fatalf("error %v, want rejected=%v", err, tc.bad)
			}
			if tc.bad {
				return
			}
			if cfg.SocketPath != tc.socket || cfg.DBPath != tc.database || cfg.OperatorSocketPath != tc.operator || cfg.OperatorUID != tc.uid {
				t.Fatalf("wrong resolved config: %+v", cfg)
			}
		})
	}
}
