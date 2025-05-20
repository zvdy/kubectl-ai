// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GoogleCloudPlatform/kubectl-ai/gollm"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/agent"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/journal"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/ui"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/ui/html"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// Using the defaults from goreleaser as per https://goreleaser.com/cookbooks/using-main.version/
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func BuildRootCommand(opt *Options) (*cobra.Command, error) {
	rootCmd := &cobra.Command{
		Use:   "kubectl-ai",
		Short: "A CLI tool to interact with Kubernetes using natural language",
		Long:  "kubectl-ai is a command-line tool that allows you to interact with your Kubernetes cluster using natural language queries. It leverages large language models to understand your intent and translate it into kubectl",
		Args:  cobra.MaximumNArgs(1), // Only one positional arg is allowed.
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunRootCommand(cmd.Context(), *opt, args)
		},
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version number of kubectl-ai",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("kubectl-ai version: %s\ncommit: %s\ndate: %s\n", version, commit, date)
			os.Exit(0)
		},
	})

	if err := opt.bindCLIFlags(rootCmd.Flags()); err != nil {
		return nil, err
	}

	return rootCmd, nil
}

type Options struct {
	ProviderID string `json:"llmProvider,omitempty"`
	ModelID    string `json:"model,omitempty"`
	// SkipPermissions is a flag to skip asking for confirmation before executing kubectl commands
	// that modifies resources in the cluster.
	SkipPermissions bool `json:"skipPermissions,omitempty"`
	// EnableToolUseShim is a flag to enable tool use shim.
	// TODO(droot): figure out a better way to discover if the model supports tool use
	// and set this automatically.
	EnableToolUseShim bool `json:"enableToolUseShim,omitempty"`
	// Quiet flag indicates if the agent should run in non-interactive mode.
	// It requires a query to be provided as a positional argument.
	Quiet                  bool     `json:"quiet,omitempty"`
	MCPServer              bool     `json:"mcpServer,omitempty"`
	MaxIterations          int      `json:"maxIterations,omitempty"`
	KubeConfigPath         string   `json:"kubeConfigPath,omitempty"`
	PromptTemplateFilePath string   `json:"promptTemplateFilePath,omitempty"`
	ExtraPromptPaths       []string `json:"extraPromptPaths,omitempty"`
	TracePath              string   `json:"tracePath,omitempty"`
	RemoveWorkDir          bool     `json:"removeWorkDir,omitempty"`
	ToolConfigPath         []string `json:"toolConfigPath,omitempty"`

	// UserInterface is the type of user interface to use.
	UserInterface UserInterface `json:"userInterface,omitempty"`

	// SkipVerifySSL is a flag to skip verifying the SSL certificate of the LLM provider.
	SkipVerifySSL bool `json:"skipVerifySSL,omitempty"`
}

type UserInterface string

const (
	UserInterfaceTerminal UserInterface = "terminal"
	UserInterfaceHTML     UserInterface = "html"
)

// Implement pflag.Value for UserInterface
func (u *UserInterface) Set(s string) error {
	switch s {
	case "terminal", "html":
		*u = UserInterface(s)
		return nil
	default:
		return fmt.Errorf("invalid user interface: %s", s)
	}
}

func (u *UserInterface) String() string {
	return string(*u)
}

func (u *UserInterface) Type() string {
	return "UserInterface"
}

func (o *Options) InitDefaults() {
	o.ProviderID = "gemini"
	o.ModelID = "gemini-2.5-pro-preview-03-25"
	// by default, confirm before executing kubectl commands that modify resources in the cluster.
	o.SkipPermissions = false
	o.MCPServer = false
	// We now default to our strongest model (gemini-2.5-pro-exp-03-25) which supports tool use natively.
	// so we don't need shim.
	o.EnableToolUseShim = false
	o.Quiet = false
	o.MCPServer = false
	o.MaxIterations = 20
	o.KubeConfigPath = ""
	o.PromptTemplateFilePath = ""
	o.ExtraPromptPaths = []string{}
	o.TracePath = filepath.Join(os.TempDir(), "kubectl-ai-trace.txt")
	o.RemoveWorkDir = false
	o.ToolConfigPath = []string{
		filepath.Join("{CONFIG}", "kubectl-ai", "tools.yaml"),
		filepath.Join("{HOME}", ".config", "kubectl-ai", "tools.yaml"),
	}

	// Default to terminal UI
	o.UserInterface = UserInterfaceTerminal

	// Default to not skipping SSL verification
	o.SkipVerifySSL = false
}

