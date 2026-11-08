package sandbox

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/smtp"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebmailLoginUsesNormalProtocolsAndKeepsTestPassword(t *testing.T) {
	c := defaultConfig()
	c.SMTPAllowInsecureAuth = true
	service, handler := testManager(t, c)
	one := createAccount(t, handler, "picker-one")
	two := createAccount(t, handler, "picker-two")
	response := apiCall(t, handler, "POST", one.APIBase+"/webmail-login", nil, 201)
	var login webmailCredentials
	if err := json.Unmarshal(response.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if response.Header().Get("Cache-Control") != "no-store" || login.Username != one.Username || login.Password == one.Password || len(login.Password) > 72 {
		t.Fatal("invalid or cacheable webmail credential response")
	}
	if ttl := time.Until(login.ExpiresAt); ttl < 59*time.Minute || ttl > time.Hour {
		t.Fatal("unexpected credential lifetime", ttl)
	}
	account := service.lookup(one.ID)
	smtpAddr := smtpAddress(t, account)
	if err := smtp.SendMail(smtpAddr, smtp.PlainAuth("", login.Username, login.Password, "127.0.0.1"), "sender@example.test", []string{"recipient@example.test"}, testRaw("picker@test", "picker", "recipient@example.test")); err != nil {
		t.Fatal(err)
	}
	imap := imapClient(t, imapAddress(t, service), login.Username, login.Password)
	selected, err := imap.Select("Sent", nil).Wait()
	if err != nil || selected.NumMessages != 1 {
		t.Fatal("temporary credentials did not open the captured mail", selected, err)
	}
	if accountCount(t, handler, two.APIBase) != 0 || service.authenticate(two.Username, login.Password) != nil {
		t.Fatal("webmail login escaped its account")
	}
	if service.authenticate(one.Username, one.Password) == nil {
		t.Fatal("original password changed")
	}
	for _, path := range []string{"/api/v1/accounts", one.APIBase, one.APIBase + "/history"} {
		body := apiCall(t, handler, "GET", path, nil, 200).Body.String()
		if strings.Contains(body, login.Password) || strings.Contains(body, one.Password) {
			t.Fatal("credentials exposed by", path)
		}
	}
	apiCall(t, handler, "DELETE", one.APIBase, nil, 204)
	apiCall(t, handler, "POST", one.APIBase+"/webmail-login", nil, 404)
	if service.authenticate(one.Username, login.Password) != nil {
		t.Fatal("deleted account still accepts temporary credentials")
	}
}

func TestWebmailCredentialExpiryTamperingAndRestart(t *testing.T) {
	c := defaultConfig()
	c.Database = filepath.Join(t.TempDir(), "mail.db")
	service, handler := testManager(t, c)
	one := createAccount(t, handler, "persisted-picker")
	account := service.lookup(one.ID)
	now := time.Now().UTC().Truncate(time.Second)
	login, err := account.newWebmailCredentials(now)
	if err != nil {
		t.Fatal(err)
	}
	if !account.validWebmailPassword(login.Password, login.ExpiresAt.Add(-time.Nanosecond)) || account.validWebmailPassword(login.Password, login.ExpiresAt) {
		t.Fatal("credential expiration boundary is incorrect")
	}
	second, err := account.newWebmailCredentials(now)
	if err != nil || login.Password == second.Password {
		t.Fatal("logins must issue distinct credentials", err)
	}
	parts := strings.Split(login.Password, ".")
	parts[2] = "modified"
	for _, password := range []string{"", "mw1.invalid", strings.Join(parts, "."), login.Password + "x", strings.Repeat("x", 10000)} {
		if account.validWebmailPassword(password, now) {
			t.Fatal("malformed or modified credential accepted")
		}
	}
	old, err := account.newWebmailCredentials(now.Add(-2 * time.Hour))
	if err != nil || service.authenticate(one.Username, old.Password) != nil {
		t.Fatal("expired credential accepted", err)
	}
	service.Close()
	reopened, err := openService(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.authenticate(one.Username, login.Password) != nil || reopened.authenticate(one.Username, one.Password) == nil {
		t.Fatal("restart must discard webmail credentials and preserve the test password")
	}
}

func TestWebmailLoginRespectsHTTPAuthAndWebroot(t *testing.T) {
	c := defaultConfig()
	c.HTTPAuth, c.Webroot = "admin:secret", "/mail"
	_, handler := testManager(t, c)
	path := "/mail" + defaultAPI + "/webmail-login"
	apiCall(t, handler, "POST", path, nil, 401)
	request := httptest.NewRequest("POST", path, nil)
	request.SetBasicAuth("admin", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
}
