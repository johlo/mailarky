package sandbox

import (
	"github.com/johlo/go-imap/v2"
	"testing"
)

func TestIMAPFoldersCopyExpungeAndStableUIDs(t *testing.T) {
	b := testStore(t, defaultConfig())
	first := appendTest(t, b, "first@test")
	second := appendTest(t, b, "second@test")
	addr := imapAddress(t, b.service)
	c := imapClient(t, addr)
	if err := c.Create("Parent", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Create("Parent/Child", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename("Parent", "Other", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.user.GetMailbox("Other/Child"); err != nil {
		t.Fatal("child rename failed", err)
	}
	set := imap.UIDSetNum(imap.UID(first.UID))
	if _, err := c.Copy(set, "Other/Child").Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	removed, err := c.Expunge().Collect()
	if err != nil || len(removed) != 1 || removed[0] != 1 {
		t.Fatal("wrong expunge sequence", removed, err)
	}

	if b.get(first.ID) != nil {
		t.Fatal("expunge did not remove")
	}
	ids, err := c.UIDSearch(new(imap.SearchCriteria), nil).Wait()
	if err != nil || len(ids.AllUIDs()) != 1 || ids.AllUIDs()[0] != imap.UID(second.UID) {
		t.Fatalf("remaining UID %v %v", ids, err)
	}
	third := appendTest(t, b, "third@test")
	if third.UID != 3 {
		t.Fatal("UID reused")
	}
	if err := c.Delete("Other/Child").Wait(); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("INBOX").Wait(); err == nil {
		t.Fatal("INBOX deleted")
	}
}
