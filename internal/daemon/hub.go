package daemon

import "sync"

// Hub wakes all waiters when a new message is stored. Each waiter takes the
// channel before it reads the database, so it cannot miss a message.
type Hub struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewHub returns a hub.
func NewHub() *Hub { return &Hub{ch: make(chan struct{})} }

// Wait returns a channel that closes at the next Notify.
func (h *Hub) Wait() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ch
}

// Notify wakes all waiters.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.ch)
	h.ch = make(chan struct{})
}
