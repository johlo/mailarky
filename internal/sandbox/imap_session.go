package sandbox

import (
	"errors"
	"io"
	"strings"

	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

// One session owns its selected view. Stores and fault registries remain shared
// only within the authenticated account.
type imapSession struct {
	service      *Service
	conn         *imapserver.Conn
	account      *Account
	selected     *mailbox
	pendingFault *faultRule
}

var _ imapserver.Session = (*imapSession)(nil)

func (s *imapSession) Close() error { s.selected = nil; return nil }
func (s *imapSession) Login(username, password string) error {
	cmd := s.conn.Command()
	e := imapEvent(s.conn, cmd, "before")
	e.username, e.identityOnly = username, true
	account := s.service.accountByUsername(username)
	if err := s.apply(cmd, e, account); err != nil {
		return err
	}
	account = s.service.authenticate(username, password)
	if account == nil {
		return imapserver.ErrAuthFailed
	}
	s.account = account
	return nil
}
func (s *imapSession) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s.selected = nil
	m, err := s.account.user.GetMailbox(name)
	if err != nil {
		return nil, imapNo(err)
	}
	data, err := m.Status()
	if err != nil {
		return nil, imapNo(err)
	}
	m.active, m.readOnly, m.conn = true, options.ReadOnly, s.conn
	s.selected = m
	return data, nil
}
func (s *imapSession) Unselect() error { s.selected = nil; return nil }
func (s *imapSession) Create(name string, _ *imap.CreateOptions) error {
	return imapNo(s.account.user.CreateMailbox(name))
}
func (s *imapSession) Delete(name string) error { return imapNo(s.account.user.DeleteMailbox(name)) }
func (s *imapSession) Rename(from, to string, _ *imap.RenameOptions) error {
	return imapNo(s.account.user.RenameMailbox(from, to))
}
func (s *imapSession) Subscribe(name string) error   { return s.subscribe(name, true) }
func (s *imapSession) Unsubscribe(name string) error { return s.subscribe(name, false) }
func (s *imapSession) subscribe(name string, subscribed bool) error {
	m, err := s.account.user.GetMailbox(name)
	if err != nil {
		return imapNo(err)
	}
	return imapNo(m.SetSubscribed(subscribed))
}
func (s *imapSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if len(patterns) == 0 || len(patterns) == 1 && patterns[0] == "" {
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: '/'})
	}
	folders, err := s.account.user.ListMailboxes(options.SelectSubscribed)
	if err != nil {
		return imapNo(err)
	}
	for _, m := range folders {
		match := false
		for _, pattern := range patterns {
			match = match || imapserver.MatchList(m.name, '/', ref, pattern)
		}
		if match {
			if err := w.WriteList(&imap.ListData{Mailbox: m.name, Delim: '/'}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *imapSession) Status(name string, _ *imap.StatusOptions) (*imap.StatusData, error) {
	m, err := s.account.user.GetMailbox(name)
	if err != nil {
		return nil, imapNo(err)
	}
	s.account.mu.RLock()
	defer s.account.mu.RUnlock()
	f, ok := s.account.state.Folders[m.name]
	if !ok {
		return nil, imapNo(errNoSuchMailbox)
	}
	records := orderedMessages(s.account.state, m.name)
	count, unseen, deleted, recent := uint32(len(records)), uint32(0), uint32(0), uint32(0)
	var size int64
	for _, record := range records {
		if !hasFlag(record.Flags, string(imap.FlagSeen)) {
			unseen++
		}
		if hasFlag(record.Flags, string(imap.FlagDeleted)) {
			deleted++
		}
		size += int64(len(record.Raw))
	}
	limit := s.AppendLimit()
	return &imap.StatusData{Mailbox: m.name, NumMessages: &count, NumUnseen: &unseen, NumDeleted: &deleted, NumRecent: &recent, Size: &size, UIDNext: imap.UID(f.NextUID), UIDValidity: f.Validity, AppendLimit: &limit}, nil
}
func (s *imapSession) Append(name string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	if strings.EqualFold(name, "INBOX") {
		name = "INBOX"
	}
	raw, err := io.ReadAll(io.LimitReader(r, s.account.options.MaxSize+1))
	if err != nil {
		return nil, imapNo(err)
	}
	_, err = s.account.append(raw, appendOptions{Folder: name, Date: options.Time, Flags: stringFlags(options.Flags), Notify: true})
	return nil, imapDestinationError(err)
}
func (s *imapSession) AppendLimit() uint32 {
	return uint32(min(s.service.config.MaxSize, int64(^uint32(0))))
}
func (s *imapSession) Expunge(_ *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	if s.selected == nil {
		return imapNo(errors.New("select a mailbox first"))
	}
	if s.selected.readOnly {
		if s.conn.Command().Name == "CLOSE" {
			return nil
		}
		return imapNo(errors.New("mailbox is read-only"))
	}
	return imapNo(s.selected.Expunge(uids))
}
func (s *imapSession) Copy(set imap.NumSet, dest string) (*imap.CopyData, error) {
	if s.selected == nil {
		return nil, imapNo(errors.New("select a mailbox first"))
	}
	return nil, imapDestinationError(s.selected.CopyMessages(set, dest))
}
func (s *imapSession) Store(_ *imapserver.FetchWriter, set imap.NumSet, flags *imap.StoreFlags, _ *imap.StoreOptions) error {
	if s.selected == nil {
		return imapNo(errors.New("select a mailbox first"))
	}
	if s.selected.readOnly {
		return imapNo(errors.New("mailbox is read-only"))
	}
	return imapNo(s.selected.UpdateMessagesFlags(set, flags))
}
func imapNo(err error) error {
	if err == nil || errors.Is(err, imapserver.ErrResponseHandled) {
		return err
	}
	return (*imap.Error)(imapFaultStatus(err))
}
func imapDestinationError(err error) error {
	if errors.Is(err, errNoSuchMailbox) {
		err = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: err.Error()}
	}
	return imapNo(err)
}
func imapFlags(flags []string) []imap.Flag {
	out := make([]imap.Flag, len(flags))
	for i, f := range flags {
		out[i] = imap.Flag(f)
	}
	return out
}
func stringFlags(flags []imap.Flag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = string(f)
	}
	return out
}
func updateFlags(existing []string, change *imap.StoreFlags) []string {
	flags := append([]string{}, existing...)
	if change.Op == imap.StoreFlagsSet {
		flags = []string{}
	}
	for _, flag := range change.Flags {
		if strings.EqualFold(string(flag), `\Recent`) {
			continue
		}
		if change.Op == imap.StoreFlagsDel {
			filtered := flags[:0]
			for _, f := range flags {
				if !strings.EqualFold(f, string(flag)) {
					filtered = append(filtered, f)
				}
			}
			flags = filtered
		} else if !hasFlag(flags, string(flag)) {
			flags = append(flags, string(flag))
		}
	}
	return flags
}
