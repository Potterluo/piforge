package pi

import (
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// retained reports whether an event must survive a slow subscriber. Only
// cosmetic progress may be dropped: losing message.final or run.settled would
// leave the UI stuck on "running" forever, so those are always kept.
func retained(eventType string) bool {
	switch eventType {
	case contracts.EvMsgDelta,
		contracts.EvToolProgress,
		contracts.EvContextUsage,
		contracts.EvQueueChanged,
		contracts.EvPtyStream:
		return false
	}
	return true
}

// Subscription is one consumer of the normalized event stream.
//
// The channel is closed when the subscription ends, either because the caller
// closed it or because the bridge shut down, so a range loop terminates on its
// own. Every event is delivered in order; when a consumer falls behind,
// cosmetic events are dropped and counted by Dropped, while retained events
// (message.final, run.settled, tool boundaries) are never dropped.
type Subscription struct {
	C <-chan contracts.Event

	sub *subscriber
}

// Dropped reports how many cosmetic events were discarded because this
// consumer did not keep up.
func (s *Subscription) Dropped() int64 {
	if s == nil || s.sub == nil {
		return 0
	}
	return s.sub.droppedCount()
}

// Close stops delivery. It is idempotent and never blocks on the consumer.
func (s *Subscription) Close() {
	if s == nil || s.sub == nil {
		return
	}
	s.sub.close()
}

// subscriber is the bridge's per-consumer state. The read loop only ever
// appends to pending and pokes notify, so it can never block on a consumer; a
// dedicated pump moves pending into the channel in order.
type subscriber struct {
	b        *Bridge
	ch       chan contracts.Event
	notify   chan struct{}
	quit     chan struct{}
	finished chan struct{}

	mu      sync.Mutex
	pending []contracts.Event
	dropped int64
	closed  bool
	once    sync.Once
}

// Subscribe registers a consumer. It must be called before Start so no record
// that arrives during startup is lost. On a bridge that is already closed it
// returns an already-finished subscription rather than one that would leak its
// pump goroutine.
func (b *Bridge) Subscribe() *Subscription {
	s := &subscriber{
		b:        b,
		ch:       make(chan contracts.Event, b.cfg.SubscriberBuffer),
		notify:   make(chan struct{}, 1),
		quit:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	select {
	case <-b.done:
		s.closed = true
		close(s.ch)
		close(s.finished)
		return &Subscription{C: s.ch, sub: s}
	default:
	}
	b.subsMu.Lock()
	b.subs[s] = struct{}{}
	b.subsMu.Unlock()
	go s.pump()
	return &Subscription{C: s.ch, sub: s}
}

// fanout appends events to every subscriber without blocking.
func (b *Bridge) fanout(events []contracts.Event) {
	if len(events) == 0 {
		return
	}
	b.subsMu.Lock()
	targets := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		targets = append(targets, s)
	}
	b.subsMu.Unlock()
	for _, s := range targets {
		for _, ev := range events {
			s.push(ev)
		}
	}
}

// push queues one event for the pump. Callers never block: when the backlog is
// full a cosmetic event is dropped, and to make room for a retained event the
// oldest cosmetic event is evicted first.
func (s *subscriber) push(ev contracts.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.pending) >= s.b.cfg.MaxBacklog {
		if !retained(ev.Type) {
			s.dropped++
			s.mu.Unlock()
			return
		}
		if !s.evictCosmeticLocked() && len(s.pending) >= 2*s.b.cfg.MaxBacklog {
			// Nothing cosmetic left to sacrifice and the hard cap is reached.
			// This only happens for a consumer that has stopped reading
			// entirely; dropping here is the last resort and is counted.
			s.dropped++
			s.mu.Unlock()
			return
		}
	}
	s.pending = append(s.pending, ev)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// evictCosmeticLocked removes the oldest droppable event. Callers hold mu.
func (s *subscriber) evictCosmeticLocked() bool {
	for i, ev := range s.pending {
		if retained(ev.Type) {
			continue
		}
		s.pending = append(s.pending[:i], s.pending[i+1:]...)
		s.dropped++
		return true
	}
	return false
}

func (s *subscriber) droppedCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// pump drains pending into the channel. It exits when the subscription is
// closed or the bridge shuts down, and closes the channel so consumers see a
// clean end of stream.
func (s *subscriber) pump() {
	defer func() {
		close(s.ch)
		close(s.finished)
	}()
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-s.notify:
			case <-s.quit:
				return
			}
			continue
		}
		ev := s.pending[0]
		s.pending = s.pending[1:]
		s.mu.Unlock()
		select {
		case s.ch <- ev:
		case <-s.quit:
			return
		}
	}
}

func (s *subscriber) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.quit)
		select {
		case s.notify <- struct{}{}:
		default:
		}
	})
}

// closeSubs ends every subscription and waits (briefly) for the pumps to stop.
func (b *Bridge) closeSubs() {
	b.subsMu.Lock()
	subs := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = map[*subscriber]struct{}{}
	b.subsMu.Unlock()
	for _, s := range subs {
		s.close()
	}
	for _, s := range subs {
		select {
		case <-s.finished:
		case <-time.After(time.Second):
		}
	}
}
