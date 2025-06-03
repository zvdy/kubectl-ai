// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tools

import (
	"strings"
)

// KubectlModifiesResource determines if a kubectl command modifies resources.
// Returns:
// - "yes" if the command definitely modifies resources
// - "no" if the command definitely doesn't modify resources
// - "unknown" if we can't determine for sure
func KubectlModifiesResource(command string) string {
	// Skip commands that aren't kubectl commands
	if !strings.Contains(command, "kubectl") {
		return "unknown"
	}

	// Check for dry-run flag first - this overrides any other command type
	if hasDryRunFlag(command) {
		return "no"
	}

	// List of prefixes/keywords that indicate read-only operations
	readOnlyOperations := []string{
		"get", "describe", "explain", "top", "logs", "api-resources",
		"api-versions", "version", "config view", "cluster-info",
		"wait", "auth can-i", "diff", "kustomize", "help",
		"options", "plugin", "proxy", "completion",
		"alpha list", "alpha status", "alpha events",
	}

	// List of prefixes/keywords that indicate resource modification
	modifyingOperations := []string{
		"create", "apply", "edit", "delete", "patch", "replace",
		"scale", "autoscale", "expose", "rollout", "run", "set",
		"label", "annotate", "taint", "drain", "cordon", "uncordon",
		"certificate approve", "certificate deny", "debug", "attach",
		"cp", "exec", "port-forward", "auth reconcile",
		"alpha events trigger",
	}

	// Clean up command for easier parsing
	trimmedCmd := strings.TrimSpace(command)
	cmdParts := strings.Fields(trimmedCmd) // Using Fields instead of Split to handle multiple spaces

	// Skip if somehow the command doesn't have enough parts
	if len(cmdParts) < 2 {
		return "unknown"
	}

	// Find the position of kubectl in case there are prefixes (like KUBECONFIG=... kubectl)
	kubectlPos := -1
	for i, part := range cmdParts {
		if strings.HasSuffix(part, "kubectl") {
			kubectlPos = i
			break
		}
	}

	if kubectlPos == -1 || kubectlPos >= len(cmdParts)-1 {
		return "unknown"
	}

	// Extract the kubectl subcommand (part after "kubectl")
	subcommand := cmdParts[kubectlPos+1]

	// Check for config commands specifically first
	if subcommand == "config" && len(cmdParts) > kubectlPos+2 {
		configCmd := cmdParts[kubectlPos+2]

		// Read-only config operations
		if configCmd == "view" && !containsAny(command, []string{"--flatten", "-o"}) {
			return "no"
		}

		// Modifying config operations
		if configCmd == "set-context" || configCmd == "use-context" ||
			configCmd == "set" || configCmd == "unset" || configCmd == "delete-context" {
			return "yes"
		}

		// Default for other config commands - they typically modify kubeconfig
		return "yes"
	}

	// Check if this is a compound subcommand for other multi-part commands
	if len(cmdParts) > kubectlPos+2 &&
		(subcommand == "certificate" || subcommand == "alpha" || subcommand == "auth") {
		subcommand = subcommand + " " + cmdParts[kubectlPos+2]
	}

	// Handle kubectl plugins (commands starting with kubectl-*)
	if strings.HasPrefix(subcommand, "krew") {
		// Krew plugin manager - only 'install', 'uninstall', and 'upgrade' modify the system
		if len(cmdParts) > kubectlPos+2 {
			krewCmd := cmdParts[kubectlPos+2]
			if krewCmd == "install" || krewCmd == "uninstall" || krewCmd == "upgrade" {
				return "yes"
			}
			// Other krew commands like 'list', 'info', 'search' are read-only
			return "no"
		}
	}

	// Check common read-only kubectl plugins
	knownReadOnlyPlugins := []string{
		"view-secret", "view-serviceaccount-kubeconfig", "tree", "whoami",
		"get-all", "neat", "view-utilization", "access-matrix", "resource-capacity",
		"ns", "ctx", "df-pv",
	}

	knownModifierPlugins := []string{
		"edit-secret", "modify-secret", "bulk-action", "restart",
		"ssh", "open-svc", "node-shell", "resource-snapshot",
	}

	// If it's a plugin command (kubectl-xxx format)
	if strings.Contains(trimmedCmd, "kubectl-") {
		pluginName := ""
		for _, part := range cmdParts {
			if strings.HasPrefix(part, "kubectl-") {
				pluginName = strings.TrimPrefix(part, "kubectl-")
				break
			}
		}

		if pluginName != "" {
			// Check for read-only plugins
			for _, plugin := range knownReadOnlyPlugins {
				if plugin == pluginName {
					return "no"
				}
			}

			// Check for modifying plugins
			for _, plugin := range knownModifierPlugins {
				if plugin == pluginName {
					return "yes"
				}
			}
		}
	}

	// Check against read-only operations
	for _, op := range readOnlyOperations {
		if strings.HasPrefix(subcommand, op) || op == subcommand {
			return "no"
		}
	}

	// Special cases are now handled earlier in the function

	// Check against modifying operations
	for _, op := range modifyingOperations {
		if strings.HasPrefix(subcommand, op) || op == subcommand {
			return "yes"
		}
	}

	// We already checked for dry-run flag at the beginning of the function

	// Check if this is a get command with output redirection
	// For example: "kubectl get pods -o yaml > pods.yaml"
	// This doesn't modify k8s resources but does modify local files
	if strings.Contains(command, " > ") || strings.Contains(command, " >> ") {
		// This isn't modifying Kubernetes resources even though it creates local files
		if strings.Contains(command, " get ") ||
			strings.Contains(command, " describe ") {
			return "no"
		}
	}

	// Check for watch flags which are read-only
	if containsAny(command, []string{"-w", "--watch"}) {
		return "no"
	}

	// Default to unknown if we can't determine
	return "unknown"
}

// hasDryRunFlag checks if the command contains any variation of the dry-run flag
func hasDryRunFlag(command string) bool {
	tokens := strings.Fields(command)

	for i, token := range tokens {
		// Check for various formats of dry-run flag
		if token == "--dry-run" ||
			strings.HasPrefix(token, "--dry-run=") {
			return true
		}

		// Handle space-separated flag value: "--dry-run client" or "--dry-run server"
		if token == "--dry-run" && i < len(tokens)-1 {
			nextToken := tokens[i+1]
			if nextToken == "client" || nextToken == "server" {
				return true
			}
		}
	}
	return false
}

// containsAny checks if the command contains any of the given strings
// For flag detection, it properly checks word boundaries to avoid false positives
func containsAny(command string, substrings []string) bool {
	// Split the command into space-separated tokens to properly detect flags
	tokens := strings.Fields(command)

	for _, substr := range substrings {
		// Simple substring check for non-flag strings
		if !strings.HasPrefix(substr, "--") {
			if strings.Contains(command, substr) {
				return true
			}
			continue
		}

		// For flags, we need to ensure proper flag matching
		for _, token := range tokens {
			if token == substr || strings.HasPrefix(token, substr+"=") {
				return true
			}
		}
	}
	return false
}
