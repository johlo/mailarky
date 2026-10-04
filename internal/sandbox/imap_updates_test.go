package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johlo/go-imap/v2"
	bolt "go.etcd.io/bbolt"
)

func TestSelectedSequenceNumbersSurviveExternalDeletion(t *testing.T) {
	a := testStore(t, defaultConfig())
	first := appendTest(t, a, "first@test")
	second := appendTest(t, a, "second@test")
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "SELECT Sent", "OK")
	apiCall(t, controlHandler(a), "DELETE", defaultAPI+"/messages/"+first.ID, nil, 204)
	response := imapReply(t, w, "FETCH 2 (UID FLAGS)", "OK")
	if strings.Contains(response, "EXPUNGE") || !strings.Contains(response, "* 2 FETCH") || !strings.Contains(response, fmt.Sprintf("UID %d", second.UID)) {
		t.Fatal("sequence changed before EXPUNGE", response)
	}
	imapReply(t, w, `STORE 2 +FLAGS.SILENT (\Flagged)`, "OK")
	if !hasFlag(a.get(second.ID).Flags, `\Flagged`) {
		t.Fatal("STORE affected wrong message")
	}
	response = imapReply(t, w, "NOOP", "OK")
	if strings.Count(response, "* 1 EXPUNGE") != 1 {
		t.Fatal("missing external EXPUNGE", response)
	}
	response = imapReply(t, w, "FETCH 1 (UID)", "OK")
	if !strings.Contains(response, fmt.Sprintf("UID %d", second.UID)) {
		t.Fatal("remaining sequence incorrect", response)
	}
}

func TestBodyFetchBatchesSeenCommit(t *testing.T) {
	for _, command := range []string{"FETCH", "UID FETCH"} {
		t.Run(command, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Database = filepath.Join(t.TempDir(), "mail.db")
			a := testStore(t, cfg)
			var messages []*storedMessage
			for i := 0; i < 8; i++ {
				messages = append(messages, appendTest(t, a, fmt.Sprintf("batch-%d@test", i)))
			}
			w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
			imapReply(t, w, "SELECT Sent", "OK")
			txID := func() int {
				var id int
				if err := a.db.View(func(tx *bolt.Tx) error { id = tx.ID(); return nil }); err != nil {
					t.Fatal(err)
				}
				return id
			}
			before := txID()
			imapReply(t, w, command+" 1:* BODY.PEEK[]", "OK")
			if txID() != before {
				t.Fatal("PEEK wrote to the store")
			}
			response := imapReply(t, w, command+" 1:* BODY[]", "OK")
			if commits := txID() - before; commits != 1 {
				t.Fatalf("body fetch used %d commits, want 1", commits)
			}
			if strings.Count(response, " FETCH (") != len(messages) || strings.Count(response, `FLAGS (\Seen)`) != len(messages) {
				t.Fatal("missing bodies or implicit flag replies", response)
			}
			for _, msg := range messages {
				if !hasFlag(a.get(msg.ID).Flags, string(imap.FlagSeen)) {
					t.Fatal("missing persisted read flag", msg.ID)
				}
			}
			if response := imapReply(t, w, "NOOP", "OK"); strings.Contains(response, " FETCH (") {
				t.Fatal("read flags reported twice", response)
			}
		})
	}
}

type flagUpdateRecorder struct{ messages []flagUpdate }
type flagUpdate struct {
	SeqNum uint32
	Flags  []imap.Flag
}

func (c *flagUpdateRecorder) WriteExpunge(uint32) error     { return nil }
func (c *flagUpdateRecorder) WriteNumMessages(uint32) error { return nil }
func (c *flagUpdateRecorder) WriteMessageFlags(seq uint32, _ imap.UID, flags []imap.Flag) error {
	c.messages = append(c.messages, flagUpdate{seq, flags})
	return nil
}