func (o *Options) LoadConfiguration(b []byte) error {
	if err := yaml.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("parsing configuration: %w", err)
	}
	return nil
}

func (o *Options) LoadConfigurationFile() error {
	configPaths := []string{
		filepath.Join("{CONFIG}", "kubectl-ai", "config.yaml"),
		filepath.Join("{HOME}", ".config", "kubectl-ai", "config.yaml"),
	}

	for _, configPath := range configPaths {
		// Try to load configuration
		tokens := strings.Split(configPath, string(os.PathSeparator))
		for i, token := range tokens {
			if token == "{CONFIG}" {
				configDir, err := os.UserConfigDir()
				if err != nil {
					return fmt.Errorf("getting user config directory: %w", err)
				}
				tokens[i] = configDir
			}
			if token == "{HOME}" {
				homeDir, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("getting user home directory: %w", err)
				}
				tokens[i] = homeDir
			}
		}
		configPath = filepath.Join(tokens...)
		configBytes, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				// ignore
			} else {
				fmt.Fprintf(os.Stderr, "warning: could not load defaults from %q: %v\n", configPath, err)
			}
		}
		if len(configBytes) > 0 {
			if err := o.LoadConfiguration(configBytes); err != nil {
				fmt.Fprintf(os.Stderr, "warning: error loading configuration from %q: %v\n", configPath, err)
			}
		}
	}

	return nil
}

func main() {
	ctx := context.Background()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		klog.Flush()
		fmt.Fprintf(os.Stderr, "Received signal, shutting down... %s\n", sig)
		os.Exit(0)
	}()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// klog setup must happen before Cobra parses any flags

	// add commandline flags for logging
	klogFlags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(klogFlags)

	klogFlags.Set("logtostderr", "false")
	klogFlags.Set("log_file", filepath.Join(os.TempDir(), "kubectl-ai.log"))

	defer klog.Flush()

	var opt Options

	opt.InitDefaults()

	// load YAML config values
	if err := opt.LoadConfigurationFile(); err != nil {
		return fmt.Errorf("failed to load config file: %w", err)
	}

	rootCmd, err := BuildRootCommand(&opt)
	if err != nil {
		return err
	}

	// cobra has to know that we pass pass flags with flag lib, otherwise it creates conflict with flags.parse() method
	// We add just the klog flags we want, not all the klog flags (there are a lot, most of them are very niche)
	rootCmd.PersistentFlags().AddGoFlag(klogFlags.Lookup("v"))

	// do this early, before the third-party code logs anything.
	redirectStdLogToKlog()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return err
	}

	return nil
}

func (opt *Options) bindCLIFlags(f *pflag.FlagSet) error {
	f.IntVar(&opt.MaxIterations, "max-iterations", opt.MaxIterations, "maximum number of iterations agent will try before giving up")
	f.StringVar(&opt.KubeConfigPath, "kubeconfig", opt.KubeConfigPath, "path to kubeconfig file")
	f.StringVar(&opt.PromptTemplateFilePath, "prompt-template-file-path", opt.PromptTemplateFilePath, "path to custom prompt template file")
	f.StringArrayVar(&opt.ExtraPromptPaths, "extra-prompt-paths", opt.ExtraPromptPaths, "extra prompt template paths")
	f.StringVar(&opt.TracePath, "trace-path", opt.TracePath, "path to the trace file")
	f.BoolVar(&opt.RemoveWorkDir, "remove-workdir", opt.RemoveWorkDir, "remove the temporary working directory after execution")

	f.StringVar(&opt.ProviderID, "llm-provider", opt.ProviderID, "language model provider")
	f.StringVar(&opt.ModelID, "model", opt.ModelID, "language model e.g. gemini-2.0-flash-thinking-exp-01-21, gemini-2.0-flash")
	f.BoolVar(&opt.SkipPermissions, "skip-permissions", opt.SkipPermissions, "(dangerous) skip asking for confirmation before executing kubectl commands that modify resources")
	f.BoolVar(&opt.MCPServer, "mcp-server", opt.MCPServer, "run in MCP server mode")
	f.StringArrayVar(&opt.ToolConfigPath, "custom-tools-config", opt.ToolConfigPath, "path to custom tools config file")
	f.BoolVar(&opt.EnableToolUseShim, "enable-tool-use-shim", opt.EnableToolUseShim, "enable tool use shim")
	f.BoolVar(&opt.Quiet, "quiet", opt.Quiet, "run in non-interactive mode, requires a query to be provided as a positional argument")

	f.Var(&opt.UserInterface, "user-interface", "user interface mode to use")
	f.BoolVar(&opt.SkipVerifySSL, "skip-verify-ssl", opt.SkipVerifySSL, "skip verifying the SSL certificate of the LLM provider")

	return nil
}

