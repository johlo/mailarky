package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func importCall(t *testing.T, h http.Handler, query, contentType string, body []byte, status int) importReport {
	t.Helper()
	req := httptest.NewRequest("POST", defaultAPI+"/import"+query, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("import: got %d want %d: %s", w.Code, status, w.Body.String())
	}
	var report importReport
	if status == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
	}
	return report
}

// byFolder indexes messages by folder and Message-ID.
func byFolder(a *Account) map[string]*storedMessage {
	out := map[string]*storedMessage{}
	for _, m := range a.snapshot() {
		out[m.Folder+" "+m.Detail.MessageID] = m
	}
	return out
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

const takeout = "From 1781234567890123456@xxx Thu Oct 01 12:00:00 +0000 2020\n" +
	"X-GM-THRID: 1781234567890123456\n" +
	"X-Gmail-Labels: Inbox,Unread,Category Updates\n" +
	"From: Alice <alice@customer.test>\n" +
	"To: me@example.test\n" +
	"Subject: New\n" +
	"Message-ID: <new@test>\n" +
	"Date: Thu, 01 Oct 2020 11:59:00 +0000\n" +
	"\n" +
	"Unread in the inbox\n" +
	"\n" +
	"From 1781234567890123457@xxx Fri Oct 02 12:00:00 +0000 2020\n" +
	"X-Gmail-Labels: Sent,Opened,Starred,Work/Projects\n" +
	"From: me@example.test\n" +
	"To: alice@customer.test\n" +
	"Subject: Reply\n" +
	"Message-ID: <reply@test>\n" +
	"\n" +
	"Sent and filed\n" +
	"\n" +
	"From 1781234567890123458@xxx Sat Oct 03 12:00:00 +0000 2020\n" +
	"X-Gmail-Labels: Archived,Opened,\"Clients, Big\",=?UTF-8?Q?R=C3=A9sum=C3=A9s?=\n" +
	"From: bob@customer.test\n" +
	"To: me@example.test\n" +
	"Subject: Filed\n" +
	"Message-ID: <filed@test>\n" +
	"\n" +
	"Labelled\n" +
	"\n" +
	"From 1781234567890123459@xxx Sun Oct 04 12:00:00 +0000 2020\n" +
	"X-Gmail-Labels: Archived,Opened\n" +
	"From: carol@customer.test\n" +
	"To: me@example.test\n" +
	"Subject: Archived\n" +
	"Message-ID: <archived@test>\n" +
	"\n" +
	"No folder label\n" +
	"\n" +
	"From 1781234567890123460@xxx Mon Oct 05 12:00:00 +0000 2020\n" +
	"X-Gmail-Labels: Chat\n" +
	"From: dave@customer.test\n" +
	"Subject: Chat with Dave\n" +
	"\n" +
	"hi\n"

func TestImportGmailTakeoutMapsLabelsToFoldersAndFlags(t *testing.T) {
	a := testStore(t, defaultConfig())
	events, unsubscribe := a.notifier.subscribe()
	defer unsubscribe()
	report := importCall(t, controlHandler(a), "?labels=gmail", "application/mbox", gzipped(t, []byte(takeout)), 200)
	want := map[string]int{"INBOX": 1, "Sent": 1, "Work/Projects": 1, "Clients, Big": 1, "Résumés": 1, "Archive": 1}
	if report.Imported != 6 || report.Skipped != 1 || !reflect.DeepEqual(report.Folders, want) {
		t.Fatalf("%+v", report)
	}
	if len(report.Problems) != 1 || report.Problems[0].Source != "message 5" || !strings.Contains(report.Problems[0].Error, "chat") {
		t.Fatalf("%+v", report.Problems)
	}
	got := byFolder(a)
	if m := got["INBOX new@test"]; m == nil || len(m.Flags) != 0 || !m.InternalDate.Equal(time.Date(2020, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", m)
	}
	for _, key := range []string{"Sent reply@test", "Work/Projects reply@test"} {
		if m := got[key]; m == nil || !reflect.DeepEqual(m.Flags, []string{`\Flagged`, `\Seen`}) {
			t.Fatalf("%s: %+v", key, m)
		}
	}
	if m := got["Archive archived@test"]; m == nil || !reflect.DeepEqual(m.Flags, []string{`\Seen`}) {
		t.Fatalf("%+v", m)
	}
	raw := string(got["INBOX new@test"].Raw)
	if strings.Contains(raw, "X-Gmail-Labels") || strings.Contains(raw, "X-GM-THRID") || !strings.HasPrefix(raw, "From: Alice") || !strings.HasSuffix(raw, "\r\n\r\nUnread in the inbox\r\n") {
		t.Fatalf("%q", raw)
	}
	folders := map[string]bool{}
	a.mu.RLock()
	for name, f := range a.state.Folders {
		folders[name] = f.Subscribed
	}
	a.mu.RUnlock()
	if !folders["Work/Projects"] || !folders["Résumés"] {
		t.Fatalf("imported folders are not subscribed: %v", folders)
	}
	select {
	case event := <-events:
		if event.Type != "import" || !reflect.DeepEqual(event.Data, map[string]int{"messages": 6}) {
			t.Fatalf("%+v", event)
		}
	default:
		t.Fatal("missing import event")
	}
	select {
	case event := <-events:
		t.Fatalf("one event per import, got %+v", event)
	default:
	}
}

func TestImportMboxFromThunderbirdAndMboxrd(t *testing.T) {
	a := testStore(t, defaultConfig())
	mbox := "From - Tue Jan  2 03:04:05 2024\r\n" +
		"X-Mozilla-Status: 0003\r\n" +
		"X-Mozilla-Status2: 00000000\r\n" +
		"From: a@test\r\n" +
		"To: b@test\r\n" +
		"Message-ID: <read@test>\r\n" +
		"\r\n" +
		">From the top\r\n" +
		">>From quoted\r\n" +
		"\r\n" +
		"From - Tue Jan  2 03:04:06 2024\r\n" +
		"X-Mozilla-Status: 0009\r\n" +
		"From: a@test\r\n" +
		"Message-ID: <expunged@test>\r\n" +
		"\r\n" +
		"gone\r\n" +
		"From MAILER-DAEMON Wed Jan  3 03:04:05 2024\r\n" +
		"Status: RO\r\n" +
		"X-Status: F\r\n" +
		"From: c@test\r\n" +
		"Message-ID: <adjacent@test>\r\n" +
		"\r\n" +
		"no blank line before me\r\n" +
		"From the body, not a separator\r\n"
	report := importCall(t, controlHandler(a), "?folder=Old/Thunderbird", "application/mbox", []byte(mbox), 200)
	if report.Imported != 2 || report.Skipped != 1 || report.Problems[0].Source != "message 2" {
		t.Fatalf("%+v", report)
	}
	got := byFolder(a)
	read := got["Old/Thunderbird read@test"]
	if read == nil || !reflect.DeepEqual(read.Flags, []string{`\Answered`, `\Seen`}) || !strings.HasSuffix(string(read.Raw), "\r\n\r\nFrom the top\r\n>From quoted\r\n") || strings.Contains(string(read.Raw), "X-Mozilla") {
		t.Fatalf("%+v %q", read, read.Raw)
	}
	adjacent := got["Old/Thunderbird adjacent@test"]
	if adjacent == nil || !reflect.DeepEqual(adjacent.Flags, []string{`\Flagged`, `\Seen`}) || !strings.HasSuffix(string(adjacent.Raw), "no blank line before me\r\nFrom the body, not a separator\r\n") {
		t.Fatalf("%+v", adjacent)
	}
	if strings.Contains(string(adjacent.Raw), "Status:") {
		t.Fatalf("%q", adjacent.Raw)
	}
	if !adjacent.InternalDate.Equal(time.Date(2024, 1, 3, 3, 4, 5, 0, time.UTC)) {
		t.Fatal(adjacent.InternalDate)
	}
}

func TestImportSkipsBadMessagesAndRejectsBadRequests(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxSize = 200
	a := testStore(t, cfg)
	h := controlHandler(a)
	mbox := "From x Tue Jan  2 03:04:05 2024\nFrom: a@test\nMessage-ID: <ok@test>\n\nfine\n\n" +
		"From x Tue Jan  2 03:04:05 2024\nFrom: a@test\nMessage-ID: <big@test>\n\n" + strings.Repeat("x", 300) + "\n\n" +
		"From x Tue Jan  2 03:04:05 2024\nnot a header line\n"
	report := importCall(t, h, "", "application/mbox", []byte(mbox), 200)
	if report.Imported != 1 || report.Skipped != 2 || len(report.Problems) != 2 || !strings.Contains(report.Problems[0].Error, "size") {
		t.Fatalf("%+v", report)
	}
	if report = importCall(t, h, "?folder=inbox", "application/mbox", []byte(mbox), 200); report.Folders["INBOX"] != 1 {
		t.Fatalf("folder names must treat INBOX case-insensitively: %+v", report)
	}
	before := len(a.snapshot())
	importCall(t, h, "", "application/mbox", []byte("Subject: not an mbox\n"), 400)
	importCall(t, h, "?labels=outlook", "application/mbox", []byte(mbox), 400)
	importCall(t, h, "?folder=INBOX", "application/x-tar", nil, 400)
	importCall(t, h, "", "message/rfc822", []byte(mbox), 415)
	importCall(t, h, "", "application/x-tar", []byte("this is not a tar archive, but it is long enough to read a header block from it"+strings.Repeat(".", 512)), 400)
	if after := len(a.snapshot()); after != before {
		t.Fatalf("rejected imports stored messages: %d -> %d", before, after)
	}
}

func TestImportIsAllOrNothingWithinQuota(t *testing.T) {
	cfg := defaultConfig()
	cfg.AccountLimits.QuotaMessages = 3
	a := testStore(t, cfg)
	appendTest(t, a, "existing@test")
	var mbox strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&mbox, "From x Tue Jan  2 03:04:05 2024\nFrom: a@test\nMessage-ID: <%d@test>\n\nbody\n\n", i)
	}
	importCall(t, controlHandler(a), "", "application/mbox", []byte(mbox.String()), 507)
	if n := len(a.snapshot()); n != 1 {
		t.Fatalf("partial import: %d messages", n)
	}
}