func TestSilentStoreReportsExternalChangesWithNoNetFlagDifference(t *testing.T) {
	for _, timing := range []string{"before", "after"} {
		t.Run(timing, func(t *testing.T) {
			a := testStore(t, defaultConfig())
			record := appendTest(t, a, "flags@test")
			b, err := a.user.GetMailbox("Sent")
			if err != nil {
				t.Fatal(err)
			}
			m := b
			if _, err := m.Status(); err != nil {
				t.Fatal(err)
			}
			wire := &flagUpdateRecorder{}
			m.active = true
			set := imap.SeqSetNum(1)
			op := imap.StoreFlagsAdd
			if timing == "before" {
				apiCall(t, controlHandler(a), "PATCH", defaultAPI+"/messages/"+record.ID, map[string]any{"flags": []string{string(imap.FlagSeen)}}, 200)
				op = imap.StoreFlagsDel
			}
			if err := m.UpdateMessagesFlags(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}); err != nil {
				t.Fatal(err)
			}
			if timing == "after" {
				apiCall(t, controlHandler(a), "PATCH", defaultAPI+"/messages/"+record.ID, map[string]any{"flags": []string{}}, 200)
			}
			if err := m.poll(wire, true); err != nil {
				t.Fatal(err)
			}
			if len(wire.messages) != 1 || wire.messages[0].SeqNum != 1 || len(wire.messages[0].Flags) != 0 {
				t.Fatal("missing external change back to empty flags", wire.messages)
			}
			if err := m.poll(wire, true); err != nil || len(wire.messages) != 1 {
				t.Fatal("external change reported twice", err)
			}
		})
	}
}

func TestBodyFetchFailedCommitDoesNotPublishSeen(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database = filepath.Join(t.TempDir(), "mail.db")
	a := testStore(t, cfg)
	record := appendTest(t, a, "unread@test")
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "SELECT Sent", "OK")
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	response := imapReply(t, w, "FETCH 1 BODY[]", "NO")
	if strings.Contains(response, " FETCH (") || hasFlag(a.get(record.ID).Flags, string(imap.FlagSeen)) {
		t.Fatal("failed commit exposed read flags or body", response)
	}
	imapReply(t, w, "NOOP", "OK")
}

func TestBodyFetchFaultOnlyMarksReturnedMessagesSeen(t *testing.T) {
	for _, action := range []map[string]any{
		{"type": "reject", "status": "NO"},
		{"type": "response", "response": "{tag} NO interrupted"},
		{"type": "hide"},
	} {
		t.Run(action["type"].(string), func(t *testing.T) {
			a := testStore(t, defaultConfig())
			first := appendTest(t, a, "first@test")
			second := appendTest(t, a, "second@test")
			third := appendTest(t, a, "third@test")
			f := rule("second", "imap", "FETCH", "content", action)
			f["filter"] = map[string]any{"message": map[string]string{"message_id": "second@test"}}
			installFault(t, a, false, f)
			w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
			imapReply(t, w, "SELECT Sent", "OK")
			status := "NO"
			hidden := action["type"] == "hide"
			if hidden {
				status = "OK"
			}
			response := imapReply(t, w, "FETCH 1:* BODY[]", status)
			if !strings.Contains(response, "* 1 FETCH") || strings.Contains(response, "* 2 FETCH") || strings.Contains(response, "* 3 FETCH") != hidden {
				t.Fatal("wrong messages returned", response)
			}
			if !hasFlag(a.get(first.ID).Flags, string(imap.FlagSeen)) || hasFlag(a.get(second.ID).Flags, string(imap.FlagSeen)) || hasFlag(a.get(third.ID).Flags, string(imap.FlagSeen)) != hidden {
				t.Fatal("wrong messages marked seen")
			}
		})
	}
}
func TestExternalFlagsAndRetentionReachSelectedClients(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxMessages = 2
	a := testStore(t, cfg)
	first := appendTest(t, a, "first@test")
	second := appendTest(t, a, "second@test")
	addr := imapAddress(t, a.service)
	one := imapWire(t, addr, mailboxUsername, mailboxPassword)
	two := imapWire(t, addr, mailboxUsername, mailboxPassword)
	imapReply(t, one, "SELECT Sent", "OK")
	imapReply(t, two, "SELECT Sent", "OK")
	imapReply(t, two, `STORE 2 +FLAGS.SILENT (\Seen)`, "OK")
	response := imapReply(t, one, "NOOP", "OK")
	if !strings.Contains(response, "* 2 FETCH") || !strings.Contains(response, `\Seen`) {
		t.Fatal("other session flag update missing", response)
	}
	appendTest(t, a, "third@test")
	if a.get(first.ID) != nil || a.get(second.ID) == nil {
		t.Fatal("retention malfunction")
	}
	response = imapReply(t, one, "NOOP", "OK")
	if !strings.Contains(response, "* 1 EXPUNGE") || !strings.Contains(response, "* 2 EXISTS") {
		t.Fatal("retention updates missing", response)
	}
}
func TestIDLEPublishesExternalExistsExpungeAndFlags(t *testing.T) {
	a := testStore(t, defaultConfig())
	first := appendTest(t, a, "first@test")
	h := controlHandler(a)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "SELECT Sent", "OK")
	if err := w.PrintfLine("a IDLE"); err != nil {
		t.Fatal(err)
	}
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "+") {
		t.Fatal(line, err)
	}
	appendTest(t, a, "second@test")
	if line, err := w.ReadLine(); err != nil || line != "* 2 EXISTS" {
		t.Fatal("IDLE missing EXISTS", line, err)
	}
	apiCall(t, h, "PATCH", defaultAPI+"/messages/"+first.ID, map[string]any{"flags": []string{`\Seen`}}, 200)
	if line, err := w.ReadLine(); err != nil || !strings.Contains(line, "* 1 FETCH") || !strings.Contains(line, `\Seen`) {
		t.Fatal("IDLE missing flags", line, err)
	}
	apiCall(t, h, "DELETE", defaultAPI+"/messages/"+first.ID, nil, 204)
	if line, err := w.ReadLine(); err != nil || line != "* 1 EXPUNGE" {
		t.Fatal("IDLE missing EXPUNGE", line, err)
	}
	w.PrintfLine("DONE")
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "a OK") {
		t.Fatal(line, err)
	}
}
func TestExamineCloseDoesNotExpunge(t *testing.T) {
	a := testStore(t, defaultConfig())
	m := appendTest(t, a, "keep@test")
	apiCall(t, controlHandler(a), "PATCH", defaultAPI+"/messages/"+m.ID, map[string]any{"flags": []string{`\Deleted`}}, 200)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "EXAMINE Sent", "OK")
	imapReply(t, w, "CLOSE", "OK")
	if a.get(m.ID) == nil {
		t.Fatal("CLOSE expunged in a read-only session")
	}
}

