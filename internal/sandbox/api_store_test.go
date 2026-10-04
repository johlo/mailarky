package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSearchDeletionIsAtomicDuringConcurrentPruning(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxMessages = 3
	a := testStore(t, cfg)
	h := controlHandler(a)
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 80; i++ {
				if _, err := a.append(testRaw(fmt.Sprintf("%d-%d@test", worker, i), "delete-me", "recipient@example.test"), appendOptions{Folder: "Sent"}); err != nil {
					errors <- err
					return
				}
				req := httptest.NewRequest("DELETE", defaultAPI+"/messages?query="+url.QueryEscape("subject:delete-me"), nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != 204 {
					errors <- fmt.Errorf("delete: %d %s", w.Code, w.Body.String())
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}
func TestMIMEPartsAreDecodedOnDemandAndJSONUsesSnakeCase(t *testing.T) {
	a := testStore(t, defaultConfig())
	h := controlHandler(a)
	raw := []byte("From: Sender <sender@example.test>\r\nTo: reader@example.test\r\nSubject: Multipart\r\nMessage-ID: <parts@test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=parts\r\n\r\n--parts\r\nContent-Type: text/plain\r\n\r\nhello\r\n--parts\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.txt\r\nContent-Transfer-Encoding: base64\r\n\r\nYXR0YWNobWVudCBwYXlsb2Fk\r\n--parts--\r\n")
	req := httptest.NewRequest("POST", defaultAPI+"/messages", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "message/rfc822")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var d messageDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 1 || d.Attachments[0].Size != 18 {
		t.Fatal(d)
	}
	data := apiCall(t, h, "GET", defaultAPI+"/messages/"+d.ID+"/parts/"+d.Attachments[0].PartID, nil, 200).Body.String()
	if data != "attachment payload" {
		t.Fatal(data)
	}
	if got := apiCall(t, h, "GET", defaultAPI+"/messages/"+d.ID+"/raw", nil, 200).Body.Bytes(); !bytes.Equal(raw, got) {
		t.Fatal("raw MIME changed")
	}
	for _, path := range []string{defaultAPI + "/messages", defaultAPI + "/messages/" + d.ID, "/api/v1/accounts", "/api/v1/info"} {
		data := apiCall(t, h, "GET", path, nil, 200).Body.Bytes()
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		var check func(any)
		check = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				for key, x := range v {
					if key != strings.ToLower(key) {
						t.Errorf("JSON key is not snake_case: %s", key)
					}
					check(x)
				}
			case []any:
				for _, x := range v {
					check(x)
				}
			}
		}
		check(v)
	}
}
func TestRemovedRoutesAndStrictJSONHaveJSONErrors(t *testing.T) {
	a := testStore(t, defaultConfig())
	h := controlHandler(a)
	for _, path := range []string{"/api/v1/webui", "/api/v1/chaos", "/api/v1/send", "/api/v1/tags", "/api/v1/search", "/messages", "/mailboxes/default/api/v1/messages"} {
		w := apiCall(t, h, "GET", path, nil, 404)
		if w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatal(w.Body.String())
		}
	}
	for _, body := range []string{`{"from":"sender@example.test","to":["reader@example.test"],"unknown":true}`, `{} {}`} {
		r := httptest.NewRequest("POST", defaultAPI+"/messages", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestPersistenceRetentionUIDsAndAge(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database = filepath.Join(t.TempDir(), "mail.db")
	cfg.MaxMessages = 2
	a := testStore(t, cfg)
	one := appendTest(t, a, "one@test")
	two := appendTest(t, a, "two@test")
	three := appendTest(t, a, "three@test")
	validity := a.state.Folders["Sent"].Validity
	if a.get(one.ID) != nil {
		t.Fatal("count retention failed")
	}
	apiCall(t, controlHandler(a), "DELETE", defaultAPI+"/messages/"+two.ID, nil, 204)
	if err := a.service.Close(); err != nil {
		t.Fatal(err)
	}
	a = testStore(t, cfg)
	four := appendTest(t, a, "four@test")
	if four.UID != 4 || a.state.Folders["Sent"].Validity != validity || !bytes.Equal(a.get(three.ID).Raw, three.Raw) {
		t.Fatal("UID or persisted MIME changed")
	}
	a.mu.Lock()
	a.options.MaxAge = time.Nanosecond
	a.mu.Unlock()
	time.Sleep(time.Millisecond)
	if err := a.prune(); err != nil {
		t.Fatal(err)
	}
	if len(a.snapshot()) != 0 {
		t.Fatal("age retention failed")
	}
}
func TestWebhooksWebSocketAndMetrics(t *testing.T) {
	delivered := make(chan messageSummary, 2)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Mailarky-Mailbox") != defaultAccountID {
			t.Error("missing account identity")
		}
		var m messageSummary
		json.NewDecoder(r.Body).Decode(&m)
		delivered <- m
	}))
	defer receiver.Close()
	cfg := defaultConfig()
	cfg.WebhookURL = receiver.URL
	cfg.WebhookLimit = 0
	cfg.EnableMetrics = true
	a := testStore(t, cfg)
	hs := httptest.NewServer(controlHandler(a))
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+defaultAPI+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	for {
		a.notifier.mu.Lock()
		n := len(a.notifier.subscribers)
		a.notifier.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("missing subscription")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	m, err := a.append(testRaw("notify@test", "notify", "recipient@example.test"), appendOptions{Folder: "Sent", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), m.ID) {
		t.Fatal(string(data))
	}
	select {
	case got := <-delivered:
		if got.ID != m.ID {
			t.Fatal(got)
		}
	case <-ctx.Done():
		t.Fatal("webhook retry missing")
	}
	if data := apiCall(t, controlHandler(a), "GET", "/metrics", nil, 200).Body.String(); !strings.Contains(data, `mailarky_messages{account="default"} 1`) {
		t.Fatal(data)
	}
}
func TestReadRawImportOversizeAndNoBodySideEffects(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxSize = 8
	a := testStore(t, cfg)
	r := httptest.NewRequest("POST", defaultAPI+"/messages", strings.NewReader("0123456789"))
	r.Header.Set("Content-Type", "message/rfc822")
	w := httptest.NewRecorder()
	controlHandler(a).ServeHTTP(w, r)
	if w.Code != 400 || len(a.snapshot()) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	io.Copy(io.Discard, w.Body)
}

func TestDuplicateSuppressionDoesNotRepeatNotifications(t *testing.T) {
	cfg := defaultConfig()
	cfg.IgnoreDuplicates = true
	a := testStore(t, cfg)
	var notifications int
	a.afterAppend = func(*storedMessage) { notifications++ }
	first, err := a.append(testRaw("duplicate@test", "first", "recipient@example.test"), appendOptions{Folder: "Sent", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.append(testRaw("duplicate@test", "second", "recipient@example.test"), appendOptions{Folder: "Sent", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || notifications != 1 || a.ignored.Load() != 1 {
		t.Fatal("duplicate append repeated effects", notifications)
	}
}
