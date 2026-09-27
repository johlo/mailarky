package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
)

const (
	mailboxUsername = "clinic@example.test"
	mailboxPassword = "local-imap-only"
)

var errReadOnly = errors.New("test mailbox supports reading and appending only")

// Fixed folders and append-only messages keep test UIDs stable. The upstream
// memory backend needs locking when HTTP fixture writes overlap IMAP polling.
type mailbox struct {
	*memory.Mailbox
	mu       sync.Mutex
	validity uint32
}

func (m *mailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.Mailbox.Status(items)
	if err == nil {
		s.UidValidity = m.validity
	}
	return s, err
}

func (m *mailbox) ListMessages(uid bool, set *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Mailbox.ListMessages(uid, set, items, ch)
}

func (m *mailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Mailbox.SearchMessages(uid, criteria)
}

func (m *mailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Mailbox.CreateMessage(flags, date, body)
}

func (m *mailbox) SetSubscribed(bool) error { return errReadOnly }
func (m *mailbox) UpdateMessagesFlags(bool, *imap.SeqSet, imap.FlagsOp, []string) error {
	return errReadOnly
}
func (m *mailbox) CopyMessages(bool, *imap.SeqSet, string) error { return errReadOnly }
func (m *mailbox) Expunge() error                                { return errReadOnly }

type mailboxUser struct {
	boxes map[string]*mailbox
}

func (u *mailboxUser) Username() string { return mailboxUsername }
func (u *mailboxUser) Logout() error    { return nil }
func (u *mailboxUser) ListMailboxes(bool) ([]backend.Mailbox, error) {
	return []backend.Mailbox{u.boxes["INBOX"], u.boxes["Sent"], u.boxes["Archive"]}, nil
}
func (u *mailboxUser) GetMailbox(name string) (backend.Mailbox, error) {
	if box := u.boxes[name]; box != nil {
		return box, nil
	}
	return nil, backend.ErrNoSuchMailbox
}
func (u *mailboxUser) CreateMailbox(string) error         { return errReadOnly }
func (u *mailboxUser) DeleteMailbox(string) error         { return errReadOnly }
func (u *mailboxUser) RenameMailbox(string, string) error { return errReadOnly }

type mailboxBackend struct {
	user *mailboxUser
}

func newMailboxBackend() (*mailboxBackend, error) {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	// Restarting clears the in-memory mail, so advertise a new UIDVALIDITY.
	validity := binary.BigEndian.Uint32(random[:]) | 1
	seed := memory.New()
	u, err := seed.Login(nil, "username", "password")
	if err != nil {
		return nil, err
	}
	user := &mailboxUser{boxes: make(map[string]*mailbox)}
	for _, name := range []string{"INBOX", "Sent", "Archive"} {
		if name != "INBOX" {
			if err := u.CreateMailbox(name); err != nil {
				return nil, err
			}
		}
		box, err := u.GetMailbox(name)
		if err != nil {
			return nil, err
		}
		m := box.(*memory.Mailbox)
		m.Messages = nil // Remove the library's sample message.
		m.Subscribed = true
		user.boxes[name] = &mailbox{Mailbox: m, validity: validity}
	}
	return &mailboxBackend{user: user}, nil
}

func (b *mailboxBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	if username != mailboxUsername || password != mailboxPassword {
		return nil, errors.New("invalid test mailbox credentials")
	}
	return b.user, nil
}
