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
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestImportMboxDiscardsOversizedLines(t *testing.T) {
	const limit = 1024
	// Exceed both the message limit and the line reader's buffer, then ensure
	// the next message is still found. Measure only the importer's allocations.
	input := "From x Tue Jan  2 03:04:05 2024\nFrom: a@test\n\n" +
		strings.Repeat("x", 32<<20) + "\n\n" +
		"From x Tue Jan  2 03:04:05 2024\nFrom: a@test\nMessage-ID: <after@test>\n\nbody\n\n"
	im := &importer{maxSize: limit}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := im.readMbox(strings.NewReader(input), "INBOX", false)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if im.report.Skipped != 1 || len(im.items) != 1 || im.items[0].Parsed.detail.MessageID != "after@test" {
		t.Fatalf("skipped=%d items=%+v", im.report.Skipped, im.items)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("allocated %d bytes to skip a line with a %d-byte message limit", allocated, limit)
	}
}

func TestImportMboxLongLineBoundaries(t *testing.T) {
	header := "From: a@test\r\nMessage-ID: <long@test>\r\n\r\n"
	body := "From " + strings.Repeat("x", 64<<10)
	want := header + body + "\r\n"
	for _, ending := range []string{"\n", "\r\n", ""} {
		for _, limit := range []int64{int64(len(want)), int64(len(want)) - 1, 1024} {
			t.Run(fmt.Sprintf("ending=%q/limit=%d", ending, limit), func(t *testing.T) {
				input := "From x Tue Jan  2 03:04:05 2024\r\n" + header + ">" + body + ending
				im := &importer{maxSize: limit}
				if err := im.readMbox(strings.NewReader(input), "INBOX", false); err != nil {
					t.Fatal(err)
				}
				if limit < int64(len(want)) {
					if im.report.Skipped != 1 || len(im.items) != 0 {
						t.Fatalf("oversized message: skipped=%d items=%d", im.report.Skipped, len(im.items))
					}
				} else if im.report.Skipped != 0 || len(im.items) != 1 || string(im.items[0].Raw) != want {
					t.Fatalf("valid long line was skipped or changed: skipped=%d items=%d", im.report.Skipped, len(im.items))
				}
			})
		}
	}
}

func TestImportRejectsCorruptGzipBeforeCommit(t *testing.T) {
	archive := maildirTar(t, map[string]time.Time{"Work/cur/message:2,S": time.Unix(1700000000, 0)})
	// Tar writers can pad beyond the two end-of-archive blocks. The gzip
	// checksum must be validated even though tar stops before this padding.
	archive = append(archive, make([]byte, 8192)...)
	for contentType, source := range map[string][]byte{
		"application/x-tar": archive,
		"application/mbox":  []byte(takeout),
	} {
		compressed := gzipped(t, source)
		corrupt := append([]byte(nil), compressed...)
		corrupt[len(corrupt)-8] ^= 1
		for name, body := range map[string][]byte{
			"bad checksum":   corrupt,
			"missing footer": compressed[:len(compressed)-8],
		} {
			t.Run(contentType+"/"+name, func(t *testing.T) {
				a := testStore(t, defaultConfig())
				existing := appendTest(t, a, "existing@test")
				events, unsubscribe := a.notifier.subscribe()
				defer unsubscribe()
				importCall(t, controlHandler(a), "", contentType, body, 400)
				if got := a.snapshot(); len(got) != 1 || got[0].ID != existing.ID {
					t.Fatalf("corrupt upload changed stored messages: %+v", got)
				}
				a.mu.RLock()
				_, created := a.state.Folders["Work"]
				a.mu.RUnlock()
				if created {
					t.Fatal("corrupt upload created a folder")
				}
				select {
				case event := <-events:
					t.Fatalf("corrupt upload emitted an event: %+v", event)
				default:
				}
			})
		}
	}
}

func TestImportRetentionSendsOneEvent(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxMessages = 128
	a := testStore(t, cfg)
	mbox := func(prefix string) []byte {
		var out strings.Builder
		for i := 0; i < cfg.MaxMessages; i++ {
			fmt.Fprintf(&out, "From x Tue Jan  2 03:04:05 2024\nFrom: a@test\nMessage-ID: <%s-%d@test>\n\nbody\n\n", prefix, i)
		}
		return []byte(out.String())
	}
	importCall(t, controlHandler(a), "", "application/mbox", mbox("old"), 200)
	events, unsubscribe := a.notifier.subscribe()
	defer unsubscribe()
	importCall(t, controlHandler(a), "", "application/mbox", mbox("new"), 200)
	want := map[string]int{"messages": 128, "deleted": 128}
	select {
	case event, ok := <-events:
		if !ok || event.Type != "import" || !reflect.DeepEqual(event.Data, want) {
			t.Fatalf("expected one import event, got %+v (open=%v)", event, ok)
		}
	default:
		t.Fatal("missing import event")
	}
	select {
	case event, ok := <-events:
		t.Fatalf("unexpected extra event or subscriber closure: %+v (open=%v)", event, ok)
	default:
	}
	if deleted := a.deleted.Load(); deleted != 128 {
		t.Fatalf("deleted metric=%d, want 128", deleted)
	}
	// The subscription must still deliver ordinary notifications afterwards.
	appendTest(t, a, "following@test")
	select {
	case event, ok := <-events:
		if !ok || event.Type != "new" {
			t.Fatalf("subscriber did not survive import: %+v (open=%v)", event, ok)
		}
	default:
		t.Fatal("missing subsequent message event")
	}
}

