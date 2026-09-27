package emulator

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/backend"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type folderState struct {
	Name       string
	NextUID    uint32
	Subscribed bool
	Validity   uint32
}
type storeState struct {
	Folders  map[string]folderState
	Messages map[string]*storedMessage
	Sequence uint64
}
type notification struct {
	Type string
	Data any
}

type mailboxBackend struct {
	mu                                                 sync.RWMutex
	state                                              storeState
	config                                             configuration
	user                                               *mailboxUser
	db                                                 *bolt.DB
	done                                               chan struct{}
	closed                                             sync.Once
	subscribers                                        map[chan notification]struct{}
	afterAppend                                        func(*storedMessage)
	toxics                                             *toxicRegistry
	started                                            time.Time
	accepted, rejected, ignored, deleted, acceptedSize atomic.Uint64
}

func newValidity() uint32 {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return binary.BigEndian.Uint32(value[:]) | 1
}

func newMailboxBackend() (*mailboxBackend, error) {
	c := defaultConfig()
	c.MaxMessages = 0
	return openMailbox(c)
}

func openMailbox(c configuration) (*mailboxBackend, error) {
	if !validFolder(c.SMTPFolder) {
		return nil, errors.New("invalid SMTP folder name")
	}
	b := &mailboxBackend{config: c, state: storeState{Folders: map[string]folderState{}, Messages: map[string]*storedMessage{}}, done: make(chan struct{}), subscribers: map[chan notification]struct{}{}, started: time.Now()}
	b.user = &mailboxUser{store: b, boxes: map[string]*mailbox{}}
	b.toxics = newToxicRegistry()
	if c.Database != "" {
		if err := os.MkdirAll(filepath.Dir(c.Database), 0700); err != nil {
			return nil, err
		}
		db, err := bolt.Open(c.Database, 0600, &bolt.Options{Timeout: time.Second})
		if err != nil {
			return nil, err
		}
		b.db = db
		if err := db.Update(func(tx *bolt.Tx) error {
			for _, name := range []string{"messages", "metadata"} {
				if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			db.Close()
			return nil, err
		}
		err = db.View(func(tx *bolt.Tx) error {
			if data := tx.Bucket([]byte("metadata")).Get([]byte("state")); data != nil {
				var meta struct {
					Folders  map[string]folderState
					Sequence uint64
				}
				if err := json.Unmarshal(data, &meta); err != nil {
					return err
				}
				b.state.Folders, b.state.Sequence = meta.Folders, meta.Sequence
			}
			return tx.Bucket([]byte("messages")).ForEach(func(_, value []byte) error {
				var m storedMessage
				if err := json.Unmarshal(value, &m); err != nil {
					return err
				}
				d, h, err := parseMessage(m.Raw, m.Created, m.EnvelopeFrom, m.EnvelopeTo)
				if err != nil {
					return err
				}
				m.Detail, m.Headers = d, h
				if m.MailboxID == "" {
					m.MailboxID = c.MailboxID
				}
				b.state.Messages[m.ID] = &m
				return nil
			})
		})
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	for _, name := range []string{"INBOX", "Sent", "Archive", c.SMTPFolder} {
		if _, ok := b.state.Folders[name]; !ok {
			b.state.Folders[name] = folderState{Name: name, NextUID: 1, Subscribed: true, Validity: newValidity()}
		}
	}
	for name, f := range b.state.Folders {
		b.user.boxes[name] = &mailbox{store: b, name: name, validity: f.Validity}
	}
	if _, ok := b.state.Folders[c.SMTPFolder]; !ok {
		if b.db != nil {
			b.db.Close()
		}
		return nil, fmt.Errorf("unknown SMTP folder %q", c.SMTPFolder)
	}
	// Persist folder identity even when the mailbox is still empty.
	if b.db != nil {
		if err := b.mutate(func(*storeState) error { return nil }); err != nil {
			b.db.Close()
			return nil, err
		}
	}
	return b, nil
}

func (b *mailboxBackend) Close() error {
	var err error
	b.closed.Do(func() {
		close(b.done)
		b.mu.Lock()
		defer b.mu.Unlock()
		for ch := range b.subscribers {
			close(ch)
		}
		b.subscribers = map[chan notification]struct{}{}
		if b.db != nil {
			err = b.db.Close()
		}
	})
	return err
}
func orderedMessages(state storeState, folder string) []*storedMessage {
	list := make([]*storedMessage, 0, len(state.Messages))
	for _, m := range state.Messages {
		if folder == "" || m.Folder == folder {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Sequence < list[j].Sequence })
	return list
}
func cloneRecord(m *storedMessage) *storedMessage {
	copy := *m
	copy.Flags = append([]string{}, m.Flags...)
	copy.Tags = append([]string{}, m.Tags...)
	return &copy
}

func (b *mailboxBackend) mutate(change func(*storeState) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done:
		return errors.New("mail store is closed")
	default:
	}
	before := b.state
	next := storeState{Folders: make(map[string]folderState, len(before.Folders)), Messages: make(map[string]*storedMessage, len(before.Messages)), Sequence: before.Sequence}
	for name, f := range before.Folders {
		next.Folders[name] = f
	}
	for id, m := range before.Messages {
		next.Messages[id] = m
	}
	if err := change(&next); err != nil {
		return err
	}
	if b.db != nil {
		if err := b.db.Update(func(tx *bolt.Tx) error {
			meta, err := json.Marshal(struct {
				Folders  map[string]folderState
				Sequence uint64
			}{next.Folders, next.Sequence})
			if err != nil {
				return err
			}
			if err := tx.Bucket([]byte("metadata")).Put([]byte("state"), meta); err != nil {
				return err
			}
			bucket := tx.Bucket([]byte("messages"))
			for id, m := range next.Messages {
				if before.Messages[id] == m {
					continue
				}
				data, err := json.Marshal(m)
				if err != nil {
					return err
				}
				if err := bucket.Put([]byte(id), data); err != nil {
					return err
				}
			}
			for id := range before.Messages {
				if next.Messages[id] == nil {
					if err := bucket.Delete([]byte(id)); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	b.state = next
	for id, m := range next.Messages {
		if before.Messages[id] == nil {
			b.broadcastLocked(notification{"new", m.summary()})
		} else if before.Messages[id] != m {
			b.broadcastLocked(notification{"update", m.summary()})
		}
	}
	for id := range before.Messages {
		if next.Messages[id] == nil {
			b.deleted.Add(1)
			b.broadcastLocked(notification{"delete", map[string]string{"ID": id}})
		}
	}
	return nil
}

func (b *mailboxBackend) broadcastLocked(n notification) {
	for ch := range b.subscribers {
		select {
		case ch <- n:
		default:
			close(ch)
			delete(b.subscribers, ch)
		}
	}
}
func (b *mailboxBackend) subscribe() (chan notification, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan notification, 64)
	b.subscribers[ch] = struct{}{}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
	}
}

type appendOptions struct {
	Folder      string
	Date        time.Time
	Flags, Tags []string
	From        string
	To          []string
	Username    string
	Notify      bool
}

func (b *mailboxBackend) append(raw []byte, options appendOptions) (*storedMessage, error) {
	if int64(len(raw)) > b.config.MaxSize {
		return nil, errors.New("message exceeds size limit")
	}
	now := time.Now().UTC()
	if options.Date.IsZero() {
		options.Date = now
	}
	d, h, err := parseMessage(raw, options.Date, options.From, options.To)
	if err != nil {
		return nil, err
	}
	m := &storedMessage{MailboxID: b.config.MailboxID, ID: uuid.NewString(), Folder: options.Folder, Created: now, InternalDate: options.Date, Raw: append([]byte(nil), raw...), Flags: append([]string{}, options.Flags...), Tags: append([]string{}, options.Tags...), EnvelopeFrom: options.From, EnvelopeTo: append([]string{}, options.To...), Username: options.Username, Detail: d, Headers: h}
	if err := b.applyTags(m); err != nil {
		return nil, err
	}
	var duplicate *storedMessage
	err = b.mutate(func(state *storeState) error {
		folder, ok := state.Folders[options.Folder]
		if !ok {
			return backend.ErrNoSuchMailbox
		}
		if b.config.IgnoreDuplicates && d.MessageID != "" {
			for _, old := range state.Messages {
				if old.Detail.MessageID == d.MessageID {
					duplicate = old
					return nil
				}
			}
		}
		if folder.NextUID == 0 || folder.NextUID == ^uint32(0) {
			return errors.New("mailbox UID space exhausted")
		}
		m.UID = folder.NextUID
		folder.NextUID++
		state.Folders[folder.Name] = folder
		state.Sequence++
		m.Sequence = state.Sequence
		state.Messages[m.ID] = m
		b.pruneState(state, now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if duplicate != nil {
		b.ignored.Add(1)
		return duplicate, nil
	}
	if options.Notify && b.afterAppend != nil {
		b.afterAppend(m)
	}
	if b.config.DumpPath != "" {
		if err := os.MkdirAll(b.config.DumpPath, 0700); err != nil {
			log.Printf("message stored but dump directory failed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(b.config.DumpPath, m.ID+".eml"), m.Raw, 0600); err != nil {
			log.Printf("message stored but dump failed: %v", err)
		}
	}
	return m, nil
}

func (b *mailboxBackend) pruneState(state *storeState, now time.Time) {
	list := orderedMessages(*state, "")
	for index, m := range list {
		if (b.config.MaxMessages > 0 && len(list)-index > b.config.MaxMessages) || (b.config.MaxAge > 0 && m.Created.Before(now.Add(-b.config.MaxAge))) {
			delete(state.Messages, m.ID)
		}
	}
}
func (b *mailboxBackend) prune() error {
	return b.mutate(func(s *storeState) error { b.pruneState(s, time.Now()); return nil })
}

func (b *mailboxBackend) get(id string) *storedMessage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if id == "latest" {
		list := orderedMessages(b.state, "")
		if len(list) == 0 {
			return nil
		}
		return cloneRecord(list[len(list)-1])
	}
	if m := b.state.Messages[id]; m != nil {
		return cloneRecord(m)
	}
	return nil
}
func (b *mailboxBackend) snapshot() []*storedMessage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	list := orderedMessages(b.state, "")
	for i, m := range list {
		list[i] = cloneRecord(m)
	}
	return list
}
