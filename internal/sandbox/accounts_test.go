package sandbox

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestSMTPAuthenticationOwnsOutboundMailAcrossParallelAccounts(t *testing.T) {
	c := defaultConfig()
	c.SMTPAllowInsecureAuth = true
	service, h := testManager(t, c)
	one := createAccount(t, h, "one")
	two := createAccount(t, h, "two")
	address := smtpAddress(t, service.lookup(defaultAccountID))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, a := range []mailboxDescription{one, two} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- smtp.SendMail(address, smtp.PlainAuth("", a.Username, a.Password, "127.0.0.1"), "app@example.test", []string{"alice@customer.test", one.Recipients[0]}, testRaw("same@test", "same", "alice@customer.test"))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []mailboxDescription{one, two} {
		if n := accountCount(t, h, a.APIBase); n != 1 {
			t.Fatal(a.ID, n)
		}
		if service.authenticate(a.Username, a.Password) == nil {
			t.Fatal("account login failed")
		}
		cost, err := bcrypt.Cost([]byte(service.lookup(a.ID).identity.PasswordHash))
		if err != nil || cost != bcrypt.MinCost {
			t.Fatal("test credentials use excessive cost", cost, err)
		}
	}
	if accountCount(t, h, defaultAPI) != 0 {
		t.Fatal("authenticated mail fell through to shared account")
	}
	if err := smtp.SendMail(address, smtp.PlainAuth("", one.Username, two.Password, "127.0.0.1"), "app@example.test", []string{"alice@customer.test"}, testRaw("bad@test", "bad", "alice@customer.test")); err == nil {
		t.Fatal("wrong password accepted")
	}
}
func TestSMTPFansOutOncePerAccountAndResets(t *testing.T) {
	service, h := testManager(t, defaultConfig())
	one := createAccount(t, h, "one")
	two := createAccount(t, h, "two")
	c, err := smtp.Dial(smtpAddress(t, service.lookup(defaultAccountID)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i, recipients := range [][]string{{one.Recipients[0], two.Recipients[0], one.Recipients[0]}, {two.Recipients[0]}} {
		if err := c.Mail("app@example.test"); err != nil {
			t.Fatal(err)
		}
		for _, to := range recipients {
			if err := c.Rcpt(to); err != nil {
				t.Fatal(err)
			}
		}
		w, err := c.Data()
		if err != nil {
			t.Fatal(err)
		}
		w.Write(testRaw(fmt.Sprintf("fanout-%d@test", i), "fanout", one.Recipients[0]))
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := c.Reset(); err != nil {
			t.Fatal(err)
		}
	}
	if accountCount(t, h, one.APIBase) != 1 || accountCount(t, h, two.APIBase) != 2 || accountCount(t, h, defaultAPI) != 0 {
		t.Fatal("fan-out or transaction reset failed")
	}
	for _, a := range []mailboxDescription{one, two} {
		if service.lookup(a.ID).snapshot()[0].UID != 1 {
			t.Fatal("UID spaces are shared")
		}
	}
}
func TestAccountPersistenceDeletionAndRetiredRecipients(t *testing.T) {
	c := defaultConfig()
	c.Database = filepath.Join(t.TempDir(), "mail.db")
	service, h := testManager(t, c)
	a := createAccount(t, h, "persist")
	stored := appendTest(t, service.lookup(a.ID), "persist@test")
	validity := service.lookup(a.ID).state.Folders["Sent"].Validity
	apiCall(t, h, "GET", defaultAPI+"/messages/"+stored.ID, nil, 404)
	for _, body := range []mailboxCreation{{Name: a.Name}, {Username: a.Username}, {Recipients: a.Recipients}, {Recipients: []string{"*@example.test"}}} {
		apiCall(t, h, "POST", "/api/v1/accounts", body, 400)
	}
	list := apiCall(t, h, "GET", "/api/v1/accounts", nil, 200).Body.String()
	if strings.Contains(list, a.Password) || strings.Contains(list, "password_hash") {
		t.Fatal("credentials exposed")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service, h = testManager(t, c)
	if service.authenticate(a.Username, a.Password) == nil {
		t.Fatal("account login failed")
	}
	restored := service.lookup(a.ID)
	if !bytes.Equal(restored.get(stored.ID).Raw, stored.Raw) || restored.state.Folders["Sent"].Validity != validity {
		t.Fatal("persistence lost MIME or UIDVALIDITY")
	}
	path := service.databasePath(a.ID)
	apiCall(t, h, "DELETE", "/api/v1/accounts/"+a.ID, nil, 204)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("deleted database remains", err)
	}
	if service.authenticate(a.Username, a.Password) != nil {
		t.Fatal("deleted login accepted")
	}
	service.Close()
	service, h = testManager(t, c)
	if service.route(a.Recipients[0]) != nil {
		t.Fatal("retired recipient fell through after restart")
	}
	replacement := createAccount(t, h, "persist")
	if replacement.ID == a.ID || service.route(a.Recipients[0]) != service.lookup(replacement.ID) {
		t.Fatal("recipient reassignment failed")
	}
	apiCall(t, h, "DELETE", "/api/v1/accounts/default", nil, 400)
}
func TestAuthenticationCannotFallThroughAfterAccountDeletion(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	service, h := testManager(t, cfg)
	a := createAccount(t, h, "removed")
	c, err := smtp.Dial(smtpAddress(t, service.lookup(defaultAccountID)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Auth(smtp.PlainAuth("", a.Username, a.Password, "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	apiCall(t, h, "DELETE", "/api/v1/accounts/"+a.ID, nil, 204)
	if err := c.Mail("app@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("alice@customer.test"); err == nil {
		t.Fatal("removed authenticated account routed elsewhere")
	}
	if accountCount(t, h, defaultAPI) != 0 {
		t.Fatal("deleted account leaked mail")
	}
}
func TestSingleRouteTreePreservesWebrootAndAuth(t *testing.T) {
	c := defaultConfig()
	c.Webroot = "/mail"
	c.HTTPAuth = "admin:secret"
	service, h := testManager(t, c)
	a, err := service.create(mailboxCreation{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.APIBase, "/mail/api/v1/accounts/") {
		t.Fatal(a.APIBase)
	}
	apiCall(t, h, "GET", a.APIBase+"/messages", nil, 401)
	req := httptest.NewRequest("GET", a.APIBase+"/messages", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	apiCall(t, h, "GET", "/mail/healthz", nil, 200)
}
func TestAccountsDefaultToUnlimitedRetentionAcrossFolders(t *testing.T) {
	service, h := testManager(t, defaultConfig())
	a := createAccount(t, h, "retention")
	account := service.lookup(a.ID)
	seeded, err := account.append(testRaw("fixture@test", "fixture", "test@example.test"), appendOptions{Folder: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		appendTest(t, account, fmt.Sprintf("sent-%d@test", i))
	}
	if len(account.snapshot()) != 502 || account.get(seeded.ID) == nil {
		t.Fatal("default retention removed fixtures")
	}
}