func TestImportRetentionWithoutNewMessages(t *testing.T) {
	a := testStore(t, defaultConfig())
	existing := appendTest(t, a, "old@test")
	// Enable age retention without its periodic worker so only the import
	// can prune the expired message in this test.
	a.options.MaxAge = time.Hour
	if err := a.mutate(func(state *storeState) error {
		old := cloneRecord(state.Messages[existing.ID])
		old.Created = time.Now().Add(-2 * time.Hour)
		state.Messages[old.ID] = old
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := a.notifier.subscribe()
	defer unsubscribe()
	importCall(t, controlHandler(a), "", "application/mbox", nil, 200)
	select {
	case event := <-events:
		if event.Type != "import" || !reflect.DeepEqual(event.Data, map[string]int{"messages": 0, "deleted": 1}) {
			t.Fatalf("unexpected event: %+v", event)
		}
	default:
		t.Fatal("missing retention-only import event")
	}
	if len(a.snapshot()) != 0 || a.deleted.Load() != 1 {
		t.Fatal("expired message was not removed and counted")
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

// The fixtures come from Python's mailbox module (scripts/generate_import_fixtures.py),
// an mbox and Maildir writer independent of this importer.
func TestImportPythonWrittenFixtures(t *testing.T) {
	a := testStore(t, defaultConfig())
	h := controlHandler(a)
	mbox, err := os.ReadFile("../../testdata/import/python.mbox")
	if err != nil {
		t.Fatal(err)
	}
	if report := importCall(t, h, "?folder=Python", "application/mbox", mbox, 200); report.Imported != 4 || report.Skipped != 0 {
		t.Fatalf("%+v", report)
	}
	archive, err := os.ReadFile("../../testdata/import/python-maildir.tar")
	if err != nil {
		t.Fatal(err)
	}
	report := importCall(t, h, "", "application/x-tar", archive, 200)
	if want := map[string]int{"INBOX": 2, "Work/Projects": 2}; report.Imported != 4 || !reflect.DeepEqual(report.Folders, want) {
		t.Fatalf("%+v", report)
	}
	got := map[string]*storedMessage{}
	for _, m := range a.snapshot() {
		got[m.Detail.MessageID] = m
	}
	for id, want := range map[string]struct {
		folder, subject string
		flags           []string
	}{
		"python-1@example.test":  {"Python", "Read", []string{`\Seen`}},
		"python-2@example.test":  {"Python", "Quoted", []string{}},
		"python-3@example.test":  {"Python", "Flagged reply", []string{`\Answered`, `\Flagged`, `\Seen`}},
		"python-4@example.test":  {"Python", "Grüße", []string{`\Seen`}},
		"python-10@example.test": {"INBOX", "Seen and flagged", []string{`\Flagged`, `\Seen`}},
		"python-11@example.test": {"INBOX", "Unread", []string{}},
		"python-12@example.test": {"Work/Projects", "Replied in a subfolder", []string{`\Answered`, `\Seen`}},
		"python-13@example.test": {"Work/Projects", "Trashed", []string{`\Deleted`}},
	} {
		m := got[id]
		if m == nil || m.Folder != want.folder || m.Detail.Subject != want.subject || !reflect.DeepEqual(append([]string{}, m.Flags...), want.flags) {
			t.Fatalf("%s: got %+v, want %+v", id, m, want)
		}
		if bytes.Contains(bytes.ReplaceAll(m.Raw, []byte("\r\n"), nil), []byte("\n")) {
			t.Fatalf("%s: IMAP needs CRLF line endings: %q", id, m.Raw)
		}
		if m.Detail.From.Name != "Alice Ångström" || strings.Contains(string(m.Raw), "\r\nStatus:") {
			t.Fatalf("%s: %+v %q", id, m.Detail.From, m.Raw)
		}
	}
	if m := got["python-1@example.test"]; !m.InternalDate.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatal(m.InternalDate)
	}
	// The archive's file times order the Maildir import.
	if got["python-10@example.test"].UID != 1 || got["python-11@example.test"].UID != 2 || got["python-13@example.test"].UID != 2 {
		t.Fatal("Maildir UIDs must follow file modification times")
	}
	// Python writes mboxo, which quotes only "From " lines; the importer reads
	// mboxrd, so a line that already began with ">From " loses one ">". The
	// two variants cannot be told apart from the file.
	if text := got["python-2@example.test"].Detail.Text; text != "From the start\r\nFrom already quoted\r\nend\r\n" {
		t.Fatalf("%q", text)
	}
	attachments := got["python-4@example.test"].Detail.Attachments
	if len(attachments) != 1 || attachments[0].FileName != "report.bin" {
		t.Fatalf("%+v", attachments)
	}
	data, err := readMIMEPart(got["python-4@example.test"].Raw, attachments[0].PartID)
	if err != nil || len(data) != 256 || data[0] != 0 || data[255] != 255 {
		t.Fatalf("attachment bytes changed: %d %v", len(data), err)
	}
}