func RunRootCommand(ctx context.Context, opt Options, args []string) error {
	var err error // Declare err once for the whole function

	// resolve kubeconfig path with priority: flag/env > KUBECONFIG > default path
	if err = resolveKubeConfigPath(&opt); err != nil {
		return fmt.Errorf("failed to resolve kubeconfig path: %w", err)
	}

	if opt.MCPServer {
		if err = startMCPServer(ctx, opt); err != nil {
			return fmt.Errorf("failed to start MCP server: %w", err)
		}
	}

	// Load and register custom tools from config files and dirs
	for _, path := range opt.ToolConfigPath {
		tokens := strings.Split(path, string(os.PathSeparator))
		for i, token := range tokens {
			if token == "{CONFIG}" {
				configDir, err := os.UserConfigDir()
				if err != nil {
					return fmt.Errorf("getting user config directory: %w", err)
				}
				tokens[i] = configDir
			}
			if token == "{HOME}" {
				homeDir, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("getting user home directory: %w", err)
				}
				tokens[i] = homeDir
			}
		}

		if err := tools.LoadAndRegisterCustomTools(filepath.Join(tokens...)); err != nil {
			// Log the error but continue execution, as custom tools are optional
			klog.Warningf("Failed to load or register custom tools (path: %q): %v", opt.ToolConfigPath, err)
		}
	}

	// After reading stdin, it is consumed
	var hasInputData bool
	hasInputData, err = hasStdInData()
	if err != nil {
		return fmt.Errorf("failed to check if stdin has data: %w", err)
	}

	// Handles positional args or stdin
	var queryFromCmd string
	queryFromCmd, err = resolveQueryInput(hasInputData, args)
	if err != nil {
		return fmt.Errorf("failed to resolve query input %w", err)
	}

	klog.Info("Application started", "pid", os.Getpid())

	var llmClient gollm.Client
	if opt.SkipVerifySSL {
		llmClient, err = gollm.NewClient(ctx, opt.ProviderID, gollm.WithSkipVerifySSL())
	} else {
		llmClient, err = gollm.NewClient(ctx, opt.ProviderID)
	}
	if err != nil {
		return fmt.Errorf("creating llm client: %w", err)
	}
	defer llmClient.Close()

	var recorder journal.Recorder
	if opt.TracePath != "" {
		var fileRecorder journal.Recorder
		fileRecorder, err = journal.NewFileRecorder(opt.TracePath)
		if err != nil {
			return fmt.Errorf("creating trace recorder: %w", err)
		}
		defer fileRecorder.Close()
		recorder = fileRecorder
	} else {
		// Ensure we always have a recorder, to avoid nil checks
		recorder = &journal.LogRecorder{}
		defer recorder.Close()
	}

	doc := ui.NewDocument()

	var userInterface ui.UI
	switch opt.UserInterface {
	case UserInterfaceTerminal:
		// since stdin is already consumed, we use TTY for taking input from user
		useTTYForInput := hasInputData

		var u ui.UI
		u, err = ui.NewTerminalUI(doc, recorder, useTTYForInput)
		if err != nil {
			return err
		}
		userInterface = u

	case UserInterfaceHTML:
		var u ui.UI
		u, err = html.NewHTMLUserInterface(doc, recorder)
		if err != nil {
			return err
		}
		// Only run server if the UI is actually an HTML UI
		if htmlUI, ok := u.(*html.HTMLUserInterface); ok {
			go func() {
				if err := htmlUI.RunServer(ctx); err != nil {
					klog.Fatalf("error running http server: %v", err)
				}
			}()
		}
		userInterface = u

	default:
		return fmt.Errorf("user-interface mode %q is not known", opt.UserInterface)
	}

	conversation := &agent.Conversation{
		Model:              opt.ModelID,
		Kubeconfig:         opt.KubeConfigPath,
		LLM:                llmClient,
		MaxIterations:      opt.MaxIterations,
		PromptTemplateFile: opt.PromptTemplateFilePath,
		ExtraPromptPaths:   opt.ExtraPromptPaths,
		Tools:              tools.Default(),
		Recorder:           recorder,
		RemoveWorkDir:      opt.RemoveWorkDir,
		SkipPermissions:    opt.SkipPermissions,
		EnableToolUseShim:  opt.EnableToolUseShim,
	}

	err = conversation.Init(ctx, doc)
	if err != nil {
		return fmt.Errorf("starting conversation: %w", err)
	}
	defer conversation.Close()

	chatSession := session{
		model:        opt.ModelID,
		doc:          doc,
		ui:           userInterface,
		conversation: conversation,
		LLM:          llmClient,
	}

	if opt.Quiet {
		if queryFromCmd == "" {
			return fmt.Errorf("quiet mode requires a query to be provided as a positional argument")
		}
		return chatSession.answerQuery(ctx, queryFromCmd)
	}

	return chatSession.repl(ctx, queryFromCmd)
}

