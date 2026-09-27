package emulator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

func TestSMTPEnvelopeStageToxicIsScoped(t *testing.T) {
	b := testStore(t, defaultConfig())
	addr := smtpAddress(t, b)
	if err := b.toxics.put(toxic{Name: "recipient-a", Type: "smtp_reject", Enabled: true, Selector: messageSelector{To: "a@example.test"}, Attributes: toxicAttributes{Stage: "recipient", Code: 550}}, true); err != nil {
		t.Fatal(err)
	}
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Mail("sender@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("a@example.test"); err == nil {
		t.Fatal("toxic failed")
	}
	if err := c.Rcpt("b@example.test"); err != nil {
		t.Fatal("rejected recipient contaminated next RCPT", err)
	}
	writer, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	writer.Write(testRaw("recipient@test", "b", "b@example.test"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if len(b.snapshot()) != 1 || b.snapshot()[0].EnvelopeTo[0] != "b@example.test" {
		t.Fatal("envelope is wrong")
	}
}

func TestIMAPFoldersCopyExpungeAndStableUIDs(t *testing.T) {
	b := testStore(t, defaultConfig())
	first := appendTest(t, b, "first@test")
	second := appendTest(t, b, "second@test")
	addr := imapAddress(t, b)
	c := imapClient(t, addr)
	if err := c.Create("Parent"); err != nil {
		t.Fatal(err)
	}
	if err := c.Create("Parent/Child"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename("Parent", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.user.GetMailbox("Other/Child"); err != nil {
		t.Fatal("child rename failed", err)
	}
	set := new(imap.SeqSet)
	set.AddNum(first.UID)
	if err := c.UidCopy(set, "Other/Child"); err != nil {
		t.Fatal(err)
	}
	if err := c.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
		t.Fatal(err)
	}
	removed := make(chan uint32, 2)
	if err := c.Expunge(removed); err != nil {
		t.Fatal(err)
	}
	if <-removed != 1 {
		t.Fatal("wrong expunge sequence")
	}
	if b.get(first.ID) != nil {
		t.Fatal("expunge did not remove")
	}
	ids, err := c.UidSearch(imap.NewSearchCriteria())
	if err != nil || len(ids) != 1 || ids[0] != second.UID {
		t.Fatalf("remaining UID %v %v", ids, err)
	}
	third := appendTest(t, b, "third@test")
	if third.UID != 3 {
		t.Fatal("UID reused")
	}
	if err := c.Delete("Other/Child"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("INBOX"); err == nil {
		t.Fatal("INBOX deleted")
	}
}

func TestThumbnailAndSpamAssassin(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 320))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	png.Encode(&encoded, img)
	b := testStore(t, defaultConfig())
	raw, _, _, err := (sendRequest{From: sendAddress{Email: "sender@example.test"}, To: []sendAddress{{Email: "a@example.test"}}, Text: "test", Attachments: []sendAttachment{{Filename: "test.png", ContentType: "image/png", Content: base64.StdEncoding.EncodeToString(encoded.Bytes())}}}).mimeMessage()
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.append(raw, appendOptions{Folder: "Sent"})
	if err != nil {
		t.Fatal(err)
	}
	response := apiCall(t, controlHandler(b), "GET", "/api/v1/message/"+m.ID+"/part/"+m.Detail.Attachments[0].PartID+"/thumb", nil, 200)
	config, err := png.DecodeConfig(response.Body)
	if err != nil || config.Width != 320 || config.Height != 160 {
		t.Fatalf("thumbnail %+v %v", config, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan []byte, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		r := bufio.NewReader(conn)
		line, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "REPORT SPAMC/") {
			serverErr <- fmt.Errorf("bad command %s %v", line, err)
			return
		}
		length := 0
		for {
			line, err = r.ReadString('\n')
			if err != nil {
				serverErr <- err
				return
			}
			if line == "\r\n" {
				break
			}
			fmt.Sscanf(line, "Content-length: %d", &length)
		}
		body := make([]byte, length)
		_, err = io.ReadFull(r, body)
		if err != nil {
			serverErr <- err
			return
		}
		received <- body
		fmt.Fprint(conn, "SPAMD/1.5 0 EX_OK\r\nSpam: True ; 6.5 / 5.0\r\n\r\n6.5 TEST_RULE Synthetic spam rule\r\n")
		serverErr <- nil
	}()
	result, err := checkSpam(context.Background(), listener.Addr().String(), m.Raw)
	if err != nil || !result.IsSpam || result.Score != 6.5 || len(result.Rules) != 1 {
		t.Fatalf("spam %+v %v", result, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-received, m.Raw) {
		t.Fatal("spamd received different MIME")
	}
}

func TestToxicSelectorsComposeAndCounterSurvivesUpdate(t *testing.T) {
	b := testStore(t, defaultConfig())
	m := appendTest(t, b, "scoped@test")
	h := controlHandler(b)
	body := toxic{Name: "scope", Type: "imap_hide", Enabled: true, MaxHits: 2, Selector: messageSelector{MessageID: "scoped@test", To: "recipient@example.test", Headers: map[string]string{"x-test-id": "scoped@test"}, Folder: "Sent"}}
	apiCall(t, h, "POST", "/api/v1/toxics", body, 201)
	if len(b.toxics.claim("imap", "", m)) != 1 {
		t.Fatal("conjunctive selector failed")
	}
	body.Enabled = false
	body.Hits = 999
	apiCall(t, h, "PUT", "/api/v1/toxics/scope", body, 200)
	var state toxic
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/toxics/scope", nil, 200).Body.Bytes(), &state)
	if state.Hits != 1 || state.Enabled {
		t.Fatal(state)
	}
	copy := cloneRecord(m)
	copy.Folder = "Archive"
	if body.Selector.matches(copy) {
		t.Fatal("folder condition ignored")
	}
}

func TestTagExpressionsAuthenticationAliasesAndZeroProbability(t *testing.T) {
	t.Setenv("MP_TAG", `"Header match"='"X-Test-ID: tagging"' Patient=to:recipient@example.test`)
	t.Setenv("MP_SMTP_AUTH", "demo:password")
	t.Setenv("MP_SMTP_AUTH_ACCEPT_ANY", "true")
	t.Setenv("MP_SMTP_AUTH_ALLOW_INSECURE", "true")
	c, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	b := testStore(t, c)
	addr := smtpAddress(t, b)
	// Accept-any must allow a client that does not send AUTH, even with users configured.
	if err := smtp.SendMail(addr, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("tags@test", "tagging", "recipient@example.test")); err != nil {
		t.Fatal(err)
	}
	stored := b.snapshot()[0]
	if !containsString(stored.Tags, "Header match") || !containsString(stored.Tags, "Patient") {
		t.Fatal(stored.Tags)
	}
	zero := 0.0
	if err := b.toxics.put(toxic{Name: "never", Type: "imap_hide", Enabled: true, Selector: messageSelector{MessageID: "tags@test"}, Probability: &zero}, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if len(b.toxics.claim("imap", "", stored)) != 0 {
			t.Fatal("zero-probability toxic fired")
		}
	}
	if b.toxics.list()[0].Hits != 0 {
		t.Fatal("unselected toxic consumed hit")
	}
	for _, selector := range []messageSelector{{MessageID: "<>"}, {MessageID: " "}, {From: ""}, {To: "not-an-email"}, {Headers: map[string]string{"bad header": "value"}}} {
		if err := validateToxic(toxic{Name: "bad", Type: "imap_hide", Selector: selector}); err == nil {
			t.Fatalf("accepted selector %+v", selector)
		}
	}
	t.Setenv("MP_SMTP_REQUIRE_TLS", "true")
	c, err = loadConfig(nil)
	if err != nil || c.SMTPTLS != "tls" {
		t.Fatalf("implicit TLS alias %+v %v", c, err)
	}
}
