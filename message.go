package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	"golang.org/x/net/html"
)

type attachment struct {
	PartID      string
	FileName    string
	ContentType string
	ContentID   string
	Size        int
	Checksums   map[string]string
	Data        []byte `json:"-"`
}

type unsubscribeInfo struct {
	Header, HeaderPost string
	Links              []string
	Errors             string
}

type messageDetail struct {
	MailboxID            string
	ID                   string
	MessageID            string
	From                 *mail.Address
	To, Cc, Bcc, ReplyTo []*mail.Address
	Subject, Text, HTML  string
	Date                 time.Time
	Size                 int
	Inline, Attachments  []attachment
	Tags                 []string
	ReturnPath, Username string
	ListUnsubscribe      unsubscribeInfo
	Folder               string
	UID                  uint32
	Read                 bool
}

type messageSummary struct {
	MailboxID            string
	ID, MessageID        string
	From                 *mail.Address
	To, Cc, Bcc, ReplyTo []*mail.Address
	Subject, Snippet     string
	Created              time.Time
	Size, Attachments    int
	Tags                 []string
	Read                 bool
	Username, Folder     string
	UID                  uint32
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
	Flags, Tags           []string
	EnvelopeFrom          string
	EnvelopeTo            []string
	Username              string
	Detail                messageDetail       `json:"-"`
	Headers               map[string][]string `json:"-"`
}

func (m *storedMessage) memory() *memory.Message {
	return &memory.Message{Uid: m.UID, Date: m.InternalDate, Size: uint32(len(m.Raw)), Flags: append([]string{}, m.Flags...), Body: m.Raw}
}

func (m *storedMessage) detail() messageDetail {
	d := m.Detail
	d.MailboxID = m.MailboxID
	d.ID, d.Folder, d.UID, d.Username = m.ID, m.Folder, m.UID, m.Username
	d.Tags = append([]string{}, m.Tags...)
	d.Read = hasFlag(m.Flags, imap.SeenFlag)
	return d
}

func (m *storedMessage) summary() messageSummary {
	d := m.detail()
	snippet := []rune(strings.Join(strings.Fields(d.Text), " "))
	if len(snippet) > 250 {
		snippet = snippet[:250]
	}
	return messageSummary{MailboxID: m.MailboxID, ID: d.ID, MessageID: d.MessageID, From: d.From, To: d.To, Cc: d.Cc, Bcc: d.Bcc, ReplyTo: d.ReplyTo, Subject: d.Subject, Snippet: string(snippet), Created: m.Created, Size: d.Size, Attachments: len(d.Attachments), Tags: d.Tags, Read: d.Read, Username: d.Username, Folder: d.Folder, UID: d.UID}
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
	d := messageDetail{Date: fallback, Size: len(raw), From: &mail.Address{}, To: []*mail.Address{}, Cc: []*mail.Address{}, Bcc: []*mail.Address{}, ReplyTo: []*mail.Address{}, Tags: []string{}, Inline: []attachment{}, Attachments: []attachment{}}
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
	addresses := func(name string) []*mail.Address {
		text, _ := entity.Header.Text(name)
		list, _ := mail.ParseAddressList(text)
		if list == nil {
			return []*mail.Address{}
		}
		return list
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
		for _, list := range [][]*mail.Address{d.To, d.Cc, d.Bcc} {
			for _, a := range list {
				if strings.EqualFold(a.Address, recipient) {
					found = true
				}
			}
		}
		if !found {
			d.Bcc = append(d.Bcc, &mail.Address{Address: recipient})
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
	d.ListUnsubscribe = parseUnsubscribe(entity.Header.Get("List-Unsubscribe"), entity.Header.Get("List-Unsubscribe-Post"))
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
		a := attachment{PartID: strings.TrimPrefix(path, "0."), FileName: name, ContentType: media, ContentID: cid, Size: len(data), Data: data, Checksums: map[string]string{"MD5": fmt.Sprintf("%x", md5.Sum(data)), "SHA1": fmt.Sprintf("%x", sha1.Sum(data)), "SHA256": fmt.Sprintf("%x", sha256.Sum256(data))}}
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

func parseUnsubscribe(header, post string) unsubscribeInfo {
	u := unsubscribeInfo{Header: header, HeaderPost: post, Links: []string{}}
	var issues []string
	if header == "" {
		if post != "" {
			u.Errors = "List-Unsubscribe-Post requires List-Unsubscribe"
		}
		return u
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "<") || !strings.HasSuffix(part, ">") {
			issues = append(issues, "links must be enclosed in angle brackets")
			continue
		}
		value := strings.Trim(part, "<>")
		parsed, err := url.Parse(value)
		if err != nil || !(parsed.Scheme == "https" || parsed.Scheme == "http" || parsed.Scheme == "mailto") {
			issues = append(issues, "invalid unsubscribe URL")
			continue
		}
		kind := parsed.Scheme
		if kind == "http" {
			kind = "https"
		}
		if seen[kind] {
			issues = append(issues, "multiple links of the same type")
		}
		seen[kind] = true
		u.Links = append(u.Links, value)
	}
	if post != "" && post != "List-Unsubscribe=One-Click" {
		issues = append(issues, "invalid one-click header")
	}
	if post != "" && !strings.Contains(header, "<https://") {
		issues = append(issues, "one-click requires HTTPS")
	}
	u.Errors = strings.Join(issues, "; ")
	return u
}
