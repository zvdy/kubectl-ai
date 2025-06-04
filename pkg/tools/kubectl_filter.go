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

	"k8s.io/klog/v2"
	"mvdan.cc/sh/v3/syntax"
)

// analyzeWithShellParser uses the mvdan/sh parser to analyze kubectl commands
// Returns the resource modification status and whether parsing was successful
func analyzeWithShellParser(command string) (string, bool) {
	// Parse the command using mvdan/sh
	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		klog.V(4).Infof("Failed to parse command with mvdan/sh: %v", err)
		return "unknown", false
	}

	// We expect a single command in most cases
	if len(file.Stmts) == 0 {
		return "unknown", false
	}

	// For multiple statements (e.g., commands separated by semicolons)
	numKubectl := 0
	for _, stmt := range file.Stmts {
		cmdExpr, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		var args []string
		for _, word := range cmdExpr.Args {
			var sb strings.Builder
			syntax.NewPrinter().Print(&sb, word)
			wordStr := strings.Trim(sb.String(), `"'`)
			args = append(args, wordStr)
		}
		if len(args) == 0 {
			continue
		}
		kubectlPos := -1
		for i, arg := range args {
			if strings.HasSuffix(arg, "kubectl") {
				kubectlPos = i
				break
			}
		}
		if kubectlPos == -1 {
			continue
		}
		// Explicitly handle exec as unknown
		if len(args) > kubectlPos+1 && args[kubectlPos+1] == "exec" {
			return "unknown", true
		}

		numKubectl++
	}
	if len(file.Stmts) > 1 && numKubectl != len(file.Stmts) {
		return "unknown", false
	}

	// For multiple statements (e.g., commands separated by semicolons)
	// If any statement modifies resources, the whole command is considered to modify resources
	hasModifications := false
	hasKubectlCmd := false

	// Check each statement
	for _, stmt := range file.Stmts {
		// We only care about command execution statements
		cmdExpr, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}

		// Get the command parts
		var args []string
		for _, word := range cmdExpr.Args {
			// Create a buffer to hold the word's string representation
			var sb strings.Builder
			// Use the printer to get the string representation
			syntax.NewPrinter().Print(&sb, word)
			wordStr := sb.String()
			// Remove any quotes that might be present
			wordStr = strings.Trim(wordStr, `"'`)
			args = append(args, wordStr)
		}

		// Check if this is a kubectl command
		if len(args) == 0 {
			continue
		}

		// Find kubectl in the command arguments
		kubectlPos := -1
		for i, arg := range args {
			if strings.HasSuffix(arg, "kubectl") {
				kubectlPos = i
				break
			}
		}

		// Not a kubectl command
		if kubectlPos == -1 {
			continue
		}

		hasKubectlCmd = true

		// Check for dry-run flag
		hasDryRun := false
		for i := kubectlPos + 1; i < len(args); i++ {
			if args[i] == "--dry-run" ||
				strings.HasPrefix(args[i], "--dry-run=") ||
				(i < len(args)-1 && args[i] == "--dry-run" && (args[i+1] == "client" || args[i+1] == "server")) {
				hasDryRun = true
				break
			}
		}

		if hasDryRun {
			// Just skip this command as it doesn't modify resources
			continue
		}

		// Now that we've confirmed it's a kubectl command, apply the same logic as the string-based approach
		if len(args) > kubectlPos+1 {
			result := analyzeKubectlSubcommand(args[kubectlPos+1:], args)
			if result == "yes" {
				hasModifications = true
			}
		}
	}

	// Only return a result if we found at least one kubectl command
	if hasKubectlCmd {
		// If there were no args after 'kubectl', treat as unknown (incomplete command)
		if !hasModifications && len(file.Stmts) == 1 {
			stmt := file.Stmts[0]
			cmdExpr, ok := stmt.Cmd.(*syntax.CallExpr)
			if ok && len(cmdExpr.Args) == 1 {
				// Extract the string value of the first arg
				var sb strings.Builder
				syntax.NewPrinter().Print(&sb, cmdExpr.Args[0])
				argStr := strings.Trim(sb.String(), `"'`)
				if strings.HasSuffix(argStr, "kubectl") {
					return "unknown", false
				}
			}
		}
		if hasModifications {
			return "yes", true
		}
		return "no", true
	}

	return "unknown", false
}

