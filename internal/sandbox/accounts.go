package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

// Accounts have disjoint stores, UID spaces, fault registries and notification
// queues. Only routing and provisioning metadata is shared.
type mailboxAccount struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Username     string    `json:"username"`
	Recipients   []string  `json:"recipients"`
	Created      time.Time `json:"created"`
	PasswordHash string    `json:"password_hash,omitempty"`
}

// Service owns global configuration, routing, HTTP credentials and server faults.
type Service struct {
	mu           sync.RWMutex
	config       configuration
	entries      map[string]*Account
	usernames    map[string]string
	recipients   map[string]string
	serverFaults *faultRegistry
	httpUsers    credentials
	ctx          context.Context
	cancel       context.CancelFunc
	closed       bool
}

// Account owns credentials, faults and notifications; Store owns persisted state.
type Account struct {
	*Store
	service                                            *Service
	identity                                           mailboxAccount
	user                                               *mailboxUser
	faults                                             *faultRegistry
	notifier                                           *eventHub
	stop                                               func()
	afterAppend                                        func(*storedMessage)
	started                                            time.Time
	accepted, rejected, ignored, deleted, acceptedSize atomic.Uint64
}
type mailboxCreation struct {
	Name       string   `json:"name"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	Recipients []string `json:"recipients"`
}
type mailboxDescription struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Username   string    `json:"username"`
	Recipients []string  `json:"recipients"`
	Created    time.Time `json:"created"`
	APIBase    string    `json:"api_base"`
	Password   string    `json:"password,omitempty"`
}

func openService(ctx context.Context, c configuration) (*Service, error) {
	ctx, cancel := context.WithCancel(ctx)
	h := &Service{config: c, entries: map[string]*Account{}, usernames: map[string]string{}, recipients: map[string]string{}, ctx: ctx, cancel: cancel, serverFaults: newFaultRegistry()}
	auth, err := readCredentials(c.HTTPAuthFile, c.HTTPAuth)
	if err != nil {
		cancel()
		return nil, err
	}
	h.httpUsers = auth
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.MinCost)
	if err != nil {
		cancel()
		return nil, err
	}
	a, err := h.openAccount(mailboxAccount{ID: defaultAccountID, Name: defaultAccountID, Username: c.Username, Recipients: []string{}, Created: time.Now().UTC(), PasswordHash: string(hash)})
	if err != nil {
		cancel()
		return nil, err
	}
	h.register(a)
	b := a.Store
	fail := func(err error) (*Service, error) { h.Close(); return nil, err }
	var saved []mailboxAccount
	if b.db != nil {
		err = b.db.View(func(tx *bolt.Tx) error {
			if retired := tx.Bucket([]byte("mailbox_retired")); retired != nil {
				if err := retired.ForEach(func(key, value []byte) error { h.recipients[string(key)] = ""; return nil }); err != nil {
					return err
				}
			}
			bucket := tx.Bucket([]byte("mailbox_accounts"))
			if bucket == nil {
				return nil
			}
			return bucket.ForEach(func(_, value []byte) error {
				var account mailboxAccount
				if err := json.Unmarshal(value, &account); err != nil {
					return err
				}
				saved = append(saved, account)
				return nil
			})
		})
		if err != nil {
			return fail(err)
		}
	}
	for _, account := range saved {
		if _, err := uuid.Parse(account.ID); err != nil {
			return fail(errors.New("invalid persisted mailbox ID"))
		}
		if err := h.available(account); err != nil {
			return fail(err)
		}
		if _, err := os.Stat(h.databasePath(account.ID)); err != nil {
			return fail(fmt.Errorf("persisted mailbox %s database missing: %w", account.ID, err))
		}
		m, err := h.openAccount(account)
		if err != nil {
			return fail(err)
		}
		h.register(m)
	}
	return h, nil
}
func (h *Service) databasePath(id string) string {
	if h.config.Database == "" {
		return ""
	}
	return filepath.Join(h.config.Database+".mailboxes", id+".db")
}
func (h *Service) openAccount(identity mailboxAccount) (*Account, error) {
	c := h.config
	path := h.databasePath(identity.ID)
	dump := c.DumpPath
	if identity.ID == defaultAccountID {
		path = c.Database
	} else if dump != "" {
		dump = filepath.Join(dump, identity.ID)
	}
	store, err := openStore(storeOptions{MailboxID: identity.ID, Database: path, SMTPFolder: c.SMTPFolder, MaxMessages: c.MaxMessages, MaxAge: c.MaxAge, MaxSize: c.MaxSize, IgnoreDuplicates: c.IgnoreDuplicates, DumpPath: dump})
	if err != nil {
		return nil, err
	}
	a := &Account{Store: store, service: h, identity: identity, faults: newFaultRegistry(), notifier: newEventHub(), started: time.Now()}
	a.user = &mailboxUser{store: a}
	store.onChange = a.changed
	a.stop = a.startWorkers(h.ctx)
	return a, nil
}

func (a *Account) Close() error {
	a.stop()
	err := a.Store.Close()
	a.notifier.close()
	return err
}
func (a *Account) append(raw []byte, options appendOptions) (*storedMessage, error) {
	m, created, err := a.Store.append(raw, options)
	if err == nil && !created {
		a.ignored.Add(1)
	}
	if err == nil && created && options.Notify && a.afterAppend != nil {
		a.afterAppend(m)
	}
	return m, err
}
func (a *Account) changed(before, after storeState) {
	for id, m := range after.Messages {
		if before.Messages[id] == nil {
			a.notifier.publish(notification{"new", m.summary()})
		} else if before.Messages[id] != m {
			a.notifier.publish(notification{"update", m.summary()})
		}
	}
	for id := range before.Messages {
		if after.Messages[id] == nil {
			a.deleted.Add(1)
			a.notifier.publish(notification{"delete", map[string]string{"id": id}})
		}
	}
}
func (h *Service) available(a mailboxAccount) error {
	for _, m := range h.entries {
		if m.identity.Name == a.Name {
			return errors.New("mailbox name already exists")
		}
	}
	if _, ok := h.usernames[a.Username]; ok {
		return errors.New("mailbox username already exists")
	}
	for _, address := range a.Recipients {
		if id, ok := h.recipients[address]; ok && id != "" {
			return errors.New("recipient is already assigned to another mailbox")
		}
	}
	return nil
}
func (h *Service) register(m *Account) {
	h.entries[m.identity.ID] = m
	h.usernames[m.identity.Username] = m.identity.ID
	for _, recipient := range m.identity.Recipients {
		h.recipients[recipient] = m.identity.ID
	}
}
func (h *Service) description(a mailboxAccount) mailboxDescription {
	return mailboxDescription{ID: a.ID, Name: a.Name, Username: a.Username, Recipients: append([]string{}, a.Recipients...), Created: a.Created, APIBase: h.config.Webroot + "/api/v1/accounts/" + a.ID}
}
func (h *Service) create(req mailboxCreation) (mailboxDescription, error) {
	id := uuid.NewString()
	if req.Name == "" {
		req.Name = id
	}
	if req.Username == "" {
		req.Username = "mailbox-" + id
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 100 || strings.ContainsAny(req.Name, "\r\n\x00") {
		return mailboxDescription{}, errors.New("mailbox name must be 1-100 characters")
	}
	if strings.TrimSpace(req.Username) == "" || len(req.Username) > 255 || strings.ContainsAny(req.Username, "\r\n\x00") {
		return mailboxDescription{}, errors.New("invalid mailbox username")
	}
	if req.Password == "" {
		var bytes [24]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return mailboxDescription{}, err
		}
		req.Password = base64.RawURLEncoding.EncodeToString(bytes[:])
	}
	if len(req.Password) > 72 || strings.ContainsAny(req.Password, "\r\n\x00") {
		return mailboxDescription{}, errors.New("mailbox password must be 1-72 bytes without control characters")
	}
	if len(req.Recipients) == 0 {
		req.Recipients = []string{id + "@mailbox.test"}
	}
	seen := map[string]bool{}
	recipients := []string{}
	for _, address := range req.Recipients {
		address = strings.ToLower(strings.TrimSpace(address))
		if !validEnvelope(address, false) || strings.ContainsAny(address, "*?") {
			return mailboxDescription{}, errors.New("recipients must be exact email addresses without wildcards")
		}
		if seen[address] {
			continue
		}
		seen[address] = true
		recipients = append(recipients, address)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
	if err != nil {
		return mailboxDescription{}, err
	}
	account := mailboxAccount{ID: id, Name: req.Name, Username: req.Username, Recipients: recipients, Created: time.Now().UTC(), PasswordHash: string(hash)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return mailboxDescription{}, errors.New("mail service is closed")
	}
	if err := h.available(account); err != nil {
		return mailboxDescription{}, err
	}
	m, err := h.openAccount(account)
	if err != nil {
		return mailboxDescription{}, err
	}
	if db := h.entries[defaultAccountID].db; db != nil {
		err = db.Update(func(tx *bolt.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists([]byte("mailbox_accounts"))
			if err != nil {
				return err
			}
			raw, err := json.Marshal(account)
			if err != nil {
				return err
			}
			if retired := tx.Bucket([]byte("mailbox_retired")); retired != nil {
				for _, address := range account.Recipients {
					if err := retired.Delete([]byte(address)); err != nil {
						return err
					}
				}
			}
			return bucket.Put([]byte(id), raw)
		})
		if err != nil {
			m.stop()
			m.Close()
			os.Remove(h.databasePath(id))
			return mailboxDescription{}, err
		}
	}
	h.register(m)
	result := h.description(account)
	result.Password = req.Password
	return result, nil
}
func (h *Service) lookup(id string) *Account {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil
	}
	return h.entries[id]
}
func (h *Service) route(recipient string) *Account {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil
	}
	id, known := h.recipients[strings.ToLower(recipient)]
	if known && id == "" {
		return nil
	}
	if !known {
		id = defaultAccountID
	}
	return h.entries[id]
}
func (h *Service) authenticate(username, password string) *Account {
	a := h.accountByUsername(username)
	if a == nil || bcrypt.CompareHashAndPassword([]byte(a.identity.PasswordHash), []byte(password)) != nil {
		return nil
	}
	select {
	case <-a.done:
		return nil
	default:
		return a
	}
}

func (h *Service) accountByUsername(username string) *Account {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil
	}
	return h.entries[h.usernames[username]]
}
func (h *Service) remove(id string) error {
	if id == defaultAccountID {
		return errors.New("the default mailbox cannot be deleted")
	}
	h.mu.Lock()
	m := h.entries[id]
	if h.closed || m == nil {
		h.mu.Unlock()
		return errors.New("mailbox not found")
	}
	if db := h.entries[defaultAccountID].db; db != nil {
		if err := db.Update(func(tx *bolt.Tx) error {
			retired, err := tx.CreateBucketIfNotExists([]byte("mailbox_retired"))
			if err != nil {
				return err
			}
			for _, recipient := range m.identity.Recipients {
				if err := retired.Put([]byte(recipient), []byte{1}); err != nil {
					return err
				}
			}
			return tx.Bucket([]byte("mailbox_accounts")).Delete([]byte(id))
		}); err != nil {
			h.mu.Unlock()
			return err
		}
	}
	delete(h.entries, id)
	delete(h.usernames, m.identity.Username)
	for _, recipient := range m.identity.Recipients {
		h.recipients[recipient] = ""
	}
	h.mu.Unlock()
	if err := m.Close(); err != nil {
		return err
	}
	if path := h.databasePath(id); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func (h *Service) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.cancel()
	entries := h.entries
	h.mu.Unlock()
	var errs []error
	for _, m := range entries {
		errs = append(errs, m.Close())
	}
	return errors.Join(errs...)
}
