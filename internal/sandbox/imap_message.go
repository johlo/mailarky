package sandbox

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/textproto"
	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

func (s *imapSession) affected(record *storedMessage) (*storedMessage, bool, error) {
	cmd := s.conn.Command()
	e := protocolEvent{protocol: "imap", command: cmd.Name, phase: "content", username: s.account.identity.Username, folder: s.selected.name, message: record}
	rules := append(s.service.serverFaults.claim(e, false), s.account.faults.claim(e, false)...)
	msg := cloneRecord(record)
	for _, rule := range rules {
		switch a := rule.Action.Parameters.(type) {
		case *hideAction:
			return msg, true, nil
		case *replaceHeaderAction:
			msg.Raw = replaceHeaders(msg.Raw, map[string]string{a.Header: a.Value})
		case *replaceBodyAction:
			msg.Raw = []byte(a.Value)
		case *disconnectAction, *responseAction:
			// Apply after all preceding FETCH responses have closed their writers.
			s.pendingFault = &rule
			return nil, false, imapserver.ErrResponseHandled
		default:
			if err := applyIMAPFault(s.conn, cmd, s.service, s.account, rule, e.folder); err != nil {
				return nil, false, err
			}
		}
	}
	return msg, false, nil
}

type imapFetchMessage struct {
	seq    uint32
	record *storedMessage
	flags  bool
}

