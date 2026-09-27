package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
	bolt "go.etcd.io/bbolt"
)

func testManager(t *testing.T, c configuration) (*mailboxManager, http.Handler) {
	t.Helper()
	h, err := openMailboxManager(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	a, err := newAPI(h.lookup("default").store)
	if err != nil {
		t.Fatal(err)
	}
	a.manager = h
	return h, a.handler()
}
func createAccount(t *testing.T, handler http.Handler, name string) mailboxDescription {
	t.Helper()
	response := apiCall(t, handler, "POST", "/api/v1/mailboxes", mailboxCreation{Name: name, Username: name, Password: "password-" + name, Recipients: []string{name + "@example.test"}}, 201)
	var m mailboxDescription
	if err := json.Unmarshal(response.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Password == "" || m.ID == "" {
		t.Fatal(m)
	}
	return m
}
func accountCount(t *testing.T, handler http.Handler, base string) int {
	t.Helper()
	var list messagesSummary
	json.Unmarshal(apiCall(t, handler, "GET", base+"/api/v1/messages", nil, 200).Body.Bytes(), &list)
	return list.Total
}

func TestSeparateMailboxesIsolateSMTPIMAPHTTPAndToxics(t *testing.T) {
	h, api := testManager(t, defaultConfig())
	a := createAccount(t, api, "a")
	b := createAccount(t, api, "b")
	defaultStore := h.lookup("default").store
	addr := smtpAddress(t, defaultStore, h)
	imapAddr := imapAddress(t, h)
	definition := map[string]any{"name": "same-name", "type": "smtp_reject", "enabled": true, "selector": map[string]any{"headers": map[string]string{"X-Test-ID": "shared-token"}}, "attributes": map[string]int{"code": 451}}
	apiCall(t, api, "POST", a.APIBase+"/api/v1/toxics", definition, 201)
	apiCall(t, api, "POST", "/api/v1/toxics", definition, 201)
	definition["enabled"] = false
	apiCall(t, api, "POST", b.APIBase+"/api/v1/toxics", definition, 201)
	send := func(recipient string) error {
		return smtp.SendMail(addr, nil, "sender@example.test", []string{recipient}, testRaw("same-message@test", "shared-token", recipient))
	}
	failures, success := make(chan error, 1), make(chan error, 1)
	go func() { failures <- send(a.Recipients[0]) }()
	go func() { success <- send(b.Recipients[0]) }()
	if err := <-failures; err == nil || !strings.Contains(err.Error(), "451") {
		t.Fatalf("target rejection %v", err)
	}
	if err := <-success; err != nil {
		t.Fatal("other account affected", err)
	}
	if accountCount(t, api, a.APIBase) != 0 || accountCount(t, api, b.APIBase) != 1 || accountCount(t, api, "") != 0 {
		t.Fatal("SMTP routed to wrong mailbox")
	}
	if h.lookup(a.ID).store.toxics.list()[0].Hits != 1 || h.lookup(b.ID).store.toxics.list()[0].Hits != 0 || defaultStore.toxics.list()[0].Hits != 0 {
		t.Fatal("toxic counters crossed account boundary")
	}
	apiCall(t, api, "PATCH", a.APIBase+"/api/v1/toxics/same-name", map[string]bool{"enabled": false}, 200)
	if err := send(a.Recipients[0]); err != nil {
		t.Fatal(err)
	}
	ca := imapClient(t, imapAddr, a.Username, a.Password)
	cb := imapClient(t, imapAddr, b.Username, b.Password)
	idsA, err := ca.UidSearch(imap.NewSearchCriteria())
	if err != nil || len(idsA) != 1 || idsA[0] != 1 {
		t.Fatalf("A IDs %v %v", idsA, err)
	}
	idsB, err := cb.UidSearch(imap.NewSearchCriteria())
	if err != nil || len(idsB) != 1 || idsB[0] != 1 {
		t.Fatalf("B IDs %v %v", idsB, err)
	}
	apiCall(t, api, "POST", a.APIBase+"/api/v1/toxics", map[string]any{"name": "hide", "type": "imap_hide", "selector": map[string]string{"message_id": "same-message@test"}}, 201)
	idsA, err = ca.UidSearch(imap.NewSearchCriteria())
	if err != nil || len(idsA) != 0 {
		t.Fatal("A hide failed", idsA, err)
	}
	idsB, err = cb.UidSearch(imap.NewSearchCriteria())
	if err != nil || len(idsB) != 1 {
		t.Fatal("A hide affected B", idsB, err)
	}
	bMessage := h.lookup(b.ID).store.snapshot()[0]
	apiCall(t, api, "GET", a.APIBase+"/api/v1/message/"+bMessage.ID, nil, 404)
	apiCall(t, api, "DELETE", a.APIBase+"/api/v1/messages", map[string]any{}, 200)
	if accountCount(t, api, b.APIBase) != 1 {
		t.Fatal("A deletion affected B")
	}
	apiCall(t, api, "DELETE", "/api/v1/mailboxes/"+a.ID, nil, 204)
	apiCall(t, api, "GET", a.APIBase+"/api/v1/messages", nil, 404)
	if _, err := ca.UidSearch(imap.NewSearchCriteria()); err == nil {
		t.Fatal("deleted account IMAP session still reads")
	}
	if _, err := h.Login(nil, a.Username, a.Password); err == nil {
		t.Fatal("deleted login accepted")
	}
	if err := send(a.Recipients[0]); err == nil {
		t.Fatal("deleted recipient fell through to default")
	}
	if accountCount(t, api, b.APIBase) != 1 || accountCount(t, api, "") != 0 {
		t.Fatal("delete affected other stores")
	}
}

func TestMailboxProvisioningValidationAndPersistence(t *testing.T) {
	c := defaultConfig()
	c.Database = filepath.Join(t.TempDir(), "mail.db")
	h, api := testManager(t, c)
	a := createAccount(t, api, "persist-a")
	b := createAccount(t, api, "persist-b")
	emptyValidity := h.lookup(a.ID).store.state.Folders["Sent"].Validity
	message := appendTest(t, h.lookup(b.ID).store, "persist@test")
	apiCall(t, api, "POST", b.APIBase+"/api/v1/toxics", map[string]any{"name": "ephemeral", "type": "imap_hide", "selector": map[string]string{"message_id": "persist@test"}}, 201)
	for _, req := range []mailboxCreation{{Name: "persist-a"}, {Username: a.Username}, {Recipients: []string{a.Recipients[0]}}, {Recipients: []string{"*@example.test"}}} {
		apiCall(t, api, "POST", "/api/v1/mailboxes", req, 400)
	}
	list := apiCall(t, api, "GET", "/api/v1/mailboxes", nil, 200).Body.String()
	if strings.Contains(list, a.Password) || strings.Contains(list, "password_hash") {
		t.Fatal("credential leaked in list")
	}
	if _, err := h.Login(nil, a.Username, b.Password); err == nil {
		t.Fatal("cross-account password accepted")
	}
	var saved []byte
	h.lookup("default").store.db.View(func(tx *bolt.Tx) error {
		saved = append([]byte{}, tx.Bucket([]byte("mailbox_accounts")).Get([]byte(a.ID))...)
		return nil
	})
	if bytes.Contains(saved, []byte(a.Password)) {
		t.Fatal("plaintext password persisted")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, api = testManager(t, c)
	if _, err := h.Login(nil, b.Username, b.Password); err != nil {
		t.Fatal("credentials not restored", err)
	}
	if h.lookup(a.ID).store.state.Folders["Sent"].Validity != emptyValidity {
		t.Fatal("empty mailbox UIDVALIDITY changed")
	}
	if !bytes.Equal(h.lookup(b.ID).store.get(message.ID).Raw, message.Raw) || len(h.lookup(b.ID).store.toxics.list()) != 0 {
		t.Fatal("message/toxic restart state wrong")
	}
	path := h.databasePath(b.ID)
	apiCall(t, api, "DELETE", "/api/v1/mailboxes/"+b.ID, nil, 204)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("deleted database still exists", err)
	}
	h.Close()
	h, api = testManager(t, c)
	if h.route(b.Recipients[0]) != nil {
		t.Fatal("retired recipient lost on restart")
	}
	if h.lookup(b.ID) != nil {
		t.Fatal("deleted account resurrected")
	}
	recreated := createAccount(t, api, "persist-b")
	if recreated.ID == b.ID || h.route(b.Recipients[0]) != h.lookup(recreated.ID).store {
		t.Fatal("recipient could not be reassigned")
	}
	apiCall(t, api, "DELETE", "/api/v1/mailboxes/default", nil, 400)
}

func TestMixedMailboxSMTPRecipientsAreRejectedAndResetWorks(t *testing.T) {
	h, api := testManager(t, defaultConfig())
	a := createAccount(t, api, "one")
	b := createAccount(t, api, "two")
	addr := smtpAddress(t, h.lookup("default").store, h)
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Mail("sender@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt(a.Recipients[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt(b.Recipients[0]); err == nil || !strings.Contains(err.Error(), "553") {
		t.Fatal("mixed account recipients accepted", err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	w.Write(testRaw("one@test", "same", a.Recipients[0]))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := c.Mail("sender@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt(b.Recipients[0]); err != nil {
		t.Fatal(err)
	}
	w, err = c.Data()
	if err != nil {
		t.Fatal(err)
	}
	w.Write(testRaw("two@test", "same", b.Recipients[0]))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if accountCount(t, api, a.APIBase) != 1 || accountCount(t, api, b.APIBase) != 1 || accountCount(t, api, "") != 0 {
		t.Fatal("RSET did not clear account routing")
	}
}

func TestScopedMailboxAPIPreservesWebrootAndAuthentication(t *testing.T) {
	c := defaultConfig()
	c.Webroot = "/mail"
	c.HTTPAuth = "admin:secret"
	h, handler := testManager(t, c)
	created, err := h.create(mailboxCreation{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.APIBase, "/mail/mailboxes/") || len(created.Recipients) != 1 {
		t.Fatal(created)
	}
	apiCall(t, handler, "GET", created.APIBase+"/api/v1/messages", nil, 401)
	req := httptest.NewRequest("GET", created.APIBase+"/api/v1/messages", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	apiCall(t, handler, "GET", created.APIBase+"/healthz", nil, 200)
}

func TestAccountsHaveIndependentRetentionAndNotificationStreams(t *testing.T) {
	c := defaultConfig()
	c.MaxMessages = 1
	c.IgnoreDuplicates = true
	c.SMTPFolder = "Outbox"
	h, api := testManager(t, c)
	a := createAccount(t, api, "retained-a")
	b := createAccount(t, api, "retained-b")
	storeA, storeB := h.lookup(a.ID).store, h.lookup(b.ID).store
	eventsA, stopA := storeA.subscribe()
	defer stopA()
	eventsB, stopB := storeB.subscribe()
	defer stopB()
	first, err := storeB.append(testRaw("duplicate@test", "same", b.Recipients[0]), appendOptions{Folder: c.SMTPFolder})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"duplicate@test", "new@test"} {
		if _, err := storeA.append(testRaw(id, "same", a.Recipients[0]), appendOptions{Folder: c.SMTPFolder}); err != nil {
			t.Fatal(err)
		}
	}
	if len(storeA.snapshot()) != 1 || storeA.snapshot()[0].Detail.MessageID != "new@test" {
		t.Fatal("A retention failed")
	}
	if got := storeB.get(first.ID); got == nil || got.MailboxID != b.ID {
		t.Fatal("B mail evicted or mislabeled")
	}
	select {
	case event := <-eventsB:
		if event.Type != "new" || event.Data.(messageSummary).MailboxID != b.ID {
			t.Fatal(event)
		}
	default:
		t.Fatal("B event missing")
	}
	select {
	case event := <-eventsB:
		t.Fatal("B stream got A event", event)
	default:
	}
	select {
	case event := <-eventsA:
		if event.Data.(messageSummary).MailboxID != a.ID {
			t.Fatal(event)
		}
	default:
		t.Fatal("A event missing")
	}
	if _, err := storeA.user.GetMailbox(c.SMTPFolder); err != nil {
		t.Fatal("configured SMTP folder not created", err)
	}
}
