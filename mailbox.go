package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/google/uuid"
)

const (
	mailboxUsername = "clinic@example.test"
	mailboxPassword = "local-imap-only"
)

type mailbox struct {
	store    *mailboxBackend
	name     string
	validity uint32
}
type mailboxUser struct {
	store *mailboxBackend
	boxes map[string]*mailbox
}

func (b *mailboxBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	if subtle.ConstantTimeCompare([]byte(username), []byte(b.config.Username)) != 1 || subtle.ConstantTimeCompare([]byte(password), []byte(b.config.Password)) != 1 {
		return nil, backend.ErrInvalidCredentials
	}
	return b.user, nil
}
func (u *mailboxUser) Username() string { return u.store.config.Username }
func (u *mailboxUser) Logout() error    { return nil }
func (u *mailboxUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	u.store.mu.RLock()
	defer u.store.mu.RUnlock()
	names := []string{}
	for name, f := range u.store.state.Folders {
		if !subscribed || f.Subscribed {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]backend.Mailbox, 0, len(names))
	for _, name := range names {
		out = append(out, &mailbox{store: u.store, name: name, validity: u.store.state.Folders[name].Validity})
	}
	return out, nil
}
func (u *mailboxUser) GetMailbox(name string) (backend.Mailbox, error) {
	if strings.EqualFold(name, "INBOX") {
		name = "INBOX"
	}
	u.store.mu.RLock()
	defer u.store.mu.RUnlock()
	f, ok := u.store.state.Folders[name]
	if !ok {
		return nil, backend.ErrNoSuchMailbox
	}
	return &mailbox{store: u.store, name: name, validity: f.Validity}, nil
}
func validFolder(name string) bool {
	return name != "" && len(name) <= 255 && !strings.ContainsAny(name, "\r\n\x00") && !strings.HasPrefix(name, "/") && !strings.HasSuffix(name, "/")
}
func (u *mailboxUser) CreateMailbox(name string) error {
	if !validFolder(name) {
		return errors.New("invalid folder name")
	}
	return u.store.mutate(func(s *storeState) error {
		if _, ok := s.Folders[name]; ok {
			return errors.New("mailbox already exists")
		}
		s.Folders[name] = folderState{Name: name, NextUID: 1, Validity: newValidity()}
		return nil
	})
}
func (u *mailboxUser) DeleteMailbox(name string) error {
	if strings.EqualFold(name, "INBOX") {
		return errors.New("cannot delete INBOX")
	}
	return u.store.mutate(func(s *storeState) error {
		if _, ok := s.Folders[name]; !ok {
			return backend.ErrNoSuchMailbox
		}
		delete(s.Folders, name)
		for id, m := range s.Messages {
			if m.Folder == name {
				delete(s.Messages, id)
			}
		}
		return nil
	})
}
func (u *mailboxUser) RenameMailbox(from, to string) error {
	if !validFolder(to) || strings.EqualFold(to, "INBOX") {
		return errors.New("invalid destination folder")
	}
	return u.store.mutate(func(s *storeState) error {
		if _, ok := s.Folders[from]; !ok {
			return backend.ErrNoSuchMailbox
		}
		if _, ok := s.Folders[to]; ok {
			return errors.New("destination already exists")
		}
		names := make([]string, 0, len(s.Folders))
		for name := range s.Folders {
			names = append(names, name)
		}
		for _, name := range names {
			f := s.Folders[name]
			if name == from || strings.HasPrefix(name, from+"/") {
				dest := to + strings.TrimPrefix(name, from)
				if _, ok := s.Folders[dest]; ok {
					return errors.New("destination child already exists")
				}
				f.Name = dest
				s.Folders[dest] = f
				delete(s.Folders, name)
			}
		}
		for id, m := range s.Messages {
			if m.Folder == from || strings.HasPrefix(m.Folder, from+"/") {
				copy := cloneRecord(m)
				copy.Folder = to + strings.TrimPrefix(m.Folder, from)
				s.Messages[id] = copy
			}
		}
		if from == "INBOX" {
			s.Folders[from] = folderState{Name: from, NextUID: 1, Subscribed: true, Validity: newValidity()}
		}
		return nil
	})
}

