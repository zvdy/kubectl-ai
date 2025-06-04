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
	"testing"
)

func TestKubectlModifiesResource(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		// Read-only commands
		{"Get pods", "kubectl get pods", "no"},
		{"Describe pod", "kubectl describe pod nginx", "no"},
		{"Get with output", "kubectl get pods -o yaml", "no"},
		{"Get with output redirection", "kubectl get pods > pods.txt", "no"},
		{"Get with name", "kubectl get pod nginx", "no"},
		{"Config view", "kubectl config view", "no"},
		{"Version", "kubectl version", "no"},
		{"API resources", "kubectl api-resources", "no"},
		{"Explain", "kubectl explain pods", "no"},
		{"Logs", "kubectl logs nginx", "no"},
		{"Logs with follow", "kubectl logs nginx -f", "no"},
		{"Watch pods", "kubectl get pods --watch", "no"},
		{"Watch pods short", "kubectl get pods -w", "no"},
		{"Diff", "kubectl diff -f deployment.yaml", "no"},
		{"Can-i", "kubectl auth can-i create pods", "no"},
		{"Kustomize", "kubectl kustomize ./", "no"},
		{"Convert", "kubectl convert -f pod.yaml --output-version=v1", "no"},
		{"Events", "kubectl events", "no"},
		{"Alpha debug", "kubectl alpha debug pod/nginx", "no"},
		{"Auth whoami", "kubectl auth whoami", "no"},

		// Commands with dry-run flags
		{"Create with dry-run", "kubectl create -f pod.yaml --dry-run=client", "no"},
		{"Apply with dry-run", "kubectl apply -f pod.yaml --dry-run", "no"},
		{"Apply with server dry-run", "kubectl apply -f pod.yaml --dry-run=server", "no"},
		{"Delete with dry-run", "kubectl delete pod nginx --dry-run client", "no"},

		// Resource-modifying commands
		{"Create pod", "kubectl create -f pod.yaml", "yes"},
		{"Apply deployment", "kubectl apply -f deployment.yaml", "yes"},
		{"Delete pod", "kubectl delete pod nginx", "yes"},
		{"Scale deployment", "kubectl scale deployment nginx --replicas=3", "yes"},
		{"Edit deployment", "kubectl edit deployment nginx", "yes"},
		{"Patch service", "kubectl patch svc nginx -p '{\"spec\":{\"type\":\"NodePort\"}}'", "yes"},
		{"Label pod", "kubectl label pod nginx app=web", "yes"},
		{"Annotate", "kubectl annotate pods nginx description='my nginx'", "yes"},
		{"Rollout restart", "kubectl rollout restart deployment nginx", "yes"},
		{"Set image", "kubectl set image deployment/nginx nginx=nginx:latest", "yes"},
		{"Taint node", "kubectl taint nodes node1 key=value:NoSchedule", "yes"},
		{"Run pod", "kubectl run nginx --image=nginx", "yes"},
		{"Config set-context", "kubectl config set-context my-context", "no"},
		{"Port-forward", "kubectl port-forward pod/nginx 8080:80", "no"},
		{"Exec command", "kubectl exec nginx -- ls", "unknown"},
		{"Cordon node", "kubectl cordon node1", "yes"},
		{"Uncordon node", "kubectl uncordon node1", "yes"},
		{"Drain node", "kubectl drain node1", "yes"},
		{"Certificate approve", "kubectl certificate approve csr-12345", "yes"},

		// Edge cases
		{"Command with env var", "KUBECONFIG=/path/to/config kubectl get pods", "no"},
		{"Command with kubectl in path", "/usr/local/bin/kubectl get pods", "no"},
		{"Not kubectl command", "ls -la", "unknown"},
		{"Multiple spaces", "kubectl  get   pods", "no"},
		{"Complex command with variables", "kubectl get pods -l app=$APP_NAME -n $NAMESPACE", "no"},
		{"Command with quotes", "kubectl get pods -l \"app=my app\"", "no"},
		{"Command with pipe", "kubectl get pods | grep nginx", "no"},

		// Additional test cases for more complex scenarios

		// Complex command combinations
		{"Complex apply with flags", "kubectl apply -f deployment.yaml --record=true --cascade=true --timeout=30s", "yes"},
		{"Apply with multiple files", "kubectl apply -f config1.yaml -f config2.yaml -f config3.yaml", "yes"},
		{"Apply with directory", "kubectl apply -f ./manifests/ -R", "yes"},
		{"Create with generator", "kubectl create deployment nginx --image=nginx --replicas=3 --port=80", "yes"},

		// Config command variations
		{"Config set credentials", "kubectl config set-credentials cluster-admin --token=secret", "no"},
		{"Config view with flatten", "kubectl config view --flatten", "no"},
		{"Config view with output", "kubectl config view -o json", "no"},
		{"Config use-context", "kubectl config use-context production", "no"},

		// Plugin commands
		{"Plugin view-secret", "kubectl view-secret my-secret", "no"},
		{"Plugin tree", "kubectl tree deployment nginx", "no"},
		{"Plugin edit-secret", "kubectl edit-secret my-secret", "yes"},
		{"Plugin restart", "kubectl restart deployment/nginx", "yes"},
		{"Plugin with kubectl prefix", "kubectl-ns default", "unknown"},
		{"Plugin krew install", "kubectl krew install neat", "yes"},
		{"Plugin krew list", "kubectl krew list", "no"},

		// Commands with special characters and quotes
		{"Label with special characters", "kubectl label pod nginx 'app.kubernetes.io/name=nginx-controller'", "yes"},
		{"Jsonpath with quotes", "kubectl get pods -o jsonpath='{.items[0].metadata.name}'", "no"},
		{"Command with escaped quotes", "kubectl patch configmap my-config --patch \"{\\\"data\\\":{\\\"key\\\":\\\"new-value\\\"}}\"", "yes"}, // Commands with environment variables and expressions
		{"Complex env vars", "KUBECONFIG=/path/to/config NS=default kubectl get pods -n $NS", "no"},
		{"Command with multiple env vars", "KUBECONFIG=/config KUBECTL_EXTERNAL_DIFF=\"diff -u\" kubectl diff -f file.yaml", "no"},

		// Multi-command operations
		{"Sequential commands with semicolon", "kubectl get ns; kubectl create ns test", "yes"}, // If any command modifies, the result should be yes
		{"Multiple safe commands", "kubectl get pods; kubectl get deployments", "no"},

		// Commands that mix safe and unsafe operations
		{"Mix safe and unsafe with result", "kubectl get pods && kubectl delete pod bad-pod", "yes"}, // && is not properly analyzed yet
		{"Mix with initial unsafe", "kubectl delete pod bad-pod && kubectl get pods", "yes"},         // Commands with kubectl aliases
		{"Kubectl alias k", "k get pods", "unknown"},                                                 // Should be unknown since it's not "kubectl"
		{"Full path with arguments", "/usr/local/custom/kubectl --kubeconfig=/path/config get pods", "no"},

		// Commands with complex filtering and output manipulation
		{"Complex jsonpath", "kubectl get pods -o=jsonpath='{range .items[*]}{.metadata.name}{\"\\t\"}{.status.phase}{\"\\n\"}{end}'", "no"},
		{"Custom columns", "kubectl get pods -o=custom-columns=NAME:.metadata.name,STATUS:.status.phase", "no"},

		// Authentication and credential-related commands
		{"Impersonation", "kubectl get pods --as=system:serviceaccount:default:deployer", "no"},
		{"With token", "kubectl --token=eyJhbGciOiJSUzI1NiIsImtpZCI6IiJ9... get pods", "no"},

		// Commands with unusual spacing and formatting
		{"Weird spacing", "kubectl     get    pods   -n   default", "no"},
		{"Line breaks", "kubectl \\n get \\n pods", "no"},

		// Complex watch scenarios
		{"Watch with selector", "kubectl get pods --selector app=nginx --watch", "no"},
		{"Negative watch timeout", "kubectl get pods --watch-only --timeout=10s", "no"},

		// Command with flags after resource names
		{"Flags after name", "kubectl delete pod mypod --now --grace-period=0", "yes"},

		// Advanced server-side operations
		{"Server-side apply", "kubectl apply -f deploy.yaml --server-side", "yes"},
		{"Field manager", "kubectl apply -f deploy.yaml --field-manager=controller", "yes"},

		// RBAC and security-related commands
		{"Create service account", "kubectl create serviceaccount jenkins", "yes"},
		{"Create role binding", "kubectl create rolebinding admin --clusterrole=admin --user=user1 --namespace=default", "yes"},

		// Test kubectl with alternate binary paths
		{"Versioned kubectl", "kubectl.1.24 get pods", "unknown"},
		{"Full kubectl path with spaces", "\"/path with spaces/kubectl\" get pods", "no"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := KubectlModifiesResource(tt.command)
			if result != tt.expected {
				t.Errorf("KubectlModifiesResource(%q) = %q, want %q", tt.command, result, tt.expected)
			}
		})
	}
}

