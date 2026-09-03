package cloud

import (
	"context"
	"sync"
	"time"

	"cloudnvr/internal/domain"
)

const webRTCRequestTTL = 30 * time.Second

type webRTCExchange struct {
	request domain.WebRTCRequest
	siteID  string
	created time.Time
	result  chan domain.WebRTCResponse
}

// webRTCBroker is a small authenticated reverse tunnel for WHEP signaling.
// Agents only make outbound requests; video packets never pass through it.
type webRTCBroker struct {
	mu      sync.Mutex
	queues  map[string][]*webRTCExchange
	pending map[string]*webRTCExchange
	notices map[string]chan struct{}
}

func newWebRTCBroker() *webRTCBroker {
	return &webRTCBroker{
		queues: make(map[string][]*webRTCExchange), pending: make(map[string]*webRTCExchange), notices: make(map[string]chan struct{}),
	}
}

func (b *webRTCBroker) exchange(ctx context.Context, siteID string, request domain.WebRTCRequest) (domain.WebRTCResponse, bool) {
	exchange := &webRTCExchange{request: request, siteID: siteID, created: time.Now(), result: make(chan domain.WebRTCResponse, 1)}
	b.mu.Lock()
	b.cleanupLocked()
	b.pending[request.ID] = exchange
	b.queues[siteID] = append(b.queues[siteID], exchange)
	notice := b.noticeLocked(siteID)
	select {
	case notice <- struct{}{}:
	default:
	}
	b.mu.Unlock()

	select {
	case response := <-exchange.result:
		return response, true
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, request.ID)
		b.mu.Unlock()
		return domain.WebRTCResponse{}, false
	}
}

func (b *webRTCBroker) next(ctx context.Context, siteID string) (domain.WebRTCRequest, bool) {
	for {
		b.mu.Lock()
		b.cleanupLocked()
		queue := b.queues[siteID]
		for len(queue) > 0 {
			exchange := queue[0]
			queue = queue[1:]
			b.queues[siteID] = queue
			if b.pending[exchange.request.ID] == exchange {
				request := exchange.request
				b.mu.Unlock()
				return request, true
			}
		}
		notice := b.noticeLocked(siteID)
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return domain.WebRTCRequest{}, false
		case <-notice:
		}
	}
}

func (b *webRTCBroker) resolve(siteID string, response domain.WebRTCResponse) bool {
	b.mu.Lock()
	exchange := b.pending[response.ID]
	if exchange == nil || exchange.siteID != siteID {
		b.mu.Unlock()
		return false
	}
	delete(b.pending, response.ID)
	b.mu.Unlock()
	select {
	case exchange.result <- response:
		return true
	default:
		return false
	}
}

func (b *webRTCBroker) cleanupLocked() {
	cutoff := time.Now().Add(-webRTCRequestTTL)
	for id, exchange := range b.pending {
		if exchange.created.Before(cutoff) {
			delete(b.pending, id)
		}
	}
}

func (b *webRTCBroker) noticeLocked(siteID string) chan struct{} {
	if b.notices[siteID] == nil {
		b.notices[siteID] = make(chan struct{}, 1)
	}
	return b.notices[siteID]
}