func (m *mailbox) Name() string { return m.name }
func (m *mailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Name: m.name, Delimiter: "/"}, nil
}
func (m *mailbox) Check() error { return nil }
func (m *mailbox) SetSubscribed(subscribed bool) error {
	return m.store.mutate(func(s *storeState) error {
		f, ok := s.Folders[m.name]
		if !ok {
			return backend.ErrNoSuchMailbox
		}
		f.Subscribed = subscribed
		s.Folders[m.name] = f
		return nil
	})
}
func (m *mailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	f, ok := m.store.state.Folders[m.name]
	if !ok {
		return nil, backend.ErrNoSuchMailbox
	}
	status := imap.NewMailboxStatus(m.name, items)
	status.UidValidity = f.Validity
	status.UidNext = f.NextUID
	status.PermanentFlags = []string{imap.SeenFlag, imap.AnsweredFlag, imap.FlaggedFlag, imap.DeletedFlag, imap.DraftFlag, "\\*"}
	status.Flags = append([]string{}, status.PermanentFlags...)
	list := orderedMessages(m.store.state, m.name)
	status.Messages = uint32(len(list))
	for i, msg := range list {
		if !hasFlag(msg.Flags, imap.SeenFlag) {
			status.Unseen++
			if status.UnseenSeqNum == 0 {
				status.UnseenSeqNum = uint32(i + 1)
			}
		}
	}
	return status, nil
}

func (m *mailbox) records() ([]*storedMessage, error) {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	if _, ok := m.store.state.Folders[m.name]; !ok {
		return nil, backend.ErrNoSuchMailbox
	}
	list := orderedMessages(m.store.state, m.name)
	for i, msg := range list {
		list[i] = cloneRecord(msg)
	}
	return list, nil
}
func selected(set *imap.SeqSet, id, max uint32) bool {
	for _, seq := range set.Set {
		start, stop := seq.Start, seq.Stop
		if start == 0 {
			start = max
		}
		if stop == 0 {
			stop = max
		}
		if start > stop {
			start, stop = stop, start
		}
		if id >= start && id <= stop {
			return true
		}
	}
	return false
}

func (m *mailbox) affected(record *storedMessage) (*memory.Message, bool, error) {
	msg := record.memory()
	for _, t := range m.store.toxics.claim("imap", "", record) {
		switch t.Type {
		case "imap_hide":
			return msg, true, nil
		case "imap_delay":
			if err := waitToxic(context.Background(), m.store.done, t.Attributes.DelayMS); err != nil {
				return nil, false, err
			}
		case "imap_replace_header":
			msg.Body = replaceHeaders(msg.Body, map[string]string{t.Attributes.Header: t.Attributes.Value})
			msg.Size = uint32(len(msg.Body))
		}
	}
	return msg, false, nil
}

func (m *mailbox) ListMessages(uid bool, set *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)
	records, err := m.records()
	if err != nil {
		return err
	}
	max := uint32(len(records))
	if uid && len(records) > 0 {
		max = records[len(records)-1].UID
	}
	for i, record := range records {
		seq := uint32(i + 1)
		id := seq
		if uid {
			id = record.UID
		}
		if !selected(set, id, max) {
			continue
		}
		msg, hidden, err := m.affected(record)
		if err != nil {
			return err
		}
		if hidden {
			continue
		}
		fetched, err := msg.Fetch(seq, items)
		if err != nil {
			return err
		}
		ch <- fetched
	}
	return nil
}

