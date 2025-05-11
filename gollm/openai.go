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

package gollm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"k8s.io/klog/v2"
)

// Register the OpenAI provider factory on package initialization.
func init() {
	if err := RegisterProvider("openai", newOpenAIClientFactory); err != nil {
		klog.Fatalf("Failed to register openai provider: %v", err)
	}
}

// newOpenAIClientFactory is the factory function for creating OpenAI clients.
func newOpenAIClientFactory(ctx context.Context, _ *url.URL) (Client, error) {
	// The URL is not currently used for OpenAI config, relies on env vars.
	return NewOpenAIClient(ctx)
}

// OpenAIClient implements the gollm.Client interface for OpenAI models.
type OpenAIClient struct {
	client openai.Client
}

// Ensure OpenAIClient implements the Client interface.
var _ Client = &OpenAIClient{}

// NewOpenAIClient creates a new client for interacting with OpenAI.
// It reads the API key and optional endpoint from environment variables
// OPENAI_API_KEY and OPENAI_ENDPOINT.
func NewOpenAIClient(ctx context.Context) (*OpenAIClient, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		// The NewClient might handle this, but explicit check is safer
		return nil, errors.New("OPENAI_API_KEY environment variable not set")
	}

	endpoint := os.Getenv("OPENAI_ENDPOINT")
	if endpoint != "" {
		klog.Infof("Using custom OpenAI endpoint: %s", endpoint)
		return &OpenAIClient{
			client: openai.NewClient(option.WithBaseURL(endpoint)),
		}, nil
	}

	return &OpenAIClient{
		client: openai.NewClient(),
	}, nil
}

// Close cleans up any resources used by the client.
func (c *OpenAIClient) Close() error {
	// No specific cleanup needed for the OpenAI client currently.
	return nil
}

// StartChat starts a new chat session.
func (c *OpenAIClient) StartChat(systemPrompt, model string) Chat {
	// Default to gpt-4o if no model is specified or if it doesn't look like a known OpenAI prefix
	if model == "" {
		model = "gpt-4o"
		klog.V(1).Info("No model specified, defaulting to gpt-4o")
	}
	klog.V(1).Infof("Starting new OpenAI chat session with model: %s", model)
	// Initialize history with system prompt if provided
	history := []openai.ChatCompletionMessageParamUnion{}
	if systemPrompt != "" {
		history = append(history, openai.SystemMessage(systemPrompt))
	}

	return &openAIChatSession{
		client:  c.client, // Pass the client from OpenAIClient
		history: history,
		model:   model,
		// functionDefinitions and tools will be set later via SetFunctionDefinitions
	}
}

// simpleCompletionResponse is a basic implementation of CompletionResponse.
type simpleCompletionResponse struct {
	content string
}

// Response returns the completion content.
func (r *simpleCompletionResponse) Response() string {
	return r.content
}

// UsageMetadata returns nil for now.
func (r *simpleCompletionResponse) UsageMetadata() any {
	return nil
}

// GenerateCompletion sends a completion request to the OpenAI API.
func (c *OpenAIClient) GenerateCompletion(ctx context.Context, req *CompletionRequest) (CompletionResponse, error) {
	klog.Infof("OpenAI GenerateCompletion called with model: %s", req.Model)
	klog.V(1).Infof("Prompt:\n%s", req.Prompt)

	// Use the Chat Completions API as shown in examples
	chatReq := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(req.Model), // Use the model specified in the request
		Messages: []openai.ChatCompletionMessageParamUnion{
			// Assuming a simple user message structure for now
			openai.UserMessage(req.Prompt),
		},
	}

	completion, err := c.client.Chat.Completions.New(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("failed to generate OpenAI completion: %w", err)
	}

	// Check if there are choices and a message
	if len(completion.Choices) == 0 || completion.Choices[0].Message.Content == "" {
		return nil, errors.New("received an empty response from OpenAI")
	}

	// Return the content of the first choice
	resp := &simpleCompletionResponse{
		content: completion.Choices[0].Message.Content,
	}

	return resp, nil
}

// SetResponseSchema is not implemented yet.
func (c *OpenAIClient) SetResponseSchema(schema *Schema) error {
	klog.Warning("OpenAIClient.SetResponseSchema is not implemented yet")
	return nil
}