// TestKubectlEdgeCases tests specific edge cases that need more detailed validation
func TestKubectlEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		// Special handling for kubectl config commands
		{"Config view without output flag", "kubectl config view", "no"},
		{"Config view with output flag", "kubectl config view --output=json", "no"},
		{"Config set", "kubectl config set contexts.my-context.namespace default", "no"},

		// Dry run in different positions
		{"Dry run at end", "kubectl delete pod nginx --dry-run=client", "no"},
		{"Dry run in middle", "kubectl delete --dry-run=client pod nginx", "no"},
		{"Dry run with space", "kubectl delete pod nginx --dry-run client", "no"},

		// Complex pipes and redirections
		{"Pipe to multiple commands", "kubectl get pods | grep Running | wc -l", "no"},
		{"Background process", "kubectl port-forward svc/nginx 8080:80 &", "no"},
		{"Multiple redirections", "kubectl get pods > pods.txt 2> errors.txt", "no"},

		// Commands with backticks or subcommands
		{"Command with backticks", "kubectl get pod `cat podname.txt`", "no"},
		{"Command with subcommand", "kubectl get namespace $(kubectl get ns -o=jsonpath='{.items[0].metadata.name}')", "no"},

		// Complex flag combinations
		{"Command with all namespaces and watch", "kubectl get pods -A -w", "no"},
		{"Command with selector and field selector", "kubectl get pods --selector=app=nginx --field-selector=status.phase=Running", "no"},

		// Shell script variables
		{"Command with script variables", "kubectl get pods -n ${NAMESPACE:-default} -l app=${APP_LABEL}", "no"},

		// Common typos and errors
		{"Typo in command", "kubect get pods", "unknown"},
		{"Empty command", "", "unknown"},
		{"Just kubectl", "kubectl", "unknown"},

		// Complex resource names
		{"Resource with dots", "kubectl get pods my.complex-pod.name", "no"},
		{"Resource with slashes", "kubectl delete deployment/my-app", "yes"},

		// Commands with output filtering
		{"Command with grep", "kubectl get pods | grep -v Completed", "no"},
		{"Command with awk", "kubectl get pods | awk '{print $1}'", "no"},

		// Force flags
		{"Delete with force", "kubectl delete pod stuck-pod --force --grace-period=0", "yes"},

		// Commands with custom API resources
		{"Custom resource get", "kubectl get virtualmachines", "no"},
		{"Custom resource apply", "kubectl apply -f vm-instance.yaml", "yes"},

		// Commands with file handling
		{"Multiple input files", "kubectl delete -f file1.yaml -f file2.yaml", "yes"},
		{"URL as input file", "kubectl apply -f https://example.com/manifest.yaml", "yes"},
		{"Input from stdin", "cat file.yaml | kubectl apply -f -", "yes"},

		// Proxy and port-forwarding
		{"Proxy command", "kubectl proxy --port=8080", "no"},
		{"Complex port-forward", "kubectl port-forward deployment/myapp 8080:8080 9000:9000", "no"},

		// Debug scenarios
		{"Attach command", "kubectl attach mypod -i", "yes"},
		{"Copy files", "kubectl cp mypod:/tmp/foo /tmp/bar", "yes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := KubectlModifiesResource(tt.command)
			if result != tt.expected {
				t.Errorf("KubectlModifiesResource(%q) = %q, want %q", tt.command, result, tt.expected)
			}
		})
	}
}