func (m *mailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	records, err := m.records()
	if err != nil {
		return nil, err
	}
	ids := []uint32{}
	for i, record := range records {
		seq := uint32(i + 1)
		// Only candidates may consume a search toxic or cause a delay.
		// Searches for another test's messages must remain independent.
		matches, err := record.memory().Match(seq, criteria)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		msg, hidden, err := m.affected(record)
		if err != nil {
			return nil, err
		}
		if hidden {
			continue
		}
		matches, err = msg.Match(seq, criteria)
		if err != nil {
			return nil, err
		}
		if matches {
			if uid {
				ids = append(ids, record.UID)
			} else {
				ids = append(ids, seq)
			}
		}
	}
	return ids, nil
}

func (m *mailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(io.LimitReader(body, m.store.config.MaxSize+1))
	if err != nil {
		return err
	}
	_, err = m.store.append(raw, appendOptions{Folder: m.name, Date: date, Flags: flags, Notify: true})
	return err
}
func (m *mailbox) UpdateMessagesFlags(uid bool, set *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	return m.store.mutate(func(s *storeState) error {
		list := orderedMessages(*s, m.name)
		max := uint32(len(list))
		if uid && len(list) > 0 {
			max = list[len(list)-1].UID
		}
		for i, record := range list {
			id := uint32(i + 1)
			if uid {
				id = record.UID
			}
			if selected(set, id, max) {
				copy := cloneRecord(record)
				copy.Flags = backendutil.UpdateFlags(copy.Flags, op, flags)
				s.Messages[record.ID] = copy
			}
		}
		return nil
	})
}
func (m *mailbox) CopyMessages(uid bool, set *imap.SeqSet, dest string) error {
	return m.store.mutate(func(s *storeState) error {
		folder, ok := s.Folders[dest]
		if !ok {
			return backend.ErrNoSuchMailbox
		}
		list := orderedMessages(*s, m.name)
		max := uint32(len(list))
		if uid && len(list) > 0 {
			max = list[len(list)-1].UID
		}
		for i, record := range list {
			id := uint32(i + 1)
			if uid {
				id = record.UID
			}
			if !selected(set, id, max) {
				continue
			}
			if folder.NextUID == ^uint32(0) {
				return errors.New("UID space exhausted")
			}
			copy := cloneRecord(record)
			copy.ID = uuid.NewString()
			copy.Folder = dest
			copy.UID = folder.NextUID
			folder.NextUID++
			s.Sequence++
			copy.Sequence = s.Sequence
			copy.Created = time.Now().UTC()
			s.Messages[copy.ID] = copy
		}
		s.Folders[dest] = folder
		m.store.pruneState(s, time.Now())
		return nil
	})
}
func (m *mailbox) Expunge() error {
	return m.store.mutate(func(s *storeState) error {
		for id, record := range s.Messages {
			if record.Folder == m.name && hasFlag(record.Flags, imap.DeletedFlag) {
				delete(s.Messages, id)
			}
		}
		return nil
	})
}

// Replace only complete header fields; body bytes and unrelated MIME headers
// stay untouched. Used by explicitly configured relay policies and toxics.
func replaceHeaders(raw []byte, replacements map[string]string) []byte {
	separator := bytes.Index(raw, []byte("\r\n\r\n"))
	if separator < 0 {
		return raw
	}
	lines := strings.Split(string(raw[:separator]), "\r\n")
	var output []string
	skip := false
	for _, line := range lines {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if !skip {
				output = append(output, line)
			}
			continue
		}
		key, _, _ := strings.Cut(line, ":")
		skip = false
		for k := range replacements {
			if strings.EqualFold(key, k) {
				skip = true
			}
		}
		if !skip {
			output = append(output, line)
		}
	}
	keys := make([]string, 0, len(replacements))
	for k := range replacements {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if replacements[k] != "" {
			output = append(output, fmt.Sprintf("%s: %s", k, replacements[k]))
		}
	}
	return append([]byte(strings.Join(output, "\r\n")+"\r\n\r\n"), raw[separator+4:]...)
}