// ListModels returns a slice of strings with model IDs.
// Note: This may not work with all OpenAI-compatible providers if they don't fully implement
// the Models.List endpoint or return data in a different format.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]string, error) {

	var opts []option.RequestOption

	endpoint := os.Getenv("OPENAI_ENDPOINT") // if another endpoint is used
	if endpoint != "" {
		opts = append(opts, option.WithBaseURL(endpoint))
	}

	res, err := c.client.Models.List(ctx, opts...)
	if err != nil {

		if endpoint != "" {
			return nil, fmt.Errorf(`
			There was an error in listing models from %s. 
			Please verify if the endpoint used is fully OpenAI compatible`,
				endpoint)
		}
		return nil, fmt.Errorf("There was an error in listing models from OpenAI")
	}

	modelsIDs := make([]string, 0, len(res.Data))
	for _, model := range res.Data {
		modelsIDs = append(modelsIDs, model.ID)
	}

	return modelsIDs, nil
}

// --- Chat Session Implementation ---

type openAIChatSession struct {
	client              openai.Client
	history             []openai.ChatCompletionMessageParamUnion
	model               string
	functionDefinitions []*FunctionDefinition            // Stored in gollm format
	tools               []openai.ChatCompletionToolParam // Stored in OpenAI format
}

// Ensure openAIChatSession implements the Chat interface.
var _ Chat = (*openAIChatSession)(nil)

// SetFunctionDefinitions stores the function definitions and converts them to OpenAI format.
func (cs *openAIChatSession) SetFunctionDefinitions(defs []*FunctionDefinition) error {
	cs.functionDefinitions = defs
	cs.tools = nil // Clear previous tools
	if len(defs) > 0 {
		cs.tools = make([]openai.ChatCompletionToolParam, len(defs))
		for i, gollmDef := range defs {
			// Basic conversion, assuming schema is compatible or nil
			var params openai.FunctionParameters
			if gollmDef.Parameters != nil {
				// NOTE: This assumes gollmDef.Parameters is directly marshalable to JSON
				// that fits openai.FunctionParameters. May need refinement.
				bytes, err := gollmDef.Parameters.ToRawSchema()
				if err != nil {
					return fmt.Errorf("failed to convert schema for function %s: %w", gollmDef.Name, err)
				}
				if err := json.Unmarshal(bytes, &params); err != nil {
					return fmt.Errorf("failed to unmarshal schema for function %s: %w", gollmDef.Name, err)
				}
			}
			cs.tools[i] = openai.ChatCompletionToolParam{
				Function: openai.FunctionDefinitionParam{
					Name:        gollmDef.Name,
					Description: openai.String(gollmDef.Description),
					Parameters:  params,
				},
			}
		}
	}
	klog.V(1).Infof("Set %d function definitions for OpenAI chat session", len(cs.functionDefinitions))
	return nil
}

