package cloud

import (
	"context"
	"sync"

	"cloudnvr/internal/domain"
)

type ptzBroker struct {
	mu      sync.Mutex
	queues  map[string][]domain.PTZCommand
	notices map[string]chan struct{}
	pending map[string]ptzPending
}

type ptzPending struct {
	siteID string
	result chan domain.PTZResult
}

func newPTZBroker() *ptzBroker {
	return &ptzBroker{queues: make(map[string][]domain.PTZCommand), notices: make(map[string]chan struct{}), pending: make(map[string]ptzPending)}
}

func (b *ptzBroker) enqueue(siteID string, command domain.PTZCommand) <-chan domain.PTZResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make(chan domain.PTZResult, 1)
	b.pending[command.ID] = ptzPending{siteID: siteID, result: result}
	queue := b.queues[siteID]
	if len(queue) >= 50 {
		dropped := queue[0]
		if pending, ok := b.pending[dropped.ID]; ok {
			delete(b.pending, dropped.ID)
			pending.result <- domain.PTZResult{ID: dropped.ID, Success: false, Error: "PTZ queue is full"}
			close(pending.result)
		}
		queue = queue[1:]
	}
	b.queues[siteID] = append(queue, command)
	notice := b.noticeLocked(siteID)
	select {
	case notice <- struct{}{}:
	default:
	}
	return result
}

func (b *ptzBroker) resolve(siteID string, result domain.PTZResult) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending, ok := b.pending[result.ID]
	if !ok || pending.siteID != siteID {
		return false
	}
	delete(b.pending, result.ID)
	pending.result <- result
	close(pending.result)
	return true
}

func (b *ptzBroker) abandon(commandID string) {
	b.mu.Lock()
	delete(b.pending, commandID)
	b.mu.Unlock()
}

func (b *ptzBroker) next(ctx context.Context, siteID string) (domain.PTZCommand, bool) {
	for {
		b.mu.Lock()
		if queue := b.queues[siteID]; len(queue) > 0 {
			command := queue[0]
			b.queues[siteID] = queue[1:]
			b.mu.Unlock()
			return command, true
		}
		notice := b.noticeLocked(siteID)
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return domain.PTZCommand{}, false
		case <-notice:
		}
	}
}

func (b *ptzBroker) noticeLocked(siteID string) chan struct{} {
	if b.notices[siteID] == nil {
		b.notices[siteID] = make(chan struct{}, 1)
	}
	return b.notices[siteID]
}
