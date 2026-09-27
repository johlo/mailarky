package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"
	bolt "go.etcd.io/bbolt"
)

func (a *httpAPI) events(w http.ResponseWriter, r *http.Request) {
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	ctx := connection.CloseRead(r.Context())
	events, unsubscribe := a.store.subscribe()
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
		case <-a.store.done:
			return
		}
	}
}

// Webhooks use one worker and a bounded queue. Retries are confined to HTTP
// delivery and never hold mailbox or toxic locks or delay SMTP acknowledgement.
func (b *mailboxBackend) startWorkers(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	if b.config.MaxAge > 0 {
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
	if b.config.WebhookURL != "" {
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
					wait := b.config.WebhookDelay
					if until := time.Until(last.Add(b.config.WebhookLimit)); until > wait {
						wait = until
					}
					if wait > 0 {
						if err := waitDuration(ctx, wait); err != nil {
							return
						}
					}
					data, _ := json.Marshal(summary)
					for attempt := 0; attempt < 3; attempt++ {
						req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.config.WebhookURL, bytes.NewReader(data))
						if err != nil {
							log.Printf("webhook request: %v", err)
							break
						}
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Mail-Sandbox-Mailbox", b.config.MailboxID)
						if b.config.Label != "" {
							req.Header.Set("Mail-Sandbox-Label", b.config.Label)
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
	list := a.store.snapshot()
	unread := 0
	tags := map[string]int{}
	for _, m := range list {
		if !hasFlag(m.Flags, "\\Seen") {
			unread++
		}
		for _, tag := range m.Tags {
			tags[tag]++
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	database := "memory"
	size := int64(0)
	if a.store.db != nil {
		database = a.store.config.Database
		a.store.db.View(func(tx *bolt.Tx) error { size = tx.Size(); return nil })
	}
	jsonResponse(w, 200, map[string]any{"MailboxID": a.store.config.MailboxID, "Version": "mail-sandbox/2", "LatestVersion": "", "Database": database, "DatabaseSize": size, "Messages": len(list), "Unread": unread, "Tags": tags, "RuntimeStats": map[string]any{"Memory": mem.Alloc, "SMTPAccepted": a.store.accepted.Load(), "SMTPAcceptedSize": a.store.acceptedSize.Load(), "SMTPRejected": a.store.rejected.Load(), "SMTPIgnored": a.store.ignored.Load(), "MessagesDeleted": a.store.deleted.Load(), "Uptime": int64(time.Since(a.store.started).Seconds())}})
}
func (a *httpAPI) capabilities(w http.ResponseWriter, r *http.Request) {
	c := a.store.config
	jsonResponse(w, 200, map[string]any{"Label": c.Label, "ChaosEnabled": false, "MessageScopedToxics": true, "MailboxManagement": a.manager != nil, "WebUI": false, "DuplicatesIgnored": c.IgnoreDuplicates, "SpamAssassin": c.SpamAssassin != ""})
}
func (a *httpAPI) metrics(w http.ResponseWriter, r *http.Request) {
	if !a.store.config.EnableMetrics {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	values := map[string]uint64{"messages": uint64(len(a.store.snapshot())), "smtp_accepted_total": a.store.accepted.Load(), "smtp_rejected_total": a.store.rejected.Load(), "smtp_accepted_bytes_total": a.store.acceptedSize.Load(), "messages_deleted_total": a.store.deleted.Load(), "messages_ignored_total": a.store.ignored.Load()}
	for name, value := range values {
		fmt.Fprintf(w, "mail_sandbox_%s %d\n", name, value)
	}
}