// Send sends the user message(s), appends to history, and gets the LLM response.
func (cs *openAIChatSession) Send(ctx context.Context, contents ...any) (ChatResponse, error) {
	klog.V(1).InfoS("openAIChatSession.Send called", "model", cs.model, "history_len", len(cs.history))

	// 1. Append user message(s) to history
	for _, content := range contents {
		switch c := content.(type) {
		case string:
			klog.V(2).Infof("Adding user message to history: %s", c)
			cs.history = append(cs.history, openai.UserMessage(c))
		case FunctionCallResult:
			klog.V(2).Infof("Adding tool call result to history: Name=%s, ID=%s", c.Name, c.ID)
			// Marshal the result map into a JSON string for the message content
			resultJSON, err := json.Marshal(c.Result)
			if err != nil {
				klog.Errorf("Failed to marshal function call result: %v", err)
				return nil, fmt.Errorf("failed to marshal function call result %q: %w", c.Name, err)
			}
			cs.history = append(cs.history, openai.ToolMessage(string(resultJSON), c.ID))
		default:
			// TODO: Handle other content types if necessary?
			klog.Warningf("Unhandled content type in Send: %T", content)
			return nil, fmt.Errorf("unhandled content type: %T", content)
		}
	}

	// 2. Prepare the API request
	chatReq := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(cs.model),
		Messages: cs.history,
	}
	if len(cs.tools) > 0 {
		chatReq.Tools = cs.tools
		// chatReq.ToolChoice = openai.ToolChoiceAuto // Or specify if needed
	}

	// 3. Call the OpenAI API
	klog.V(1).InfoS("Sending request to OpenAI Chat API", "model", cs.model, "messages", len(chatReq.Messages), "tools", len(chatReq.Tools))
	completion, err := cs.client.Chat.Completions.New(ctx, chatReq)
	if err != nil {
		// TODO: Check if error is retryable using cs.IsRetryableError
		klog.Errorf("OpenAI ChatCompletion API error: %v", err)
		return nil, fmt.Errorf("OpenAI chat completion failed: %w", err)
	}
	klog.V(1).InfoS("Received response from OpenAI Chat API", "id", completion.ID, "choices", len(completion.Choices))

	// 4. Process the response
	if len(completion.Choices) == 0 {
		klog.Warning("Received response with no choices from OpenAI")
		return nil, errors.New("received empty response from OpenAI (no choices)")
	}

	// Add assistant's response (first choice) to history
	assistantMsg := completion.Choices[0].Message
	// Convert to param type before appending to history
	cs.history = append(cs.history, assistantMsg.ToParam())
	klog.V(2).InfoS("Added assistant message to history", "content_present", assistantMsg.Content != "", "tool_calls", len(assistantMsg.ToolCalls))

	// Wrap the response
	resp := &openAIChatResponse{
		openaiCompletion: completion,
	}

	return resp, nil
}

// SendStreaming sends the user message(s) and returns an iterator for the LLM response stream.
// This implementation uses the OpenAI streaming API to provide genuine streaming functionality.
func (cs *openAIChatSession) SendStreaming(ctx context.Context, contents ...any) (ChatResponseIterator, error) {
	klog.V(1).InfoS("openAIChatSession.SendStreaming called (actual streaming)", "model", cs.model)

	// 1. Append user message(s) to history - same as in Send
	for _, content := range contents {
		switch c := content.(type) {
		case string:
			klog.V(2).Infof("Adding user message to history: %s", c)
			cs.history = append(cs.history, openai.UserMessage(c))
		case FunctionCallResult:
			klog.V(2).Infof("Adding tool call result to history: Name=%s, ID=%s", c.Name, c.ID)
			resultJSON, err := json.Marshal(c.Result)
			if err != nil {
				klog.Errorf("Failed to marshal function call result: %v", err)
				return nil, fmt.Errorf("failed to marshal function call result %q: %w", c.Name, err)
			}
			cs.history = append(cs.history, openai.ToolMessage(string(resultJSON), c.ID))
		default:
			klog.Warningf("Unhandled content type in SendStreaming: %T", content)
			return nil, fmt.Errorf("unhandled content type: %T", content)
		}
	}

	// 2. Prepare the API request - same parameters as in Send
	chatReq := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(cs.model),
		Messages: cs.history,
	}
	if len(cs.tools) > 0 {
		chatReq.Tools = cs.tools
	}

	// 3. Start the OpenAI streaming request
	klog.V(1).InfoS("Starting OpenAI streaming request", "model", cs.model, "messages", len(chatReq.Messages), "tools", len(chatReq.Tools))
	stream := cs.client.Chat.Completions.NewStreaming(ctx, chatReq)

	// Create an accumulator to track the full response
	acc := openai.ChatCompletionAccumulator{}

	// 4. Create and return the stream iterator
	return func(yield func(ChatResponse, error) bool) {
		var lastResponseChunk *openAIChatStreamResponse

		// Process stream chunks
		for stream.Next() {
			chunk := stream.Current()

			// Update the accumulator with the new chunk
			acc.AddChunk(chunk)

			// Create a streaming response for this chunk
			streamResponse := &openAIChatStreamResponse{
				streamChunk: chunk,
				accumulator: acc,
			}

			// Keep track of the last response to append to history
			lastResponseChunk = streamResponse

			// Yield the streaming response
			if !yield(streamResponse, nil) {
				// Consumer wants to stop
				break
			}
		}

		// Check for errors after streaming completes
		if err := stream.Err(); err != nil {
			klog.Errorf("Error in OpenAI streaming: %v", err)
			yield(nil, fmt.Errorf("OpenAI streaming error: %w", err))
			return
		}

		// Once streaming is complete, update the conversation history with the complete message
		if lastResponseChunk != nil && acc.Choices != nil && len(acc.Choices) > 0 {
			// The accumulator has the complete message
			completeMessage := openai.ChatCompletionMessage{
				Content:   acc.Choices[0].Message.Content,
				Role:      acc.Choices[0].Message.Role,
				ToolCalls: acc.Choices[0].Message.ToolCalls,
			}

			// Append the full assistant response to history
			cs.history = append(cs.history, completeMessage.ToParam())
			klog.V(2).InfoS("Added complete assistant message to history",
				"content_present", completeMessage.Content != "",
				"tool_calls", len(completeMessage.ToolCalls))
		}
	}, nil
}

