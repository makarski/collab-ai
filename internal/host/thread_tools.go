package host

import (
	"errors"
	"strings"
)

// Runtime MCP works for ordinary saved conversations as well as managed ones.
// Codex can only add dynamic tools on thread/start, not resume or fork.
func (p *Proxy) addThreadTools(params map[string]any, method string) error {
	if p.RuntimeMCP == nil {
		if method != "thread/start" {
			return errors.New("resume and fork require the runtime MCP tool connection")
		}
		return addToolSpecs(params, p.Tools.Specs)
	}
	if err := validateToolNames(params); err != nil {
		return err
	}
	return p.addRuntimeConfig(params)
}

func (p *Proxy) addRuntimeConfig(params map[string]any) error {
	config, ok := params["config"].(map[string]any)
	if params["config"] != nil && !ok {
		return errors.New("thread config must be an object")
	}
	if config == nil {
		config = make(map[string]any)
	}
	config["mcp_servers.collab_runtime"] = p.RuntimeMCP
	params["config"] = config
	return nil
}

func addManagedInstructions(params map[string]any, method string) {
	instructions, explicit := params["developerInstructions"].(string)
	if method != "thread/start" && !explicit {
		return // Preserve saved developer instructions on resume and fork.
	}
	if strings.Contains(instructions, managedInstructions) {
		return
	}
	params["developerInstructions"] = instructions + "\n" + managedInstructions
}
