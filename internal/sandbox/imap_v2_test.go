package sandbox

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestIMAPSASLFaultAndRecovery(t *testing.T) {
	for _, action := range []map[string]any{
		{"type": "reject", "status": "NO", "response_code": "UNAVAILABLE"},
		{"type": "response", "response": "{tag} NO [UNAVAILABLE] unavailable"},
	} {
		t.Run(action["type"].(string), func(t *testing.T) {
			a := testStore(t, defaultConfig())
			f := rule("sasl", "imap", "AUTHENTICATE", "before", action)
			f["max_hits"] = 1
			installFault(t, a, false, f)
			w := imapWire(t, imapAddress(t, a.service), "", "")
			auth := "AUTHENTICATE PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00"+mailboxUsername+"\x00"+mailboxPassword))
			imapReply(t, w, auth, "NO")
			imapReply(t, w, auth, "OK")
			imapReply(t, w, "SELECT Sent", "OK")
			got, _ := a.faults.get("sasl")
			if got.Hits != 1 {
				t.Fatal("SASL rule claimed more than once", got.Hits)
			}
		})
	}
}

func TestIMAPAppendAndCopyCompletionFaults(t *testing.T) {
	a := testStore(t, defaultConfig())
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	for _, command := range []string{"APPEND", "UID COPY"} {
		f := rule(command, "imap", command, "after", map[string]any{"type": "reject", "status": "NO"})
		f["filter"] = map[string]any{"folder": "Sent"}
		f["max_hits"] = 1
		installFault(t, a, false, f)
	}
	raw := testRaw("append@test", "append", "recipient@example.test")
	imapReply(t, w, fmt.Sprintf("APPEND Sent {%d+}\r\n%s", len(raw), raw), "NO")
	if len(a.snapshot()) != 1 {
		t.Fatal("APPEND after fault ran before commit")
	}
	imapReply(t, w, "SELECT Sent", "OK")
	imapReply(t, w, "UID COPY 1 Archive", "NO")
	if len(a.snapshot()) != 2 {
		t.Fatal("COPY after fault ran before commit")
	}
	imapReply(t, w, "UID COPY 1 Archive", "OK")
	if len(a.snapshot()) != 3 {
		t.Fatal("COPY did not recover")
	}
}

func TestIMAPReadOnlyAndFailedSelection(t *testing.T) {
	a := testStore(t, defaultConfig())
	msg := appendTest(t, a, "keep@test")
	apiCall(t, controlHandler(a), "PATCH", defaultAPI+"/messages/"+msg.ID, map[string]any{"flags": []string{`\Deleted`}}, 200)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "EXAMINE Sent", "OK")
	imapReply(t, w, `STORE 1 +FLAGS (\Seen)`, "NO")
	imapReply(t, w, "EXPUNGE", "NO")
	imapReply(t, w, "CLOSE", "OK")
	if a.get(msg.ID) == nil || hasFlag(a.get(msg.ID).Flags, `\Seen`) {
		t.Fatal("read-only command mutated message")
	}
	imapReply(t, w, "SELECT Sent", "OK")
	f := rule("selection", "imap", "SELECT", "before", map[string]any{"type": "reject", "status": "NO"})
	f["filter"] = map[string]any{"folder": "Archive"}
	installFault(t, a, false, f)
	imapReply(t, w, "SELECT Archive", "NO")
	// A failed SELECT deselects the previous mailbox.
	imapReply(t, w, "FETCH 1 FLAGS", "BAD")
	imapReply(t, w, "SELECT Sent", "OK")
	if reply := imapReply(t, w, "ENABLE IMAP4rev2", "OK"); strings.Contains(reply, "ENABLED IMAP4rev2") {
		t.Fatal("enabled an unadvertised protocol", reply)
	}
}