// IsRetryableError determines if an error from the OpenAI API should be retried.
// This specifically focuses on detecting streaming errors to enable fallback to non-streaming mode.
func (cs *openAIChatSession) IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Common error strings for streaming issues
	streamingErrorPatterns := []string{
		"streaming",
		"stream",
		"sse",
		"server-sent events",
		"unexpected EOF",
		"broken pipe",
	}

	// Check if the error message contains indicators of streaming issues
	errMsg := strings.ToLower(err.Error())
	for _, pattern := range streamingErrorPatterns {
		if strings.Contains(errMsg, pattern) {
			klog.V(1).Infof("Detected streaming error, will retry with non-streaming: %v", err)
			return true
		}
	}

	return false
}

// --- Helper structs for ChatResponse interface ---

type openAIChatResponse struct {
	openaiCompletion *openai.ChatCompletion
}

var _ ChatResponse = (*openAIChatResponse)(nil)

func (r *openAIChatResponse) UsageMetadata() any {
	// Check if the main completion object and Usage exist
	if r.openaiCompletion != nil && r.openaiCompletion.Usage.TotalTokens > 0 { // Check a field within Usage
		return r.openaiCompletion.Usage
	}
	return nil
}

func (r *openAIChatResponse) Candidates() []Candidate {
	if r.openaiCompletion == nil {
		return nil
	}
	candidates := make([]Candidate, len(r.openaiCompletion.Choices))
	for i, choice := range r.openaiCompletion.Choices {
		candidates[i] = &openAICandidate{openaiChoice: &choice}
	}
	return candidates
}

type openAICandidate struct {
	openaiChoice *openai.ChatCompletionChoice
}

var _ Candidate = (*openAICandidate)(nil)

func (c *openAICandidate) Parts() []Part {
	// Check if the choice exists before accessing Message
	if c.openaiChoice == nil {
		return nil
	}

	// OpenAI message can have Content AND ToolCalls
	var parts []Part
	if c.openaiChoice.Message.Content != "" {
		parts = append(parts, &openAIPart{content: c.openaiChoice.Message.Content})
	}
	if len(c.openaiChoice.Message.ToolCalls) > 0 {
		parts = append(parts, &openAIPart{toolCalls: c.openaiChoice.Message.ToolCalls})
	}
	return parts
}

// String provides a simple string representation for logging/debugging.
func (c *openAICandidate) String() string {
	if c.openaiChoice == nil {
		return "<nil candidate>"
	}
	content := "<no content>"
	if c.openaiChoice.Message.Content != "" {
		content = c.openaiChoice.Message.Content
	}
	toolCalls := len(c.openaiChoice.Message.ToolCalls)
	finishReason := string(c.openaiChoice.FinishReason)
	return fmt.Sprintf("Candidate(FinishReason: %s, ToolCalls: %d, Content: %q)", finishReason, toolCalls, content)
}

type openAIPart struct {
	content   string
	toolCalls []openai.ChatCompletionMessageToolCall // Correct type
}

var _ Part = (*openAIPart)(nil)

func (p *openAIPart) AsText() (string, bool) {
	return p.content, p.content != ""
}