func TestImportGmailCopiesRespectDuplicateSuppression(t *testing.T) {
	cfg := defaultConfig()
	cfg.IgnoreDuplicates = true
	a := testStore(t, cfg)
	report := importCall(t, controlHandler(a), "?labels=gmail", "application/mbox", []byte(takeout), 200)
	// reply@test is in Sent and Work/Projects, filed@test in two labels.
	if report.Imported != 4 || report.Duplicates != 2 {
		t.Fatalf("%+v", report)
	}
}

func maildirTar(t *testing.T, files map[string]time.Time) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for name, modified := range files {
		id := strings.NewReplacer("/", "-", ":", "-", ",", "-").Replace(name)
		body := []byte("From: a@test\nTo: b@test\nMessage-ID: <" + id + ">\n\nbody\n")
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), ModTime: modified, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestImportMaildirLayouts(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database = filepath.Join(t.TempDir(), "mail.db")
	service, h := testManager(t, cfg)
	day := func(d int) time.Time { return time.Date(2023, 5, d, 0, 0, 0, 0, time.UTC) }
	archive := maildirTar(t, map[string]time.Time{
		"./cur/1700000002.M1P1.host:2,S":           day(2),
		"./cur/1700000001.M1P1.host:2,FS":          day(1),
		"./new/1700000003.M1P1.host":               day(3),
		"./tmp/1700000004.M1P1.host":               day(4),
		"./dovecot-uidlist":                        day(4),
		"./.Sent/cur/1700000005.M1P1.host:2,RS":    day(5),
		"./.Work.Projects/cur/1700000006.host;2,T": day(6),
		"./Archive/2020/cur/1700000007.host:2,D":   day(7),
	})
	report := importCall(t, h, "", "application/x-tar", gzipped(t, archive), 200)
	want := map[string]int{"INBOX": 3, "Sent": 1, "Work/Projects": 1, "Archive/2020": 1}
	if report.Imported != 6 || report.Skipped != 0 || !reflect.DeepEqual(report.Folders, want) {
		t.Fatalf("%+v", report)
	}
	check := func(a *Account) {
		t.Helper()
		got := map[string]*storedMessage{}
		for _, m := range a.snapshot() {
			got[m.Detail.MessageID] = m
		}
		for id, flags := range map[string][]string{
			"-cur-1700000001.M1P1.host-2-FS":          {`\Flagged`, `\Seen`},
			"-cur-1700000002.M1P1.host-2-S":           {`\Seen`},
			"-new-1700000003.M1P1.host":               {},
			"-.Sent-cur-1700000005.M1P1.host-2-RS":    {`\Answered`, `\Seen`},
			"-.Work.Projects-cur-1700000006.host;2-T": {`\Deleted`},
			"-Archive-2020-cur-1700000007.host-2-D":   {`\Draft`},
		} {
			m := got["."+id]
			if m == nil || !reflect.DeepEqual(append([]string{}, m.Flags...), flags) {
				t.Fatalf("%s: %+v", id, m)
			}
		}
		if got[".-cur-1700000001.M1P1.host-2-FS"].UID != 1 || got[".-cur-1700000002.M1P1.host-2-S"].UID != 2 || got[".-new-1700000003.M1P1.host"].UID != 3 {
			t.Fatal("UIDs must follow delivery time")
		}
		if raw := string(got[".-new-1700000003.M1P1.host"].Raw); !strings.HasSuffix(raw, "\r\n\r\nbody\r\n") {
			t.Fatalf("%q", raw)
		}
	}
	check(service.lookup(defaultAccountID))
	service.Close()
	reopened, err := openService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened.lookup(defaultAccountID))
}

func TestImportedMailIsVisibleOverIMAP(t *testing.T) {
	a := testStore(t, defaultConfig())
	importCall(t, controlHandler(a), "?labels=gmail", "application/mbox", []byte(takeout), 200)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	if list := imapReply(t, w, `LSUB "" "*"`, "OK"); !strings.Contains(list, `"Work/Projects"`) {
		t.Fatal(list)
	}
	imapReply(t, w, `SELECT "Work/Projects"`, "OK")
	fetch := imapReply(t, w, "FETCH 1 (FLAGS INTERNALDATE)", "OK")
	if !strings.Contains(fetch, `\Flagged`) || !strings.Contains(fetch, `\Seen`) || !strings.Contains(fetch, " 2-Oct-2020 12:00:00 +0000") {
		t.Fatal(fetch)
	}
}
