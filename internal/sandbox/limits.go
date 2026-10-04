package sandbox

import (
	"errors"
	"time"
)

// Limits reject excess work. Retention settings, separately, prune old mail.
type accountLimits struct {
	SMTPConnections int    `json:"smtp_connections" yaml:"smtp_connections"`
	IMAPConnections int    `json:"imap_connections" yaml:"imap_connections"`
	SendMessages    int    `json:"send_messages" yaml:"send_messages"`
	SendWindow      string `json:"send_window,omitempty" yaml:"send_window"`
	QuotaMessages   int    `json:"quota_messages" yaml:"quota_messages"`
	QuotaBytes      int64  `json:"quota_bytes" yaml:"quota_bytes"`
}

var (
	errQuota           = errors.New("account storage quota exceeded")
	errAccountClosed   = errors.New("account is closed")
	errConnectionLimit = errors.New("account connection limit reached")
)

func (l accountLimits) validate() error {
	if l.SMTPConnections < 0 || l.IMAPConnections < 0 || l.SendMessages < 0 || l.QuotaMessages < 0 || l.QuotaBytes < 0 {
		return errors.New("account limits must be nonnegative")
	}
	if l.SendWindow != "" {
		if d, err := time.ParseDuration(l.SendWindow); err != nil || d <= 0 {
			return errors.New("send_window must be a positive duration, e.g. 1m")
		}
	} else if l.SendMessages > 0 {
		return errors.New("send_window is required with send_messages")
	}
	return nil
}

func (a *Account) acquireConnection(protocol string) error {
	a.resourceMu.Lock()
	defer a.resourceMu.Unlock()
	select {
	case <-a.done:
		return errAccountClosed
	default:
	}
	n, limit := &a.imapConnections, a.identity.Limits.IMAPConnections
	if protocol == "smtp" {
		n, limit = &a.smtpConnections, a.identity.Limits.SMTPConnections
	}
	if limit > 0 && *n >= limit {
		return errConnectionLimit
	}
	(*n)++
	return nil
}
func (a *Account) releaseConnection(protocol string) {
	a.resourceMu.Lock()
	defer a.resourceMu.Unlock()
	if protocol == "smtp" {
		a.smtpConnections--
	} else {
		a.imapConnections--
	}
}

// Reserve before delivery so concurrent submissions cannot exceed the window.
// Roll back uncommitted deliveries, without touching a later window's counter.
func (a *Account) reserveSend(now time.Time) (rollback func(), ok bool) {
	a.resourceMu.Lock()
	defer a.resourceMu.Unlock()
	l := a.identity.Limits
	if l.SendMessages == 0 {
		return func() {}, true
	}
	window, _ := time.ParseDuration(l.SendWindow)
	if a.sendWindow.IsZero() || !now.Before(a.sendWindow.Add(window)) {
		a.sendWindow, a.sentInWindow = now, 0
	}
	if a.sentInWindow >= l.SendMessages {
		return nil, false
	}
	a.sentInWindow++
	start := a.sendWindow
	return func() {
		a.resourceMu.Lock()
		defer a.resourceMu.Unlock()
		if a.sendWindow == start {
			a.sentInWindow--
		}
	}, true
}

// Called under a store lock, before insertion or retention. A full quota must
// never evict existing fixtures to make room for the new message.
func (b *Store) checkQuota(state storeState, messages int, bytes int64) error {
	l := b.options.Limits
	if l.QuotaMessages > 0 && len(state.Messages)+messages > l.QuotaMessages {
		return errQuota
	}
	if l.QuotaBytes > 0 {
		for _, m := range state.Messages {
			bytes += int64(len(m.Raw))
		}
		if bytes > l.QuotaBytes {
			return errQuota
		}
	}
	return nil
}
func (b *Store) preflightAppend(size int, messageID string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.options.IgnoreDuplicates && messageID != "" {
		for _, m := range b.state.Messages {
			if m.Detail.MessageID == messageID {
				return nil
			}
		}
	}
	return b.checkQuota(b.state, 1, int64(size))
}
