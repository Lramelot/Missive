package main

import "sync"

type orderingEmitter struct {
	inner     Emitter
	store     *Store
	mu        sync.Mutex
	announced map[string]bool
}

func newOrderingEmitter(inner Emitter, store *Store) *orderingEmitter {
	return &orderingEmitter{inner: inner, store: store, announced: make(map[string]bool)}
}

func (o *orderingEmitter) markAnnounced(threads []Thread) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, th := range threads {
		o.announced[th.ID] = true
	}
}

func (o *orderingEmitter) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.announced = make(map[string]bool)
}

func (o *orderingEmitter) Emit(event string, data any) {
	switch event {
	case EventThreads:
		if te, ok := data.(ThreadsEvent); ok {
			o.markAnnounced(te.Threads)
		}
	case EventThreadRemoved:
		if tr, ok := data.(ThreadRemovedEvent); ok {
			o.mu.Lock()
			delete(o.announced, tr.ThreadID)
			o.mu.Unlock()
		}
	case EventMessage:
		if msg, ok := data.(Message); ok {
			o.mu.Lock()
			seen := o.announced[msg.ThreadID]
			o.mu.Unlock()
			if !seen {
				if view, found := o.store.ThreadView(msg.ThreadID); found {
					o.markAnnounced([]Thread{view})
					o.inner.Emit(EventThreads, ThreadsEvent{Threads: []Thread{view}})
				}
			}
		}
	}
	o.inner.Emit(event, data)
}