// session represents the user chat session (interactive/non-interactive both)
type session struct {
	model           string
	ui              ui.UI
	doc             *ui.Document
	conversation    *agent.Conversation
	availableModels []string
	LLM             gollm.Client
}

// repl is a read-eval-print loop for the chat session.
func (s *session) repl(ctx context.Context, initialQuery string) error {
	query := initialQuery
	if query == "" {
		s.doc.AddBlock(ui.NewAgentTextBlock().WithText("Hey there, what can I help you with today?"))
	}
	for {
		if query == "" {
			input := ui.NewInputTextBlock()
			s.doc.AddBlock(input)

			userInput, err := input.Observable().Wait()
			if err != nil {
				if err == io.EOF {
					// Use hit control-D, or was piping and we reached the end of stdin.
					// Not a "big" problem
					return nil
				}
				return fmt.Errorf("reading input: %w", err)
			}
			query = strings.TrimSpace(userInput)
		}

		switch {
		case query == "":
			continue
		case query == "reset":
			err := s.conversation.Init(ctx, s.doc)
			if err != nil {
				return err
			}
		case query == "clear":
			s.ui.ClearScreen()
		case query == "exit" || query == "quit":
			// s.ui.RenderOutput(ctx, "Allright...bye.\n")
			return nil
		default:
			if err := s.answerQuery(ctx, query); err != nil {
				errorBlock := &ui.ErrorBlock{}
				errorBlock.SetText(fmt.Sprintf("Error: %v\n", err))
				s.doc.AddBlock(errorBlock)
			}
		}
		// Reset query to empty string so that we prompt for input again
		query = ""
	}
}

func (s *session) listModels(ctx context.Context) ([]string, error) {
	if s.availableModels == nil {
		modelNames, err := s.LLM.ListModels(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing models: %w", err)
		}
		s.availableModels = modelNames
	}
	return s.availableModels, nil
}