// TestKubectlAnalyzerComponents tests the internal helper functions used by KubectlModifiesResource
func TestKubectlAnalyzerComponents(t *testing.T) {
	t.Run("hasDryRunFlag detection", func(t *testing.T) {
		tests := []struct {
			command  string
			expected bool
		}{
			{"kubectl apply -f deploy.yaml --dry-run=client", true},
			{"kubectl apply -f deploy.yaml --dry-run", true},
			{"kubectl delete pod nginx --dry-run client", true},
			{"kubectl delete pod nginx --dry-run=server", true},
			{"kubectl apply -f deploy.yaml", false},
			{"kubectl get pods --dry", false}, // Not a valid dry-run flag
			{"echo --dry-run", true},          // The current implementation doesn't check if it's kubectl
		}

		for _, tt := range tests {
			result := hasDryRunFlag(tt.command)
			if result != tt.expected {
				t.Errorf("hasDryRunFlag(%q) = %v, want %v", tt.command, result, tt.expected)
			}
		}
	})

	t.Run("containsAny detection", func(t *testing.T) {
		tests := []struct {
			command    string
			substrings []string
			expected   bool
		}{
			{"kubectl get pods -w", []string{"-w", "--watch"}, true},
			{"kubectl get pods --watch", []string{"-w", "--watch"}, true},
			{"kubectl get pods", []string{"-w", "--watch"}, false},
			{"kubectl get pods -o wide", []string{"-o", "--output"}, true},
			{"kubectl get pods --output=json", []string{"--output"}, true},
			{"kubectl get pods -output=json", []string{"--output"}, false}, // Wrong flag format
		}

		for _, tt := range tests {
			result := containsAny(tt.command, tt.substrings)
			if result != tt.expected {
				t.Errorf("containsAny(%q, %v) = %v, want %v", tt.command, tt.substrings, result, tt.expected)
			}
		}
	})

	t.Run("analyzeWithShellParser functionality", func(t *testing.T) {
		tests := []struct {
			command     string
			expectedRes string
			expectedOk  bool
		}{
			{"kubectl get pods", "no", true},
			{"kubectl apply -f deploy.yaml", "yes", true},
			{"ls -la", "unknown", false},      // Not kubectl
			{"kubectl", "unknown", false},     // Incomplete command
			{"kubectl; ls", "unknown", false}, // Multiple commands
		}

		for _, tt := range tests {
			result, ok := analyzeWithShellParser(tt.command)
			if result != tt.expectedRes || ok != tt.expectedOk {
				t.Errorf("analyzeWithShellParser(%q) = (%q, %v), want (%q, %v)",
					tt.command, result, ok, tt.expectedRes, tt.expectedOk)
			}
		}
	})
}
