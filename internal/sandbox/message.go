package sandbox

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"github.com/johlo/go-imap/v2"
	"golang.org/x/net/html"
)

type address struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}
type attachment struct {
	PartID      string `json:"part_id"`
	FileName    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id,omitempty"`
	Size        int    `json:"size"`
}
type messageDetail struct {
	MailboxID   string       `json:"account_id"`
	ID          string       `json:"id"`
	MessageID   string       `json:"message_id"`
	From        *address     `json:"from"`
	To          []*address   `json:"to"`
	Cc          []*address   `json:"cc"`
	Bcc         []*address   `json:"bcc"`
	ReplyTo     []*address   `json:"reply_to"`
	Subject     string       `json:"subject"`
	Text        string       `json:"text"`
	HTML        string       `json:"html"`
	Date        time.Time    `json:"date"`
	Size        int          `json:"size"`
	Inline      []attachment `json:"inline"`
	Attachments []attachment `json:"attachments"`
	ReturnPath  string       `json:"return_path"`
	Username    string       `json:"username"`
	Folder      string       `json:"folder"`
	UID         uint32       `json:"uid"`
	Read        bool         `json:"read"`
	Flags       []string     `json:"flags"`
}
type messageSummary struct {
	MailboxID   string     `json:"account_id"`
	ID          string     `json:"id"`
	MessageID   string     `json:"message_id"`
	From        *address   `json:"from"`
	To          []*address `json:"to"`
	Subject     string     `json:"subject"`
	Snippet     string     `json:"snippet"`
	Created     time.Time  `json:"created"`
	Size        int        `json:"size"`
	Attachments int        `json:"attachments"`
	Read        bool       `json:"read"`
	Username    string     `json:"username"`
	Folder      string     `json:"folder"`
	UID         uint32     `json:"uid"`
}
type parsedMessage struct {
	detail  messageDetail
	headers map[string][]string
}

// Raw is immutable. Mutable fields and maps belong to the store mutex.
type storedMessage struct {
	MailboxID             string
	Sequence              uint64
	ID                    string
	Folder                string
	UID                   uint32
	Created, InternalDate time.Time
	Raw                   []byte
	Flags                 []string
	EnvelopeFrom          string
	EnvelopeTo            []string
	Username              string
	Detail                messageDetail       `json:"-"`
	Headers               map[string][]string `json:"-"`
}

func (m *storedMessage) detail() messageDetail {
	d := m.Detail
	d.MailboxID = m.MailboxID
	d.ID, d.Folder, d.UID, d.Username = m.ID, m.Folder, m.UID, m.Username
	d.Flags = append([]string{}, m.Flags...)
	d.Read = hasFlag(m.Flags, string(imap.FlagSeen))
	return d
}

func (m *storedMessage) summary() messageSummary {
	d := m.detail()
	snippet := []rune(strings.Join(strings.Fields(d.Text), " "))
	if len(snippet) > 250 {
		snippet = snippet[:250]
	}
	return messageSummary{MailboxID: m.MailboxID, ID: d.ID, MessageID: d.MessageID, From: d.From, To: d.To, Subject: d.Subject, Snippet: string(snippet), Created: m.Created, Size: d.Size, Attachments: len(d.Attachments), Read: d.Read, Username: d.Username, Folder: d.Folder, UID: d.UID}
}

func hasFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, flag) {
			return true
		}
	}
	return false
}

