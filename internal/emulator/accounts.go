package emulator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

// Accounts have disjoint stores, UID spaces, toxic registries and notification
// queues. Only routing and provisioning metadata is shared.
type mailboxAccount struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Username     string    `json:"username"`
	Recipients   []string  `json:"recipients"`
	Created      time.Time `json:"created"`
	PasswordHash string    `json:"password_hash,omitempty"`
}
type managedMailbox struct {
	account mailboxAccount
	store   *mailboxBackend
	handler http.Handler
	stop    func()
}
type mailboxManager struct {
	mu         sync.RWMutex
	config     configuration
	entries    map[string]*managedMailbox
	usernames  map[string]string
	recipients map[string]string
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
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

func openMailboxManager(ctx context.Context, c configuration) (*mailboxManager, error) {
	ctx, cancel := context.WithCancel(ctx)
	h := &mailboxManager{config: c, entries: map[string]*managedMailbox{}, usernames: map[string]string{}, recipients: map[string]string{}, ctx: ctx, cancel: cancel}
	b, err := openMailbox(c)
	if err != nil {
		cancel()
		return nil, err
	}
	h.entries["default"] = &managedMailbox{account: mailboxAccount{ID: "default", Name: "default", Username: c.Username, Recipients: []string{}, Created: time.Now().UTC()}, store: b, stop: func() {}}
	fail := func(err error) (*mailboxManager, error) { h.Close(); return nil, err }
	a, err := h.openAccount(h.entries["default"].account, b)
	if err != nil {
		return fail(err)
	}
	h.entries["default"] = a
	h.usernames[c.Username] = "default"
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
		m, err := h.openAccount(account, nil)
		if err != nil {
			return fail(err)
		}
		h.register(m)
	}
	return h, nil
}
func (h *mailboxManager) databasePath(id string) string {
	if h.config.Database == "" {
		return ""
	}
	return filepath.Join(h.config.Database+".mailboxes", id+".db")
}
func (h *mailboxManager) openAccount(account mailboxAccount, b *mailboxBackend) (*managedMailbox, error) {
	c := h.config
	c.Webroot = ""
	if b == nil {
		c.MailboxID = account.ID
		c.Username = account.Username
		c.Password = account.PasswordHash
		c.Database = h.databasePath(account.ID)
		if c.DumpPath != "" {
			c.DumpPath = filepath.Join(c.DumpPath, account.ID)
		}
		var err error
		b, err = openMailbox(c)
		if err != nil {
			return nil, err
		}
	}
	// A scoped handler has the usual paths without the outer service webroot.
	api, err := newAPI(b)
	if err != nil {
		b.Close()
		return nil, err
	}
	api.webrootOverride = new(string)
	return &managedMailbox{account: account, store: b, handler: api.handler(), stop: b.startWorkers(h.ctx)}, nil
}
func (h *mailboxManager) available(a mailboxAccount) error {
	for _, m := range h.entries {
		if m.account.Name == a.Name {
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
func (h *mailboxManager) register(m *managedMailbox) {
	h.entries[m.account.ID] = m
	h.usernames[m.account.Username] = m.account.ID
	for _, recipient := range m.account.Recipients {
		h.recipients[recipient] = m.account.ID
	}
}
func (h *mailboxManager) description(a mailboxAccount) mailboxDescription {
	return mailboxDescription{ID: a.ID, Name: a.Name, Username: a.Username, Recipients: append([]string{}, a.Recipients...), Created: a.Created, APIBase: h.prefix() + "/mailboxes/" + a.ID}
}
func (h *mailboxManager) create(req mailboxCreation) (mailboxDescription, error) {
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
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
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
	m, err := h.openAccount(account, nil)
	if err != nil {
		return mailboxDescription{}, err
	}
	if db := h.entries["default"].store.db; db != nil {
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
			m.store.Close()
			os.Remove(h.databasePath(id))
			return mailboxDescription{}, err
		}
	}
	h.register(m)
	result := h.description(account)
	result.Password = req.Password
	return result, nil
}
func (h *mailboxManager) lookup(id string) *managedMailbox {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil
	}
	return h.entries[id]
}
func (h *mailboxManager) route(recipient string) *mailboxBackend {
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
		id = "default"
	}
	return h.entries[id].store
}
func (h *mailboxManager) Login(info *imap.ConnInfo, username, password string) (backend.User, error) {
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return nil, backend.ErrInvalidCredentials
	}
	id := h.usernames[username]
	m := h.entries[id]
	h.mu.RUnlock()
	if m == nil {
		return nil, backend.ErrInvalidCredentials
	}
	if id == "default" {
		return m.store.Login(info, username, password)
	}
	if bcrypt.CompareHashAndPassword([]byte(m.account.PasswordHash), []byte(password)) != nil {
		return nil, backend.ErrInvalidCredentials
	}
	select {
	case <-m.store.done:
		return nil, backend.ErrInvalidCredentials
	default:
	}
	return m.store.user, nil
}
func (h *mailboxManager) remove(id string) error {
	if id == "default" {
		return errors.New("the default mailbox cannot be deleted")
	}
	h.mu.Lock()
	m := h.entries[id]
	if h.closed || m == nil {
		h.mu.Unlock()
		return errors.New("mailbox not found")
	}
	if db := h.entries["default"].store.db; db != nil {
		if err := db.Update(func(tx *bolt.Tx) error {
			retired, err := tx.CreateBucketIfNotExists([]byte("mailbox_retired"))
			if err != nil {
				return err
			}
			for _, recipient := range m.account.Recipients {
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
	delete(h.usernames, m.account.Username)
	for _, recipient := range m.account.Recipients {
		h.recipients[recipient] = ""
	}
	h.mu.Unlock()
	m.stop()
	if err := m.store.Close(); err != nil {
		return err
	}
	if path := h.databasePath(id); path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func (h *mailboxManager) Close() error {
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
		m.stop()
		errs = append(errs, m.store.Close())
	}
	return errors.Join(errs...)
}
func (a *httpAPI) listMailboxes(w http.ResponseWriter, r *http.Request) {
	h := a.manager
	h.mu.RLock()
	out := []mailboxDescription{}
	for _, m := range h.entries {
		out = append(out, h.description(m.account))
	}
	h.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	jsonResponse(w, 200, out)
}
func (a *httpAPI) createMailbox(w http.ResponseWriter, r *http.Request) {
	var req mailboxCreation
	if err := decodeJSON(w, r, 64<<10, &req); err != nil {
		apiError(w, 400, err)
		return
	}
	result, err := a.manager.create(req)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, result)
}
func (a *httpAPI) getMailbox(w http.ResponseWriter, r *http.Request) {
	m := a.manager.lookup(r.PathValue("mailbox"))
	if m == nil {
		apiError(w, 404, errors.New("mailbox not found"))
		return
	}
	jsonResponse(w, 200, a.manager.description(m.account))
}
func (a *httpAPI) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("mailbox")
	if a.manager.lookup(id) == nil {
		apiError(w, 404, errors.New("mailbox not found"))
		return
	}
	if err := a.manager.remove(id); err != nil {
		apiError(w, 400, err)
		return
	}
	w.WriteHeader(204)
}
func (a *httpAPI) scopedMailbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("mailbox")
	m := a.manager.lookup(id)
	if m == nil {
		apiError(w, 404, errors.New("mailbox not found"))
		return
	}
	prefix := a.manager.prefix() + "/mailboxes/" + id
	http.StripPrefix(prefix, m.handler).ServeHTTP(w, r)
}

func (h *mailboxManager) prefix() string {
	root := strings.Trim(h.config.Webroot, "/")
	if root == "" {
		return ""
	}
	return "/" + root
}
