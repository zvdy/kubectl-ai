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

package sessions

import (
	"sync"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/api"
)

// InMemoryChatStore is an in-memory implementation of the api.ChatMessageStore interface.
// It stores chat messages in a slice and is safe for concurrent use.
type InMemoryChatStore struct {
	mu       sync.RWMutex
	messages []*api.Message
}

// NewInMemoryChatStore creates a new InMemoryChatStore.
func NewInMemoryChatStore() *InMemoryChatStore {
	return &InMemoryChatStore{
		messages: make([]*api.Message, 0),
	}
}

// AddChatMessage adds a message to the store.
func (s *InMemoryChatStore) AddChatMessage(record *api.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, record)
	return nil
}

// SetChatMessages replaces the entire chat history with a new one.
func (s *InMemoryChatStore) SetChatMessages(newHistory []*api.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = newHistory
	return nil
}

// ChatMessages returns all chat messages from the store.
func (s *InMemoryChatStore) ChatMessages() []*api.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy to prevent race conditions on the slice.
	messageCopy := make([]*api.Message, len(s.messages))
	copy(messageCopy, s.messages)
	return messageCopy
}

// ClearChatMessages removes all messages from the store.
func (s *InMemoryChatStore) ClearChatMessages() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]*api.Message, 0)
	return nil
}