func TestIMAPMIMESectionsAndSearchCriteria(t *testing.T) {
	a := testStore(t, defaultConfig())
	raw := []byte("From: Sender <sender@example.test>\r\nTo: reader@example.test\r\nSubject: =?ISO-8859-1?Q?caf=E9?=\r\nDate: Thu, 02 Jan 2020 00:30:00 +0200\r\nMessage-ID: <mime@test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=parts\r\n\r\n--parts\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9\r\n--parts\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=note.txt\r\nContent-Transfer-Encoding: base64\r\n\r\nYXR0YWNobWVudC10b2tlbg==\r\n--parts--\r\n")
	m, err := a.append(raw, appendOptions{Folder: "Sent"})
	if err != nil {
		t.Fatal(err)
	}
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "SELECT Sent", "OK")
	for _, criteria := range []string{
		`CHARSET UTF-8 SUBJECT "café"`,
		`CHARSET UTF-8 BODY "café"`,
		`BODY "attachment-token"`,
		`TEXT "sender@example.test"`,
		`SENTON 2-Jan-2020`,
		`UID 1:* NOT SEEN`,
		`OR SUBJECT "missing" HEADER Message-ID "mime@test"`,
	} {
		reply := imapReply(t, w, "UID SEARCH "+criteria, "OK")
		if !strings.Contains(reply, "* SEARCH 1\n") {
			t.Fatalf("criteria %s did not match: %s", criteria, reply)
		}
	}
	if reply := imapReply(t, w, "UID SEARCH SENTBEFORE 2-Jan-2020", "OK"); strings.Contains(reply, "* SEARCH 1") {
		t.Fatal("SENT date used UTC rather than header date", reply)
	}
	reply := imapReply(t, w, "UID FETCH 1 (BODYSTRUCTURE BODY.PEEK[1]<0.4>)", "OK")
	if !strings.Contains(reply, "BODYSTRUCTURE") || !strings.Contains(reply, "BODY[1]<0> {4}\n") || !strings.Contains(reply, "caf=") {
		t.Fatal("wrong MIME section", reply)
	}
	if hasFlag(a.get(m.ID).Flags, `\Seen`) {
		t.Fatal("MIME PEEK marked read")
	}
	imapReply(t, w, "UID FETCH 1 RFC822.HEADER", "OK")
	if hasFlag(a.get(m.ID).Flags, `\Seen`) {
		t.Fatal("RFC822.HEADER marked read")
	}
	imapReply(t, w, "UID FETCH 1 RFC822.TEXT", "OK")
	if !hasFlag(a.get(m.ID).Flags, `\Seen`) {
		t.Fatal("RFC822.TEXT did not mark read")
	}
}

func TestIMAPAppendBeforeFaultDoesNotRequestLiteral(t *testing.T) {
	a := testStore(t, defaultConfig())
	f := rule("before-append", "imap", "APPEND", "before", map[string]any{"type": "reject", "status": "NO"})
	f["filter"] = map[string]any{"folder": "Sent"}
	f["max_hits"] = 1
	installFault(t, a, false, f)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	if err := w.PrintfLine("a APPEND Sent {100}"); err != nil {
		t.Fatal(err)
	}
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "a NO ") {
		t.Fatal("APPEND requested data before rejecting", line, err)
	}
	imapReply(t, w, "NOOP", "OK")
	if len(a.snapshot()) != 0 {
		t.Fatal("rejected APPEND stored a message")
	}
}

func TestIMAPListHierarchyAndUnicodeNames(t *testing.T) {
	a := testStore(t, defaultConfig())
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	if reply := imapReply(t, w, `LIST "" ""`, "OK"); !strings.Contains(reply, `* LIST (\Noselect) "/" ""`) {
		t.Fatal("missing hierarchy delimiter", reply)
	}
	// Modified UTF-7 for "Äldre". The server hook receives the decoded name.
	imapReply(t, w, `CREATE "&AMQ-ldre"`, "OK")
	if _, err := a.user.GetMailbox("Äldre"); err != nil {
		t.Fatal("mailbox name was not decoded", err)
	}
	f := rule("unicode", "imap", "SELECT", "before", map[string]any{"type": "reject", "status": "NO"})
	f["filter"] = map[string]any{"folder": "Äldre"}
	installFault(t, a, false, f)
	imapReply(t, w, `SELECT "&AMQ-ldre"`, "NO")
	imapReply(t, w, `SUBSCRIBE "&AMQ-ldre"`, "OK")
	if reply := imapReply(t, w, `LSUB "" "*"`, "OK"); !strings.Contains(reply, "&AMQ-ldre") {
		t.Fatal("missing subscription", reply)
	}
	imapReply(t, w, `UNSUBSCRIBE "&AMQ-ldre"`, "OK")
	if reply := imapReply(t, w, `LSUB "" "*"`, "OK"); strings.Contains(reply, "&AMQ-ldre") {
		t.Fatal("subscription remained", reply)
	}
}