// analyzeKubectlSubcommand analyzes kubectl subcommands to determine if they modify resources
func analyzeKubectlSubcommand(args []string, allArgs []string) string {
	if len(args) == 0 {
		return "unknown"
	}

	subcommand := args[0]

	// List of prefixes/keywords that indicate read-only operations
	readOnlyOperations := []string{
		"get", "describe", "explain", "top", "logs", "api-resources",
		"api-versions", "version", "config view", "cluster-info",
		"wait", "auth can-i", "diff", "kustomize", "help",
		"options", "plugin", "proxy", "completion", "convert",
		"alpha list", "alpha status", "alpha debug",
		"events", "auth whoami",
	}

	// List of prefixes/keywords that indicate resource modification
	modifyingOperations := []string{
		"create", "apply", "edit", "delete", "patch", "replace",
		"scale", "autoscale", "expose", "rollout", "run", "set",
		"label", "annotate", "taint", "drain", "cordon", "uncordon",
		"certificate approve", "certificate deny", "debug", "attach",
		"cp", "auth reconcile",
		"alpha events trigger",
	}

	// Check for config commands specifically
	if subcommand == "config" && len(args) > 1 {
		// All config commands only modify local kubeconfig, not cluster resources
		return "no"
	}

	// Explicitly handle exec as unknown
	if subcommand == "exec" {
		return "unknown"
	}

	// Explicitly handle port-forward as no
	if subcommand == "port-forward" {
		return "no"
	}

	// Check if this is a compound subcommand
	if len(args) > 1 &&
		(subcommand == "certificate" || subcommand == "alpha" || subcommand == "auth") {
		compoundSubcmd := subcommand + " " + args[1]

		// Check against read-only operations
		for _, op := range readOnlyOperations {
			if op == compoundSubcmd {
				return "no"
			}
		}

		// Check against modifying operations
		for _, op := range modifyingOperations {
			if op == compoundSubcmd {
				return "yes"
			}
		}
	}

	// Handle kubectl plugins (commands starting with kubectl-*)
	if strings.HasPrefix(subcommand, "krew") && len(args) > 1 {
		krewCmd := args[1]
		if krewCmd == "install" || krewCmd == "uninstall" || krewCmd == "upgrade" {
			return "yes"
		}
		// Other krew commands like 'list', 'info', 'search' are read-only
		return "no"
	}

	// Check common read-only kubectl plugins
	knownReadOnlyPlugins := []string{
		"view-secret", "view-serviceaccount-kubeconfig", "tree", "whoami",
		"get-all", "neat", "view-utilization", "access-matrix", "resource-capacity",
		"ns", "ctx", "df-pv", "view", "grep", "tail", "dig", "exec-as", "explore",
		"iexec", "ktop", "krew", "mtail", "pv", "sniff", "status", "trace", "who-can",
		"view-allocations", "view-utilization", "view-secret",
	}

	knownModifierPlugins := []string{
		"edit-secret", "modify-secret", "bulk-action", "restart", "recreate",
		"ssh", "open-svc", "node-shell", "resource-snapshot", "cost",
		"drain", "fleet", "example", "whisper-secret", "rename", "rotate-cert",
		"slice", "transform", "strip-pvc", "cost-capacity", "cost-request",
	}

	// If it's a plugin command (kubectl-xxx format)
	// This would be in the original command string
	for _, arg := range allArgs {
		if strings.HasPrefix(arg, "kubectl-") {
			pluginName := strings.TrimPrefix(arg, "kubectl-")

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

	// Also check if this is a plugin invoked directly through kubectl (e.g., kubectl view-secret)
	if subcommand != "" {
		// Check for read-only plugins
		for _, plugin := range knownReadOnlyPlugins {
			if plugin == subcommand {
				return "no"
			}
		}

		// Check for modifying plugins
		for _, plugin := range knownModifierPlugins {
			if plugin == subcommand {
				return "yes"
			}
		}
	}

	// Check against read-only operations
	for _, op := range readOnlyOperations {
		if op == subcommand {
			return "no"
		}
	}

	// Check against modifying operations
	for _, op := range modifyingOperations {
		if op == subcommand {
			return "yes"
		}
	}

	// Check for watch flags which are read-only
	for _, arg := range args {
		if arg == "-w" || arg == "--watch" {
			return "no"
		}
	}

	// Default to unknown if we can't determine
	return "unknown"
}

// KubectlModifiesResource determines if a kubectl command modifies resources.
// Returns:
// - "yes" if the command definitely modifies resources
// - "no" if the command definitely doesn't modify resources
// - "unknown" if we can't determine for sure
func KubectlModifiesResource(command string) string {
	// Try to use the shell parser to analyze the command
	result, parsed := analyzeWithShellParser(command)
	if parsed {
		return result
	}

	// Fall back to the traditional string-based method if shell parsing fails
	// Skip commands that aren't kubectl commands
	if !strings.Contains(command, "kubectl") {
		return "unknown"
	}

	// For multi-statement commands (separated by semicolons), try to split and analyze
	if strings.Contains(command, ";") || strings.Contains(command, "&&") || strings.Contains(command, "||") {
		separators := []string{";", "&&", "||"}
		cmds := []string{command}
		for _, sep := range separators {
			var newCmds []string
			for _, c := range cmds {
				parts := strings.Split(c, sep)
				for _, p := range parts {
					if strings.TrimSpace(p) != "" {
						newCmds = append(newCmds, strings.TrimSpace(p))
					}
				}
			}
			cmds = newCmds
		}
		for _, cmd := range cmds {
			if !strings.Contains(cmd, "kubectl") {
				continue
			}
			result := KubectlModifiesResource(cmd)
			if result == "yes" {
				return "yes"
			}
		}
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
		"options", "plugin", "proxy", "completion", "convert",
		"alpha list", "alpha status", "alpha events", "alpha debug",
		"events", "auth whoami",
	}

	// List of prefixes/keywords that indicate resource modification
	modifyingOperations := []string{
		"create", "apply", "edit", "delete", "patch", "replace",
		"scale", "autoscale", "expose", "rollout", "run", "set",
		"label", "annotate", "taint", "drain", "cordon", "uncordon",
		"certificate approve", "certificate deny", "debug", "attach",
		"cp", "auth reconcile",
		"alpha events trigger",
	}

	// Clean up command for easier parsing
	trimmedCmd := strings.TrimSpace(command)
	cmdParts := strings.Fields(trimmedCmd) // Using Fields instead of Split to handle multiple spaces

	// Skip if somehow the command doesn't have enough parts
	if len(cmdParts) < 2 || (len(cmdParts) == 1 && cmdParts[0] == "kubectl") {
		return "unknown"
	}

	// Find the position of kubectl in case there are prefixes (like KUBECONFIG=... kubectl)
	kubectlPos := -1
	for i, part := range cmdParts {
		if strings.HasSuffix(part, "kubectl") ||
			strings.Contains(part, "kubectl\"") || // Handle quoted paths with spaces
			strings.Contains(part, "kubectl'") {
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
		"ns", "ctx", "df-pv", "view", "grep", "tail", "dig", "exec-as", "explore",
		"iexec", "ktop", "krew", "mtail", "pv", "sniff", "status", "trace", "who-can",
		"view-allocations", "view-utilization", "view-secret",
	}

	knownModifierPlugins := []string{
		"edit-secret", "modify-secret", "bulk-action", "restart", "recreate",
		"ssh", "open-svc", "node-shell", "resource-snapshot", "cost",
		"drain", "fleet", "example", "whisper-secret", "rename", "rotate-cert",
		"slice", "transform", "strip-pvc", "cost-capacity", "cost-request",
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

	// Check if the subcommand itself is a known plugin name
	// For example: "kubectl view-secret my-secret"
	for _, plugin := range knownReadOnlyPlugins {
		if plugin == subcommand {
			return "no"
		}
	}

	for _, plugin := range knownModifierPlugins {
		if plugin == subcommand {
			return "yes"
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