func TestSilentStoreReportsConcurrentExternalFlags(t *testing.T) {
	for _, command := range []string{"STORE", "UID STORE"} {
		for _, target := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/external_%d", command, target), func(t *testing.T) {
				a := testStore(t, defaultConfig())
				appendTest(t, a, "first@test")
				appendTest(t, a, "second@test")
				addr := imapAddress(t, a.service)
				one := imapWire(t, addr, mailboxUsername, mailboxPassword)
				two := imapWire(t, addr, mailboxUsername, mailboxPassword)
				imapReply(t, one, "SELECT Sent", "OK")
				imapReply(t, two, "SELECT Sent", "OK")
				f := rule("pause-store", "imap", command, "before", map[string]any{"type": "delay", "delay_ms": 250})
				f["max_hits"] = 1
				installFault(t, a, false, f)
				if err := one.PrintfLine("a %s 1 +FLAGS.SILENT (\\Flagged)", command); err != nil {
					t.Fatal(err)
				}
				awaitFault(t, a.faults, "pause-store", 1)
				imapReply(t, two, fmt.Sprintf(`STORE %d +FLAGS.SILENT (\Seen)`, target), "OK")
				line, err := one.ReadLine()
				if err != nil || !strings.HasPrefix(line, fmt.Sprintf("* %d FETCH (", target)) || !strings.Contains(line, `\Seen`) {
					t.Fatal("SILENT swallowed external flags", line, err)
				}
				if line, err = one.ReadLine(); err != nil || !strings.HasPrefix(line, "a OK ") {
					t.Fatal("unexpected self update or completion", line, err)
				}
				if response := imapReply(t, one, "NOOP", "OK"); strings.Contains(response, " FETCH (") {
					t.Fatal("repeated flag notification", response)
				}
				if response := imapReply(t, one, command+` 1 -FLAGS.SILENT (\Flagged)`, "OK"); strings.Contains(response, " FETCH (") {
					t.Fatal("SILENT reported its own flag change", response)
				}
			})
		}
	}
}
