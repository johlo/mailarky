package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

const (
	mailboxUsername = "user@example.test"
	mailboxPassword = "local-imap-only"
)

type mailbox struct {
	store            *Account
	name             string
	validity         uint32
	view             []*storedMessage
	active, readOnly bool
	conn             *imapserver.Conn
	silentChanges    map[string]flagChange
}

var errNoSuchMailbox = errors.New("no such mailbox")

type flagChange struct{ before, after *storedMessage }
type mailboxUser struct {
	store *Account
}

func (u *mailboxUser) ListMailboxes(subscribed bool) ([]*mailbox, error) {
	u.store.mu.RLock()
	defer u.store.mu.RUnlock()
	select {
	case <-u.store.done:
		return nil, errors.New("mailbox is closed")
	default:
	}
	names := []string{}
	for name, f := range u.store.state.Folders {
		if !subscribed || f.Subscribed {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]*mailbox, 0, len(names))
	for _, name := range names {
		out = append(out, &mailbox{store: u.store, name: name, validity: u.store.state.Folders[name].Validity})
	}
	return out, nil
}
func (u *mailboxUser) GetMailbox(name string) (*mailbox, error) {
	if strings.EqualFold(name, "INBOX") {
		name = "INBOX"
	}
	u.store.mu.RLock()
	defer u.store.mu.RUnlock()
	select {
	case <-u.store.done:
		return nil, errors.New("mailbox is closed")
	default:
	}
	f, ok := u.store.state.Folders[name]
	if !ok {
		return nil, errNoSuchMailbox
	}
	return &mailbox{store: u.store, name: name, validity: f.Validity}, nil
}
func validFolder(name string) bool {
	return name != "" && len(name) <= 255 && !strings.ContainsAny(name, "\r\n\x00") && !strings.HasPrefix(name, "/") && !strings.HasSuffix(name, "/")
}
func (u *mailboxUser) CreateMailbox(name string) error {
	if strings.EqualFold(name, "INBOX") {
		name = "INBOX"
	}
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
			return errNoSuchMailbox
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
			return errNoSuchMailbox
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
func (m *mailbox) SetSubscribed(subscribed bool) error {
	return m.store.mutate(func(s *storeState) error {
		f, ok := s.Folders[m.name]
		if !ok {
			return errNoSuchMailbox
		}
		f.Subscribed = subscribed
		s.Folders[m.name] = f
		return nil
	})
}
func (m *mailbox) Status() (*imap.SelectData, error) {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	select {
	case <-m.store.done:
		return nil, errors.New("mailbox is closed")
	default:
	}
	f, ok := m.store.state.Folders[m.name]
	if !ok {
		return nil, errNoSuchMailbox
	}
	status := new(imap.SelectData)
	status.UIDValidity = f.Validity
	status.UIDNext = imap.UID(f.NextUID)
	status.Flags = []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft}
	status.PermanentFlags = append(append([]imap.Flag{}, status.Flags...), "\\*")
	list := orderedMessages(m.store.state, m.name)
	if !m.active {
		m.view = append([]*storedMessage{}, list...)
	}
	status.NumMessages = uint32(len(list))
	for i, msg := range list {
		if !hasFlag(msg.Flags, string(imap.FlagSeen)) {
			if status.FirstUnseenSeqNum == 0 {
				status.FirstUnseenSeqNum = uint32(i + 1)
			}
		}
	}
	return status, nil
}

func (m *mailbox) records() ([]*storedMessage, error) {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	select {
	case <-m.store.done:
		return nil, errors.New("mailbox is closed")
	default:
	}
	if _, ok := m.store.state.Folders[m.name]; !ok {
		return nil, errNoSuchMailbox
	}
	list := orderedMessages(m.store.state, m.name)
	if m.active {
		list = append([]*storedMessage{}, m.view...)
	}
	for i, msg := range list {
		list[i] = cloneRecord(msg)
	}
	return list, nil
}
func selectedRange(start, stop, id, max uint32) bool {
	if start == 0 {
		start = max
	}
	if stop == 0 {
		stop = max
	}
	if start > stop {
		start, stop = stop, start
	}
	return id != 0 && id >= start && id <= stop
}

func selected(set imap.NumSet, seq uint32, record *storedMessage, records []*storedMessage) bool {
	maxSeq := uint32(len(records))
	var maxUID uint32
	if len(records) > 0 {
		maxUID = records[len(records)-1].UID
	}
	switch set := set.(type) {
	case imap.SeqSet:
		for _, r := range set {
			if selectedRange(r.Start, r.Stop, seq, maxSeq) {
				return true
			}
		}
	case imap.UIDSet:
		for _, r := range set {
			if selectedRange(uint32(r.Start), uint32(r.Stop), record.UID, maxUID) {
				return true
			}
		}
	}
	return false
}

func (m *mailbox) UpdateMessagesFlags(set imap.NumSet, flags *imap.StoreFlags) error {
	records, err := m.records()
	if err != nil {
		return err
	}
	changes := make(map[string]flagChange)
	err = m.store.mutate(func(s *storeState) error {
		for i, record := range records {
			if selected(set, uint32(i+1), record, records) {
				current := s.Messages[record.ID]
				if current == nil {
					continue
				}
				copy := cloneRecord(current)
				copy.Flags = updateFlags(copy.Flags, flags)
				s.Messages[record.ID] = copy
				if flags.Silent {
					changes[record.ID] = flagChange{before: current, after: copy}
				}
			}
		}
		return nil
	})
	if err == nil {
		m.silentChanges = changes
	}
	return err
}
func (m *mailbox) CopyMessages(set imap.NumSet, dest string) error {
	records, err := m.records()
	if err != nil {
		return err
	}
	return m.store.mutate(func(s *storeState) error {
		folder, ok := s.Folders[dest]
		if !ok {
			return errNoSuchMailbox
		}
		var copies []*storedMessage
		var bytes int64
		for i, record := range records {
			if !selected(set, uint32(i+1), record, records) {
				continue
			}
			current := s.Messages[record.ID]
			if current == nil {
				continue
			}
			copies = append(copies, current)
			bytes += int64(len(current.Raw))
		}
		if len(copies) > 0 {
			if err := m.store.checkQuota(*s, len(copies), bytes); err != nil {
				return err
			}
		}
		for _, current := range copies {
			if folder.NextUID == ^uint32(0) {
				return errors.New("UID space exhausted")
			}
			copy := cloneRecord(current)
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
func (m *mailbox) Expunge(uids *imap.UIDSet) error {
	return m.store.mutate(func(s *storeState) error {
		for id, record := range s.Messages {
			if record.Folder == m.name && hasFlag(record.Flags, string(imap.FlagDeleted)) && (uids == nil || uids.Contains(imap.UID(record.UID))) {
				delete(s.Messages, id)
			}
		}
		return nil
	})
}

// Replace only complete header fields; body bytes and unrelated MIME headers
// stay untouched. Used by IMAP header faults and sendmail Bcc removal.
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
