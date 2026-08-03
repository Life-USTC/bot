package agent

import (
	"sync"
)

// followUpInbox holds user messages that arrive while an agent run is active.
// They are injected into the current ChatModelAgent state before the next model call.
type followUpInbox struct {
	mu       sync.Mutex
	messages []Input
}

func newFollowUpInbox() *followUpInbox {
	return &followUpInbox{}
}

func (b *followUpInbox) Push(input Input) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, input)
}

func (b *followUpInbox) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.messages)
}

func (b *followUpInbox) Drain() []Input {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.messages) == 0 {
		return nil
	}
	out := append([]Input(nil), b.messages...)
	b.messages = nil
	return out
}