func parseMessage(raw []byte, fallback time.Time, envelopeFrom string, envelopeTo []string) (messageDetail, map[string][]string, error) {
	d := messageDetail{Date: fallback, Size: len(raw), From: &address{}, To: []*address{}, Cc: []*address{}, Bcc: []*address{}, ReplyTo: []*address{}, Inline: []attachment{}, Attachments: []attachment{}}
	headers := make(map[string][]string)
	entity, err := message.Read(bytes.NewReader(raw))
	if entity == nil || (err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err)) {
		return d, nil, fmt.Errorf("invalid MIME message: %w", err)
	}
	for fields := entity.Header.Fields(); fields.Next(); {
		value, err := fields.Text()
		if err != nil {
			value = fields.Value()
		}
		headers[fields.Key()] = append(headers[fields.Key()], value)
	}
	addresses := func(name string) []*address {
		text, _ := entity.Header.Text(name)
		list, _ := mail.ParseAddressList(text)
		if list == nil {
			return []*address{}
		}
		out := make([]*address, 0, len(list))
		for _, a := range list {
			out = append(out, &address{Name: a.Name, Address: a.Address})
		}
		return out
	}
	if list := addresses("From"); len(list) > 0 {
		d.From = list[0]
	} else if envelopeFrom != "" {
		d.From.Address = envelopeFrom
	}
	d.To, d.Cc, d.Bcc, d.ReplyTo = addresses("To"), addresses("Cc"), addresses("Bcc"), addresses("Reply-To")
	// SMTP envelope recipients omitted from visible headers are blind recipients.
	for _, recipient := range envelopeTo {
		found := false
		for _, list := range [][]*address{d.To, d.Cc, d.Bcc} {
			for _, a := range list {
				if strings.EqualFold(a.Address, recipient) {
					found = true
				}
			}
		}
		if !found {
			d.Bcc = append(d.Bcc, &address{Address: recipient})
		}
	}
	d.Subject, _ = entity.Header.Text("Subject")
	d.MessageID = strings.Trim(entity.Header.Get("Message-ID"), "<> \t\r\n")
	d.ReturnPath = entity.Header.Get("Return-Path")
	if d.ReturnPath == "" {
		d.ReturnPath = envelopeFrom
	}
	if date, err := mail.ParseDate(entity.Header.Get("Date")); err == nil {
		d.Date = date.UTC()
	}

	parts := 0
	var walk func(*message.Entity, string, int) error
	walk = func(e *message.Entity, path string, depth int) error {
		parts++
		if depth > 32 || parts > 1000 {
			return fmt.Errorf("MIME nesting or part limit exceeded")
		}
		if reader := e.MultipartReader(); reader != nil {
			defer reader.Close()
			for index := 1; ; index++ {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if part == nil {
					return err
				}
				if err := walk(part, fmt.Sprintf("%s.%d", path, index), depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		media, params, _ := e.Header.ContentType()
		if media == "" {
			media = "text/plain"
		}
		disposition, dispParams, _ := e.Header.ContentDisposition()
		name := dispParams["filename"]
		if name == "" {
			name = params["name"]
		}
		cid := strings.Trim(e.Header.Get("Content-ID"), "<>")
		data, err := io.ReadAll(io.LimitReader(e.Body, maxMessageBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxMessageBytes {
			return fmt.Errorf("decoded MIME part too large")
		}
		if disposition != "attachment" && name == "" && cid == "" && (media == "text/plain" || media == "text/html") {
			if media == "text/html" {
				d.HTML += string(data)
			} else {
				if d.Text != "" {
					d.Text += "\n"
				}
				d.Text += string(data)
			}
			return nil
		}
		a := attachment{PartID: strings.TrimPrefix(path, "0."), FileName: name, ContentType: media, ContentID: cid, Size: len(data)}
		if disposition == "inline" || cid != "" {
			d.Inline = append(d.Inline, a)
		} else {
			d.Attachments = append(d.Attachments, a)
		}
		return nil
	}
	if err := walk(entity, "0", 0); err != nil {
		return d, nil, err
	}
	if d.Text == "" && d.HTML != "" {
		d.Text = htmlText(d.HTML)
	}
	return d, headers, nil
}

func htmlText(source string) string {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return source
	}
	var output strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "style" || n.Data == "script" || n.Data == "head") {
			return
		}
		if n.Type == html.TextNode {
			output.WriteString(n.Data)
			output.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(output.String()), " ")
}

// Only raw MIME and searchable text/metadata are cached; attachment payloads
// are decoded on demand instead of being retained beside the raw message.
func readMIMEPart(raw []byte, target string) ([]byte, error) {
	root, err := message.Read(bytes.NewReader(raw))
	if root == nil {
		return nil, err
	}
	var walk func(*message.Entity, string) ([]byte, error)
	walk = func(e *message.Entity, path string) ([]byte, error) {
		if strings.TrimPrefix(path, "0.") == target {
			return io.ReadAll(io.LimitReader(e.Body, maxMessageBytes+1))
		}
		if r := e.MultipartReader(); r != nil {
			defer r.Close()
			for i := 1; ; i++ {
				part, err := r.NextPart()
				if err == io.EOF {
					break
				}
				if part == nil {
					return nil, err
				}
				data, err := walk(part, fmt.Sprintf("%s.%d", path, i))
				if err != nil || data != nil {
					return data, err
				}
			}
		}
		return nil, nil
	}
	return walk(root, "0")
}
func withEnvelope(d messageDetail, recipients []string) messageDetail {
	d.Bcc = append([]*address{}, d.Bcc...)
	for _, recipient := range recipients {
		found := false
		for _, list := range [][]*address{d.To, d.Cc, d.Bcc} {
			for _, a := range list {
				if strings.EqualFold(a.Address, recipient) {
					found = true
				}
			}
		}
		if !found {
			d.Bcc = append(d.Bcc, &address{Address: recipient})
		}
	}
	return d
}
