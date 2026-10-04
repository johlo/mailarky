package sandbox

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
	"time"

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

// Store owns only immutable message snapshots, folder state and persistence.
// Its change callback runs after commit and must not read the store or block.
type Store struct {
	mu       sync.RWMutex
	state    storeState
	options  storeOptions
	db       *bolt.DB
	done     chan struct{}
	closed   sync.Once
	changed  chan struct{}
	onChange func(storeState, storeState)
}
type storeOptions struct {
	Limits           accountLimits
	MailboxID        string
	Database         string
	SMTPFolder       string
	MaxMessages      int
	MaxAge           time.Duration
	MaxSize          int64
	IgnoreDuplicates bool
	DumpPath         string
}

func newValidity() uint32 {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return binary.BigEndian.Uint32(value[:]) | 1
}

func openStore(c storeOptions) (*Store, error) {
	if !validFolder(c.SMTPFolder) {
		return nil, errors.New("invalid SMTP folder name")
	}
	b := &Store{options: c, state: storeState{Folders: map[string]folderState{}, Messages: map[string]*storedMessage{}}, done: make(chan struct{}), changed: make(chan struct{})}
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
				d, h, err := parseMessage(m.Raw, m.InternalDate, m.EnvelopeFrom, m.EnvelopeTo)
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

func (b *Store) Close() error {
	var err error
	b.closed.Do(func() {
		close(b.done)
		b.mu.Lock()
		defer b.mu.Unlock()
		close(b.changed)
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
	return &copy
}

func (b *Store) mutate(change func(*storeState) error) error {
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
	close(b.changed)
	b.changed = make(chan struct{})
	if b.onChange != nil {
		b.onChange(before, next)
	}

	return nil
}

type appendOptions struct {
	Folder   string
	Date     time.Time
	Flags    []string
	From     string
	To       []string
	Username string
	Notify   bool
	Parsed   *parsedMessage
}

func (b *Store) append(raw []byte, options appendOptions) (*storedMessage, bool, error) {
	if int64(len(raw)) > b.options.MaxSize {
		return nil, false, errors.New("message exceeds size limit")
	}
	now := time.Now().UTC()
	if options.Date.IsZero() {
		options.Date = now
	}
	parsed := options.Parsed
	if parsed == nil {
		d, h, err := parseMessage(raw, options.Date, options.From, options.To)
		if err != nil {
			return nil, false, err
		}
		parsed = &parsedMessage{detail: d, headers: h}
	}
	d, h := withEnvelope(parsed.detail, options.To), parsed.headers
	m := &storedMessage{MailboxID: b.options.MailboxID, ID: uuid.NewString(), Folder: options.Folder, Created: now, InternalDate: options.Date, Raw: append([]byte(nil), raw...), Flags: append([]string{}, options.Flags...), EnvelopeFrom: options.From, EnvelopeTo: append([]string{}, options.To...), Username: options.Username, Detail: d, Headers: h}
	var duplicate *storedMessage
	err := b.mutate(func(state *storeState) error {
		folder, ok := state.Folders[options.Folder]
		if !ok {
			return errNoSuchMailbox
		}
		if b.options.IgnoreDuplicates && d.MessageID != "" {
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
		if err := b.checkQuota(*state, 1, int64(len(raw))); err != nil {
			return err
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
		return nil, false, err
	}
	if duplicate != nil {
		return duplicate, false, nil
	}
	if b.options.DumpPath != "" {
		if err := os.MkdirAll(b.options.DumpPath, 0700); err != nil {
			log.Printf("message stored but dump directory failed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(b.options.DumpPath, m.ID+".eml"), m.Raw, 0600); err != nil {
			log.Printf("message stored but dump failed: %v", err)
		}
	}
	return m, true, nil
}

func (b *Store) pruneState(state *storeState, now time.Time) {
	list := orderedMessages(*state, "")
	for index, m := range list {
		if (b.options.MaxMessages > 0 && len(list)-index > b.options.MaxMessages) || (b.options.MaxAge > 0 && m.Created.Before(now.Add(-b.options.MaxAge))) {
			delete(state.Messages, m.ID)
		}
	}
}
func (b *Store) prune() error {
	return b.mutate(func(s *storeState) error { b.pruneState(s, time.Now()); return nil })
}

func (b *Store) get(id string) *storedMessage {
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
func (b *Store) snapshot() []*storedMessage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	list := orderedMessages(b.state, "")
	for i, m := range list {
		list[i] = cloneRecord(m)
	}
	return list
}

// watch registers atomically with a state read, so IDLE cannot miss a commit.
func (b *Store) watch() <-chan struct{} {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.changed
}
