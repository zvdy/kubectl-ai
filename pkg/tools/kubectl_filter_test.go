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
		{"Config set-context", "kubectl config set-context my-context", "yes"},

		// Edge cases
		{"Command with env var", "KUBECONFIG=/path/to/config kubectl get pods", "no"},
		{"Command with kubectl in path", "/usr/local/bin/kubectl get pods", "no"},
		{"Not kubectl command", "ls -la", "unknown"},
		{"Multiple spaces", "kubectl  get   pods", "no"},
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