func (m *mailbox) markSeen(batch []imapFetchMessage) error {
	if len(batch) == 0 {
		return nil
	}
	err := m.store.mutate(func(state *storeState) error {
		for i := range batch {
			fetched := &batch[i]
			current := state.Messages[fetched.record.ID]
			if current == nil {
				continue
			}
			if !hasFlag(current.Flags, string(imap.FlagSeen)) {
				copy := cloneRecord(current)
				copy.Flags = updateFlags(copy.Flags, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}})
				state.Messages[current.ID] = copy
				current = copy
				fetched.flags = true
			}
			fetched.record.Flags = append([]string{}, current.Flags...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, fetched := range batch {
		if fetched.flags && m.active {
			copy := cloneRecord(m.view[fetched.seq-1])
			copy.Flags = append([]string{}, fetched.record.Flags...)
			m.view[fetched.seq-1] = copy
		}
	}
	return nil
}
func (s *imapSession) Fetch(w *imapserver.FetchWriter, set imap.NumSet, options *imap.FetchOptions) error {
	m := s.selected
	if m == nil {
		return imapNo(errors.New("select a mailbox first"))
	}
	records, err := m.records()
	if err != nil {
		return imapNo(err)
	}
	marksSeen := false
	for _, section := range options.BodySection {
		marksSeen = marksSeen || !section.Peek
	}
	marksSeen = marksSeen && !m.readOnly
	var batch []imapFetchMessage
	var fetchErr error
	for i, record := range records {
		seq := uint32(i + 1)
		if !selected(set, seq, record, records) {
			continue
		}
		msg, hidden, err := s.affected(record)
		if err != nil {
			fetchErr = err
			break
		}
		if hidden {
			continue
		}
		fetched := imapFetchMessage{seq: seq, record: msg, flags: options.Flags}
		if marksSeen {
			batch = append(batch, fetched)
		} else if err := fetched.write(w, options); err != nil {
			return err
		}
	}
	if err := m.markSeen(batch); err != nil {
		s.pendingFault = nil
		return imapNo(err)
	}
	for _, fetched := range batch {
		if err := fetched.write(w, options); err != nil {
			s.pendingFault = nil
			return err
		}
	}
	return imapNo(fetchErr)
}
func (f imapFetchMessage) write(w *imapserver.FetchWriter, options *imap.FetchOptions) (err error) {
	writer := w.CreateMessage(f.seq)
	defer func() {
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
	}()
	msg := f.record
	if options.UID {
		writer.WriteUID(imap.UID(msg.UID))
	}
	if f.flags {
		writer.WriteFlags(imapFlags(msg.Flags))
	}
	if options.InternalDate {
		writer.WriteInternalDate(msg.InternalDate)
	}
	if options.RFC822Size {
		writer.WriteRFC822Size(int64(len(msg.Raw)))
	}
	if options.Envelope {
		header, _ := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(msg.Raw)))
		writer.WriteEnvelope(imapserver.ExtractEnvelope(header))
	}
	if options.BodyStructure != nil {
		writer.WriteBodyStructure(imapserver.ExtractBodyStructure(bytes.NewReader(msg.Raw)))
	}
	for _, section := range options.BodySection {
		body := imapserver.ExtractBodySection(bytes.NewReader(msg.Raw), section)
		literal := writer.WriteBodySection(section, int64(len(body)))
		_, writeErr := literal.Write(body)
		closeErr := literal.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
func (s *imapSession) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, _ *imap.SearchOptions) (*imap.SearchData, error) {
	if s.selected == nil {
		return nil, imapNo(errors.New("select a mailbox first"))
	}
	records, err := s.selected.records()
	if err != nil {
		return nil, imapNo(err)
	}
	var seqSet imap.SeqSet
	var uidSet imap.UIDSet
	data := new(imap.SearchData)
	for i, record := range records {
		seq := uint32(i + 1)
		if !matchIMAP(record, seq, records, criteria) {
			continue
		}
		msg, hidden, err := s.affected(record)
		if err != nil {
			return nil, imapNo(err)
		}
		if hidden {
			continue
		}
		if !bytes.Equal(msg.Raw, record.Raw) {
			msg.Detail, msg.Headers, err = parseMessage(msg.Raw, msg.InternalDate, msg.EnvelopeFrom, msg.EnvelopeTo)
			if err != nil {
				return nil, imapNo(err)
			}
		}
		if !matchIMAP(msg, seq, records, criteria) {
			continue
		}
		seqSet.AddNum(seq)
		uidSet.AddNum(imap.UID(msg.UID))
		n := seq
		if kind == imapserver.NumKindUID {
			n = msg.UID
		}
		if data.Min == 0 || n < data.Min {
			data.Min = n
		}
		if n > data.Max {
			data.Max = n
		}
		data.Count++
	}
	if kind == imapserver.NumKindUID {
		data.All = uidSet
	} else {
		data.All = seqSet
	}
	return data, nil
}
func matchIMAP(msg *storedMessage, seq uint32, records []*storedMessage, c *imap.SearchCriteria) bool {
	for _, set := range c.SeqNum {
		if !selected(set, seq, msg, records) {
			return false
		}
	}
	for _, set := range c.UID {
		if !selected(set, seq, msg, records) {
			return false
		}
	}
	for _, flag := range c.Flag {
		if !hasFlag(msg.Flags, string(flag)) {
			return false
		}
	}
	for _, flag := range c.NotFlag {
		if hasFlag(msg.Flags, string(flag)) {
			return false
		}
	}
	if !matchIMAPDate(msg.InternalDate, c.Since, c.Before) {
		return false
	}
	if !c.SentSince.IsZero() || !c.SentBefore.IsZero() {
		var sent time.Time
		for key, values := range msg.Headers {
			if strings.EqualFold(key, "Date") && len(values) > 0 {
				sent, _ = mail.ParseDate(values[0])
			}
		}
		if sent.IsZero() || !matchIMAPDate(sent, c.SentSince, c.SentBefore) {
			return false
		}
	}
	size := int64(len(msg.Raw))
	if c.Larger != 0 && size <= c.Larger || c.Smaller != 0 && size >= c.Smaller {
		return false
	}
	decoded := func(value string) string {
		if s, err := (&mime.WordDecoder{CharsetReader: charset.Reader}).DecodeHeader(value); err == nil {
			return s
		}
		return value
	}
	contains := func(text, query string) bool { return strings.Contains(strings.ToLower(text), strings.ToLower(query)) }
	for _, field := range c.Header {
		found := false
		for key, values := range msg.Headers {
			if strings.EqualFold(key, field.Key) {
				for _, value := range values {
					found = found || contains(decoded(value), field.Value)
				}
			}
		}
		if !found {
			return false
		}
	}
	for _, query := range c.Body {
		if !matchIMAPText(msg.Raw, query, false) {
			return false
		}
	}
	for _, query := range c.Text {
		if !matchIMAPText(msg.Raw, query, true) {
			return false
		}
	}
	for _, not := range c.Not {
		if matchIMAP(msg, seq, records, &not) {
			return false
		}
	}
	for _, or := range c.Or {
		if !matchIMAP(msg, seq, records, &or[0]) && !matchIMAP(msg, seq, records, &or[1]) {
			return false
		}
	}
	return true
}

// Search textual MIME parts, including attachments, without retaining decoded
// payloads in the store. The same nesting and part bounds as ingestion apply.
func matchIMAPText(raw []byte, query string, includeHeaders bool) bool {
	if query == "" {
		return true
	}
	entity, _ := message.Read(bytes.NewReader(raw))
	if entity == nil {
		return false
	}
	needle := strings.ToLower(query)
	parts := 0
	var walk func(*message.Entity, int) bool
	walk = func(entity *message.Entity, depth int) bool {
		parts++
		if depth > 32 || parts > 1000 {
			return false
		}
		if includeHeaders {
			fields := entity.Header.Fields()
			for fields.Next() {
				value, _ := fields.Text()
				if strings.Contains(strings.ToLower(fields.Key()+": "+value), needle) {
					return true
				}
			}
		}
		if reader := entity.MultipartReader(); reader != nil {
			defer reader.Close()
			for {
				part, err := reader.NextPart()
				if part != nil && walk(part, depth+1) {
					return true
				}
				if err != nil {
					return false
				}
			}
		}
		media, _, _ := entity.Header.ContentType()
		if media != "" && !strings.HasPrefix(media, "text/") && !strings.HasPrefix(media, "message/") {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(entity.Body, maxMessageBytes+1))
		return err == nil && len(data) <= maxMessageBytes && strings.Contains(strings.ToLower(string(data)), needle)
	}
	return walk(entity, 0)
}
func matchIMAPDate(date, since, before time.Time) bool {
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	return (since.IsZero() || !day.Before(since)) && (before.IsZero() || day.Before(before))
}
