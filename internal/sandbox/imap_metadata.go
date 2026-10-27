package sandbox

import (
	"maps"
	"strings"

	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

var _ imapserver.SessionMetadata = (*imapSession)(nil)

// Each account is an isolated mail server namespace. Both server annotations
// and mailbox annotations belong to that account, including /shared entries.
func (s *imapSession) GetMetadata(mailbox string, entries []string, depth imap.GetMetadataDepth) (map[string][]byte, error) {
	if strings.EqualFold(mailbox, "INBOX") {
		mailbox = "INBOX"
	}
	s.account.mu.RLock()
	defer s.account.mu.RUnlock()
	values := s.account.state.Metadata
	if mailbox != "" {
		folder, ok := s.account.state.Folders[mailbox]
		if !ok {
			return nil, imapNo(errNoSuchMailbox)
		}
		values = folder.Metadata
	}
	result := make(map[string][]byte)
	for name, value := range values {
		for _, entry := range entries {
			suffix, child := strings.CutPrefix(name, entry+"/")
			if name == entry || child && (depth == imap.GetMetadataDepthInfinity || depth == imap.GetMetadataDepthOne && !strings.Contains(suffix, "/")) {
				result[name] = value // store snapshots and their values are immutable
				break
			}
		}
	}
	return result, nil
}

func (s *imapSession) SetMetadata(mailbox string, entries map[string]*[]byte) error {
	if strings.EqualFold(mailbox, "INBOX") {
		mailbox = "INBOX"
	}
	return imapNo(s.account.mutate(func(state *storeState) error {
		values := state.Metadata
		if mailbox != "" {
			folder, ok := state.Folders[mailbox]
			if !ok {
				return errNoSuchMailbox
			}
			values = folder.Metadata
		}
		values = maps.Clone(values)
		if values == nil {
			values = make(map[string][]byte)
		}
		for name, value := range entries {
			if mailbox == "" && name == "/shared/admin" {
				return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm, Text: "The server administrator annotation is read-only"}
			}
			if value == nil {
				delete(values, name)
			} else {
				values[name] = append([]byte{}, (*value)...)
			}
		}
		if len(values) > imapserver.MetadataMaxEntries {
			return &imap.Error{Type: imap.StatusResponseTypeNo, Code: "METADATA TOOMANY", Text: "At most 128 metadata entries may be stored per scope"}
		}
		if mailbox == "" {
			state.Metadata = values
		} else {
			folder := state.Folders[mailbox]
			folder.Metadata = values
			state.Folders[mailbox] = folder
		}
		return nil
	}))
}
