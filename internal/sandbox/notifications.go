package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

func (a *accountAPI) events(w http.ResponseWriter, r *http.Request) {
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	ctx := connection.CloseRead(r.Context())
	events, unsubscribe := a.account.notifier.subscribe()
	defer unsubscribe()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = connection.Write(timeout, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		case <-a.account.done:
			return
		}
	}
}

// Webhooks use one worker and a bounded queue. Retries are confined to HTTP
// delivery and never hold mailbox or fault locks or delay SMTP acknowledgement.
func (b *Account) startWorkers(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	if b.service.config.MaxAge > 0 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := b.prune(); err != nil {
						log.Printf("retention: %v", err)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	if b.service.config.WebhookURL != "" {
		queue := make(chan messageSummary, 1024)
		b.afterAppend = func(m *storedMessage) {
			select {
			case queue <- m.summary():
			default:
				log.Printf("webhook queue full; skipped notification for %s", m.ID)
			}
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			var last time.Time
			for {
				select {
				case summary := <-queue:
					wait := b.service.config.WebhookDelay
					if until := time.Until(last.Add(b.service.config.WebhookLimit)); until > wait {
						wait = until
					}
					if wait > 0 {
						if err := waitDuration(ctx, wait); err != nil {
							return
						}
					}
					data, _ := json.Marshal(summary)
					for attempt := 0; attempt < 3; attempt++ {
						req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.service.config.WebhookURL, bytes.NewReader(data))
						if err != nil {
							log.Printf("webhook request: %v", err)
							break
						}
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Mailarky-Mailbox", b.identity.ID)
						if b.service.config.Label != "" {
							req.Header.Set("Mailarky-Label", b.service.config.Label)
						}
						response, err := client.Do(req)
						last = time.Now()
						if err == nil {
							io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
							response.Body.Close()
							if response.StatusCode >= 200 && response.StatusCode < 300 {
								break
							}
							err = fmt.Errorf("HTTP %d", response.StatusCode)
						}
						if attempt == 2 {
							log.Printf("webhook failed for %s: %v", summary.ID, err)
							break
						}
						if err := waitDuration(ctx, time.Duration(attempt+1)*time.Second); err != nil {
							return
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	return func() { cancel(); workers.Wait() }
}

func waitDuration(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *httpAPI) info(w http.ResponseWriter, r *http.Request) {
	a.service.mu.RLock()
	count := len(a.service.entries)
	a.service.mu.RUnlock()
	jsonResponse(w, 200, map[string]any{"service": "mailarky", "accounts": count, "protocols": []string{"smtp", "imap", "http"}})
}
func (a *httpAPI) metrics(w http.ResponseWriter, r *http.Request) {
	if !a.service.config.EnableMetrics {
		apiError(w, 404, fmt.Errorf("metrics are disabled"))
		return
	}
	a.service.mu.RLock()
	accounts := make([]*Account, 0, len(a.service.entries))
	for _, account := range a.service.entries {
		accounts = append(accounts, account)
	}
	a.service.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, account := range accounts {
		values := map[string]uint64{"messages": uint64(len(account.snapshot())), "smtp_accepted_total": account.accepted.Load(), "smtp_rejected_total": account.rejected.Load(), "smtp_accepted_bytes_total": account.acceptedSize.Load(), "messages_deleted_total": account.deleted.Load(), "messages_ignored_total": account.ignored.Load()}
		for name, value := range values {
			fmt.Fprintf(w, "mailarky_%s{account=%q} %d\n", name, account.identity.ID, value)
		}
	}
}

type notification struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}
type eventHub struct {
	mu          sync.Mutex
	subscribers map[chan notification]struct{}
	closed      bool
}

func newEventHub() *eventHub { return &eventHub{subscribers: map[chan notification]struct{}{}} }
func (h *eventHub) publish(event notification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(h.subscribers, ch)
		}
	}
}
func (h *eventHub) subscribe() (chan notification, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan notification, 64)
	if h.closed {
		close(ch)
	} else {
		h.subscribers[ch] = struct{}{}
	}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subscribers[ch]; ok {
			delete(h.subscribers, ch)
			close(ch)
		}
	}
}
func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subscribers {
		close(ch)
	}
	h.subscribers = map[chan notification]struct{}{}
}
