package sandbox

import (
	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
	"slices"
	"time"
)

type imapUpdateWriter interface {
	WriteExpunge(uint32) error
	WriteNumMessages(uint32) error
	WriteMessageFlags(uint32, imap.UID, []imap.Flag) error
}

// All view changes and wire updates run on the connection's command goroutine.
// External writers commit without waiting on a client or holding a socket lock.
func (m *mailbox) poll(w imapUpdateWriter, expunge bool) error {
	if !m.active {
		return nil
	}
	m.store.mu.RLock()
	select {
	case <-m.store.done:
		m.store.mu.RUnlock()
		return imapDisconnect(m.conn, "Account removed", false)
	default:
	}
	f, exists := m.store.state.Folders[m.name]
	current := orderedMessages(m.store.state, m.name)
	m.store.mu.RUnlock()
	if !exists || f.Validity != m.validity {
		return imapDisconnect(m.conn, "Selected folder changed; reconnect", false)
	}
	byID := make(map[string]*storedMessage, len(current))
	for _, msg := range current {
		byID[msg.ID] = msg
	}
	view := append([]*storedMessage{}, m.view...)
	if expunge {
		// Descending sequence numbers stay valid as earlier removals shift the view.
		for i := len(view) - 1; i >= 0; i-- {
			if byID[view[i].ID] == nil {
				if err := w.WriteExpunge(uint32(i + 1)); err != nil {
					return err
				}
				view = append(view[:i], view[i+1:]...)
			}
		}
	}
	known := make(map[string]bool, len(view))
	for _, msg := range view {
		known[msg.ID] = true
	}
	added := false
	for _, msg := range current {
		if !known[msg.ID] {
			view = append(view, msg)
			added = true
		}
	}
	if added {
		if err := w.WriteNumMessages(uint32(len(view))); err != nil {
			return err
		}
	}
	for i, old := range view {
		now := byID[old.ID]
		if now == nil {
			continue
		}
		report := !slices.Equal(old.Flags, now.Flags)
		if change, ok := m.silentChanges[old.ID]; ok {
			// Suppress only this STORE's changes. External updates observed
			// before the mutation or committed after it still need a reply.
			report = !slices.Equal(old.Flags, change.before.Flags) || !slices.Equal(change.after.Flags, now.Flags)
		}
		if report {
			if err := w.WriteMessageFlags(uint32(i+1), imap.UID(now.UID), imapFlags(now.Flags)); err != nil {
				return err
			}
		}
		view[i] = now
	}
	m.view = view
	m.silentChanges = nil
	return nil
}

func (s *imapSession) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if s.selected == nil {
		return nil
	}
	return s.selected.poll(w, allowExpunge)
}
func (s *imapSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	// The library joins this worker before processing DONE or another command.
	// Keep timers and writes here: an independent writer could outlive IDLE.
	e := imapEvent(s.conn, s.conn.Command(), "active")
	rules := s.idleClaims(e)
	var pending *disconnectAction
	var interval time.Duration
	for _, rule := range rules {
		a := rule.Action.Parameters.(*disconnectAction)
		var delay time.Duration
		if a.AfterMS != nil {
			delay = time.Duration(*a.AfterMS) * time.Millisecond
		}
		if pending == nil || delay < interval {
			pending, interval = a, delay
		}
	}
	var deadline <-chan time.Time
	if pending != nil {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		deadline = timer.C
	}
	updates := &idleFaultWriter{imapUpdateWriter: w, session: s}
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		changed := s.account.watch()
		if err := s.selected.poll(updates, true); err != nil {
			_ = s.conn.Close()
			return err
		}
		select {
		case <-stop:
			return nil
		case <-changed:
		case <-deadline:
			select {
			case <-stop:
				return nil
			default:
			}
			message := pending.Message
			if message == "" {
				message = "IDLE timeout"
			}
			return imapDisconnect(s.conn, message, pending.Silent)
		case <-s.account.done:
			return imapDisconnect(s.conn, "Account removed", false)
		}
	}
}

func (s *imapSession) idleClaims(e protocolEvent) []faultRule {
	rules := s.service.serverFaults.claim(e, false)
	return append(rules, s.account.faults.claim(e, false)...)
}

// Dropping an update deliberately leaves the client stale while the server's
// view advances. Reselecting the mailbox establishes a fresh, consistent view.
type idleFaultWriter struct {
	imapUpdateWriter
	session *imapSession
}

func (w *idleFaultWriter) drop(kind string) bool {
	e := imapEvent(w.session.conn, w.session.conn.Command(), "notification")
	e.notification = kind
	return len(w.session.idleClaims(e)) != 0
}
func (w *idleFaultWriter) WriteExpunge(n uint32) error {
	if w.drop("EXPUNGE") {
		return nil
	}
	return w.imapUpdateWriter.WriteExpunge(n)
}
func (w *idleFaultWriter) WriteNumMessages(n uint32) error {
	if w.drop("EXISTS") {
		return nil
	}
	return w.imapUpdateWriter.WriteNumMessages(n)
}
func (w *idleFaultWriter) WriteMessageFlags(n uint32, uid imap.UID, flags []imap.Flag) error {
	if w.drop("FLAGS") {
		return nil
	}
	return w.imapUpdateWriter.WriteMessageFlags(n, uid, flags)
}
