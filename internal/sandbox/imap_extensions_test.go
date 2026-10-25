package sandbox

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

func TestMetadataPersistenceAndAccountIsolation(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database = filepath.Join(t.TempDir(), "mail.db")
	var other mailboxDescription
	var validity uint32
	value := []byte("{\"identities\":[]}\x00\xff\r\n")
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			service, h := testManager(t, cfg)
			if !restart {
				other = createAccount(t, h, "metadata-other")
			}
			addr := imapAddress(t, service)
			c := imapClient(t, addr)
			if !c.Caps().Has(imap.CapMetadata) || !c.Caps().Has(imap.CapUIDPlus) {
				t.Fatal("missing capabilities", c.Caps())
			}
			if !restart {
				for _, mailbox := range []string{"", "INBOX"} {
					if err := c.SetMetadata(mailbox, map[string]*[]byte{"/private/vendor/alps/settings": &value}).Wait(); err != nil {
						t.Fatal(err)
					}
				}
				raw := testRaw("uid-persist", "uid-persist", "recipient@example.test")
				appendCmd := c.Append("INBOX", int64(len(raw)), nil)
				if _, err := appendCmd.Write(raw); err != nil {
					t.Fatal(err)
				}
				if err := appendCmd.Close(); err != nil {
					t.Fatal(err)
				}
				data, err := appendCmd.Wait()
				if err != nil || data.UID != 1 || data.UIDValidity == 0 {
					t.Fatalf("%+v %v", data, err)
				}
				validity = data.UIDValidity
			}
			// Read through a second connection, then repeat after reopening bbolt.
			reader := imapClient(t, addr)
			for _, mailbox := range []string{"", "inbox"} {
				data, err := reader.GetMetadata(mailbox, []string{"/PRIVATE/vendor/alps/settings"}, nil).Wait()
				if err != nil {
					t.Fatal(err)
				}
				got := data.Entries["/private/vendor/alps/settings"]
				if got == nil || !bytes.Equal(*got, value) {
					t.Fatalf("scope %q lost metadata: %+v", mailbox, data)
				}
			}
			selected, err := reader.Select("INBOX", nil).Wait()
			if err != nil || selected.UIDValidity != validity || selected.UIDNext != 2 {
				t.Fatalf("UID state changed: %+v, %v", selected, err)
			}
			second := imapClient(t, addr, other.Username, "password-metadata-other")
			for _, mailbox := range []string{"", "INBOX"} {
				data, err := second.GetMetadata(mailbox, []string{"/private/vendor/alps/settings"}, nil).Wait()
				if err != nil || data.Entries["/private/vendor/alps/settings"] != nil {
					t.Fatalf("metadata escaped account: %+v %v", data, err)
				}
			}
		})
	}
}

func TestMetadataMailboxLifecycleAndAtomicity(t *testing.T) {
	a := testStore(t, defaultConfig())
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, `SETMETADATA INBOX (/private/comment "original" /shared/comment "shared")`, "OK")
	imapReply(t, w, "RENAME INBOX Renamed", "OK")
	for _, folder := range []string{"INBOX", "Renamed"} {
		got := imapReply(t, w, "GETMETADATA "+folder+" /private/comment", "OK")
		if !strings.Contains(got, `"original"`) {
			t.Fatal(got)
		}
	}
	imapReply(t, w, `SETMETADATA INBOX (/private/comment "changed")`, "OK")
	if got := imapReply(t, w, `GETMETADATA Renamed /private/comment`, "OK"); !strings.Contains(got, `"original"`) {
		t.Fatal("rename aliases annotation maps:", got)
	}
	imapReply(t, w, "RENAME Renamed Moved", "OK")
	if got := imapReply(t, w, `GETMETADATA Moved /shared/comment`, "OK"); !strings.Contains(got, `"shared"`) {
		t.Fatal(got)
	}
	imapReply(t, w, "DELETE Moved", "OK")
	imapReply(t, w, "CREATE Moved", "OK")
	if got := imapReply(t, w, `GETMETADATA Moved /private/comment`, "OK"); !strings.Contains(got, "NIL") {
		t.Fatal(got)
	}
	imapReply(t, w, `GETMETADATA Missing /private/comment`, "NO")
	imapReply(t, w, `SETMETADATA Missing (/private/comment "bad")`, "NO")
	imapReply(t, w, `SETMETADATA "" (/private/vendor/alps/settings "original")`, "OK")
	imapReply(t, w, `SETMETADATA "" (/private/vendor/alps/settings "changed" /shared/admin "mailto:test@example.test")`, "NO")
	if got := imapReply(t, w, `GETMETADATA "" /private/vendor/alps/settings`, "OK"); !strings.Contains(got, `"original"`) {
		t.Fatal("partial write on backend error:", got)
	}
	var entries []string
	for i := 0; i < imapserver.MetadataMaxEntries; i++ {
		entries = append(entries, fmt.Sprintf(`/private/vendor/alps/key%d "value"`, i))
	}
	imapReply(t, w, "SETMETADATA Moved ("+strings.Join(entries, " ")+")", "OK")
	got := imapReply(t, w, `SETMETADATA Moved (/private/vendor/alps/key0 "changed" /private/vendor/alps/extra "overflow")`, "NO")
	if !strings.Contains(got, "[METADATA TOOMANY]") {
		t.Fatal(got)
	}
	if got := imapReply(t, w, `GETMETADATA Moved /private/vendor/alps/key0`, "OK"); !strings.Contains(got, `"value"`) {
		t.Fatal("partial write on quota error:", got)
	}
	imapReply(t, w, `SETMETADATA Moved (/private/vendor/alps/key0 NIL /private/vendor/alps/extra "")`, "OK")
	if got := imapReply(t, w, `GETMETADATA Moved (/private/vendor/alps/key0 /private/vendor/alps/extra)`, "OK"); !strings.Contains(got, `"/private/vendor/alps/key0" NIL`) || !strings.Contains(got, `"/private/vendor/alps/extra" ""`) {
		t.Fatal(got)
	}
}