func (s *session) answerQuery(ctx context.Context, query string) error {
	switch {
	case query == "model":
		infoBlock := &ui.AgentTextBlock{}
		infoBlock.AppendText(fmt.Sprintf("Current model is `%s`\n", s.model))
		s.doc.AddBlock(infoBlock)

	case query == "version":
		infoBlock := &ui.AgentTextBlock{}
		infoBlock.AppendText(fmt.Sprintf("Version: `%s`\n", version))
		s.doc.AddBlock(infoBlock)

	case query == "models":
		models, err := s.listModels(ctx)
		if err != nil {
			return fmt.Errorf("listing models: %w", err)
		}
		infoBlock := &ui.AgentTextBlock{}
		infoBlock.AppendText("\n  Available models:\n")
		infoBlock.AppendText(strings.Join(models, "\n"))
		s.doc.AddBlock(infoBlock)

	case query == "tools":
		if s.conversation == nil {
			return fmt.Errorf("listing tols: conversation is not initialized")
		}
		infoBlock := &ui.AgentTextBlock{}
		infoBlock.AppendText("\n  Available tools:\n")
		infoBlock.AppendText(strings.Join(s.conversation.Tools.Names(), "\n"))
		s.doc.AddBlock(infoBlock)

	default:
		return s.conversation.RunOneRound(ctx, query)
	}
	return nil
}

// Redirect standard log output to our custom klog writer
// This is primarily to suppress warning messages from
// genai library https://github.com/googleapis/go-genai/blob/6ac4afc0168762dc3b7a4d940fc463cc1854f366/types.go#L1633
func redirectStdLogToKlog() {
	log.SetOutput(klogWriter{})

	// Disable standard log's prefixes (date, time, file info)
	// because klog will add its own more detailed prefix.
	log.SetFlags(0)
}

// Define a custom writer that forwards messages to klog.Warning
type klogWriter struct{}

// Implement the io.Writer interface
func (writer klogWriter) Write(data []byte) (n int, err error) {
	// We trim the trailing newline because klog adds its own.
	message := string(bytes.TrimSuffix(data, []byte("\n")))
	klog.Warning(message)
	return len(data), nil
}

func hasStdInData() (bool, error) {
	hasData := false

	stat, err := os.Stdin.Stat()
	if err != nil {
		return hasData, fmt.Errorf("checking stdin: %w", err)
	}
	hasData = (stat.Mode() & os.ModeCharDevice) == 0

	return hasData, nil
}

// resolveQueryInput determines the query input from positional args and/or stdin.
// It supports:
// - 1 positional arg only -> kubectl-ai "get pods"
// - stdin only -> echo "get pods" | kubectl-ai
// - 1 positional arg + stdin (combined) -> kubectl-ai get <<< "pods" or kubectl-ai "get" <<< "pods"
// As default no positional arg nor stdin
func resolveQueryInput(hasStdInData bool, args []string) (string, error) {
	switch {
	case len(args) == 1 && !hasStdInData:
		// Use argument directly
		return args[0], nil

	case len(args) == 1 && hasStdInData:
		// Combine arg + stdin
		var b strings.Builder
		b.WriteString(args[0])
		b.WriteString("\n")

		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			b.WriteString(scanner.Text())
			b.WriteString("\n")
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		query := strings.TrimSpace(b.String())
		if query == "" {
			return "", fmt.Errorf("no query provided from stdin")
		}
		return query, nil

	case len(args) == 0 && hasStdInData:
		// Read stdin only
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		query := strings.TrimSpace(string(b))
		if query == "" {
			return "", fmt.Errorf("no query provided from stdin")
		}
		return query, nil

	default:
		// Case: No input at all — return empty string, no error
		return "", nil
	}
}

func resolveKubeConfigPath(opt *Options) error {
	switch {
	case opt.KubeConfigPath != "":
		// Already set from flag or viper env
	case os.Getenv("KUBECONFIG") != "":
		opt.KubeConfigPath = os.Getenv("KUBECONFIG")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get user home directory: %w", err)
		}
		opt.KubeConfigPath = filepath.Join(home, ".kube", "config")
	}

	return nil
}

func startMCPServer(ctx context.Context, opt Options) error {
	workDir := filepath.Join(os.TempDir(), "kubectl-ai-mcp")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("error creating work directory: %w", err)
	}
	mcpServer, err := newKubectlMCPServer(ctx, opt.KubeConfigPath, tools.Default(), workDir)
	if err != nil {
		return fmt.Errorf("creating mcp server: %w", err)
	}
	return mcpServer.Serve(ctx)
}