func (p *openAIPart) AsFunctionCalls() ([]FunctionCall, bool) {
	if len(p.toolCalls) == 0 {
		return nil, false
	}

	// Convert only complete function calls
	var completeCalls []FunctionCall
	for _, tc := range p.toolCalls {
		// Check if it's a function call by seeing if Function Name is populated
		if tc.Function.Name == "" { // Adjusted check for function calls
			klog.V(2).Infof("Skipping non-function tool call ID: %s", tc.ID)
			continue
		}
		var args map[string]any
		// Attempt to unmarshal arguments, ignore error for now if it fails
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)

		completeCalls = append(completeCalls, FunctionCall{
			ID:        tc.ID, // Pass the Tool Call ID
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return completeCalls, true
}

// openAIChatStreamResponse represents a streaming response chunk from OpenAI.
type openAIChatStreamResponse struct {
	streamChunk openai.ChatCompletionChunk
	accumulator openai.ChatCompletionAccumulator
}

// Ensure the streaming response implements ChatResponse interface.
var _ ChatResponse = (*openAIChatStreamResponse)(nil)

// UsageMetadata returns usage metadata if available in the final chunk.
func (r *openAIChatStreamResponse) UsageMetadata() any {
	if r.accumulator.Usage.TotalTokens > 0 {
		return r.accumulator.Usage
	}
	return nil
}

// Candidates returns a slice with a single streaming candidate.
func (r *openAIChatStreamResponse) Candidates() []Candidate {
	// Each streaming chunk gets converted to a candidate
	if len(r.streamChunk.Choices) == 0 {
		return nil
	}

	candidates := make([]Candidate, len(r.streamChunk.Choices))
	for i, choice := range r.streamChunk.Choices {
		candidates[i] = &openAIStreamCandidate{streamChoice: choice}
	}
	return candidates
}

// openAIStreamCandidate adapts a streaming chunk choice to the Candidate interface.
type openAIStreamCandidate struct {
	streamChoice openai.ChatCompletionChunkChoice
}

// Ensure the streaming candidate implements Candidate interface.
var _ Candidate = (*openAIStreamCandidate)(nil)

// String provides a string representation of the candidate.
func (c *openAIStreamCandidate) String() string {
	return fmt.Sprintf("StreamingCandidate(Index: %d, FinishReason: %s)",
		c.streamChoice.Index, c.streamChoice.FinishReason)
}

// Parts returns the parts of this streaming chunk candidate.
func (c *openAIStreamCandidate) Parts() []Part {
	var parts []Part

	// Include text content if present
	if c.streamChoice.Delta.Content != "" {
		parts = append(parts, &openAIStreamPart{
			content: c.streamChoice.Delta.Content,
		})
	}

	// Include tool calls if present
	if len(c.streamChoice.Delta.ToolCalls) > 0 {
		parts = append(parts, &openAIStreamPart{
			toolCalls: c.streamChoice.Delta.ToolCalls,
		})
	}

	return parts
}

// openAIStreamPart adapts streaming parts to the Part interface.
type openAIStreamPart struct {
	content   string
	toolCalls []openai.ChatCompletionChunkChoiceDeltaToolCall
}

// Ensure the streaming part implements Part interface.
var _ Part = (*openAIStreamPart)(nil)

// AsText returns the text content of this part if it has any.
func (p *openAIStreamPart) AsText() (string, bool) {
	return p.content, p.content != ""
}

// AsFunctionCalls returns the function calls from this part if it has any.
func (p *openAIStreamPart) AsFunctionCalls() ([]FunctionCall, bool) {
	if len(p.toolCalls) == 0 {
		return nil, false
	}

	// Count valid function calls first
	validCount := 0
	for _, tc := range p.toolCalls {
		// Only count tool calls that have a function name
		if tc.Function.Name != "" {
			validCount++
		}
	}

	// If no valid function calls, return nil
	if validCount == 0 {
		return nil, false
	}

	// Create properly sized array
	completeCalls := make([]FunctionCall, 0, validCount)

	// Process tool calls
	for _, tc := range p.toolCalls {
		// Skip tool calls that don't have a complete function definition yet
		if tc.Function.Name == "" {
			continue
		}

		var args map[string]any
		// Attempt to unmarshal arguments if present
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				klog.V(2).Infof("Error unmarshaling function arguments: %v", err)
				// Continue with empty args if unmarshal fails
				args = make(map[string]any)
			}
		} else {
			// Initialize empty args map if no arguments provided
			args = make(map[string]any)
		}

		completeCalls = append(completeCalls, FunctionCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	return completeCalls, len(completeCalls) > 0
}