func TestUIDPlusMappingsAndExpunge(t *testing.T) {
	a := testStore(t, defaultConfig())
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	a.mu.RLock()
	inbox, archive := a.state.Folders["INBOX"].Validity, a.state.Folders["Archive"].Validity
	a.mu.RUnlock()
	for i := 1; i <= 3; i++ {
		raw := testRaw(fmt.Sprintf("uid-%d", i), "uid", "recipient@example.test")
		got := imapReply(t, w, fmt.Sprintf("APPEND INBOX {%d+}\r\n%s", len(raw), raw), "OK")
		if !strings.Contains(got, fmt.Sprintf("[APPENDUID %d %d]", inbox, i)) {
			t.Fatal(got)
		}
	}
	imapReply(t, w, "SELECT INBOX", "OK")
	got := imapReply(t, w, "UID COPY 1,3 Archive", "OK")
	if !strings.Contains(got, fmt.Sprintf("[COPYUID %d 1,3 1:2]", archive)) {
		t.Fatal(got)
	}
	got = imapReply(t, w, "COPY 2 Archive", "OK")
	if !strings.Contains(got, fmt.Sprintf("[COPYUID %d 2 3]", archive)) {
		t.Fatal(got)
	}
	if got := imapReply(t, w, "UID COPY 99 Archive", "OK"); strings.Contains(got, "COPYUID") {
		t.Fatal(got)
	}
	imapReply(t, w, `UID STORE 1:2 +FLAGS (\Deleted)`, "OK")
	imapReply(t, w, "UID EXPUNGE 2:3", "OK")
	if got := imapReply(t, w, "UID SEARCH ALL", "OK"); !strings.Contains(got, "* SEARCH 1 3") {
		t.Fatal(got)
	}
	imapReply(t, w, "EXAMINE INBOX", "OK")
	imapReply(t, w, "UID EXPUNGE 1", "NO")
	imapReply(t, w, "SELECT INBOX", "OK")
	imapReply(t, w, `UID STORE 3 +FLAGS (\Deleted)`, "OK")
	imapReply(t, w, "UID EXPUNGE 99:*", "OK")
	imapReply(t, w, "UID EXPUNGE *", "OK")
	if got := imapReply(t, w, "UID SEARCH ALL", "OK"); strings.Contains(got, "* SEARCH 1") {
		t.Fatal(got)
	}
	imapReply(t, w, "SELECT Archive", "OK")
	if got := imapReply(t, w, "UID SEARCH ALL", "OK"); !strings.Contains(got, "* SEARCH 1 2 3") {
		t.Fatal("expunge affected destination:", got)
	}
}

func TestUIDPlusDuplicateSuppression(t *testing.T) {
	cfg := defaultConfig()
	cfg.IgnoreDuplicates = true
	a := testStore(t, cfg)
	m := appendTest(t, a, "duplicate")
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	for _, dest := range []string{"Sent", "Archive"} {
		got := imapReply(t, w, fmt.Sprintf("APPEND %s {%d+}\r\n%s", dest, len(m.Raw), m.Raw), "OK")
		if strings.Contains(got, "APPENDUID") != (dest == "Sent") {
			t.Fatal("incorrect duplicate UID result:", got)
		}
	}
	if len(a.snapshot()) != 1 {
		t.Fatal("duplicate behavior changed")
	}
}

func TestMetadataAndUIDExpungeFaults(t *testing.T) {
	for _, command := range []string{"GETMETADATA", "SETMETADATA", "UID EXPUNGE"} {
		for _, phase := range []string{"before", "after"} {
			t.Run(command+"/"+phase, func(t *testing.T) {
				a := testStore(t, defaultConfig())
				w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
				imapReply(t, w, "SELECT Sent", "OK")
				folder := "INBOX"
				if command == "UID EXPUNGE" {
					folder = "Sent"
				}
				f := rule("extension", "imap", command, phase, map[string]any{"type": "reject", "status": "NO", "message": "extension fault"})
				f["filter"] = map[string]string{"folder": folder}
				f["max_hits"] = 1
				installFault(t, a, false, f)
				request := command + ` INBOX /private/comment`
				if command == "SETMETADATA" {
					request = command + ` INBOX (/private/comment "stored")`
				}
				if command == "UID EXPUNGE" {
					request = command + " 1"
				} else {
					// Server annotations must not inherit the selected folder.
					imapReply(t, w, "SELECT INBOX", "OK")
					imapReply(t, w, strings.Replace(request, " INBOX ", ` "" `, 1), "OK")
					imapReply(t, w, "SELECT Sent", "OK")
				}
				if got := imapReply(t, w, request, "NO"); !strings.Contains(got, "extension fault") {
					t.Fatal(got)
				}
				if command == "SETMETADATA" {
					got := imapReply(t, w, `GETMETADATA INBOX /private/comment`, "OK")
					if strings.Contains(got, `"stored"`) != (phase == "after") {
						t.Fatal("fault fired at wrong phase:", got)
					}
				}
				imapReply(t, w, request, "OK")
				if fault, _ := a.faults.get("extension"); fault.Hits != 1 {
					t.Fatal(fault.Hits)
				}
			})
		}
	}
}
