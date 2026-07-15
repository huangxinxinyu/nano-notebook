package app

import "sync"

type runHub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func newRunHub() *runHub {
	return &runHub{subscribers: make(map[string]map[chan struct{}]struct{})}
}

func (h *runHub) subscribe(runID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subscribers[runID] == nil {
		h.subscribers[runID] = make(map[chan struct{}]struct{})
	}
	h.subscribers[runID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers[runID], ch)
		if len(h.subscribers[runID]) == 0 {
			delete(h.subscribers, runID)
		}
		h.mu.Unlock()
	}
}

func (h *runHub) notify(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if runID == "" {
		for _, subscribers := range h.subscribers {
			wakeSubscribers(subscribers)
		}
		return
	}
	wakeSubscribers(h.subscribers[runID])
}

func wakeSubscribers(subscribers map[chan struct{}]struct{}) {
	for ch := range subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
