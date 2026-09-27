package emulator

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type sendAddress struct {
	Name  string
	Email string
}
type sendAttachment struct{ Filename, ContentType, ContentID, Content string }
type sendRequest struct {
	From                sendAddress
	To, Cc, ReplyTo     []sendAddress
	Bcc                 []string
	Subject, Text, HTML string
	Headers             map[string]string
	Tags                []string
	Attachments         []sendAttachment
}

func encodePart(media, body string) (string, []byte, error) {
	var buffer bytes.Buffer
	writer := quotedprintable.NewWriter(&buffer)
	if _, err := writer.Write([]byte(body)); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return media + "; charset=utf-8", buffer.Bytes(), nil
}
func (req sendRequest) mimeMessage() (raw []byte, from string, recipients []string, err error) {
	if !validEnvelope(req.From.Email, false) {
		return nil, "", nil, errors.New("valid From.Email is required")
	}
	from = req.From.Email
	fields := map[string]string{"Date": time.Now().UTC().Format(time.RFC1123Z), "From": (&mail.Address{Name: req.From.Name, Address: req.From.Email}).String(), "Subject": mime.QEncoding.Encode("UTF-8", req.Subject), "Message-Id": "<" + uuid.NewString() + "@mail-emulator.test>", "Mime-Version": "1.0"}
	for name, list := range map[string][]sendAddress{"To": req.To, "Cc": req.Cc, "Reply-To": req.ReplyTo} {
		var values []string
		for _, a := range list {
			if !validEnvelope(a.Email, false) || strings.ContainsAny(a.Name, "\r\n") {
				return nil, "", nil, errors.New("invalid address")
			}
			values = append(values, (&mail.Address{Name: a.Name, Address: a.Email}).String())
			if name != "Reply-To" {
				recipients = append(recipients, a.Email)
			}
		}
		if len(values) > 0 {
			fields[name] = strings.Join(values, ", ")
		}
	}
	for _, to := range req.Bcc {
		if !validEnvelope(to, false) {
			return nil, "", nil, errors.New("invalid Bcc address")
		}
		recipients = append(recipients, to)
	}
	if len(recipients) == 0 {
		return nil, "", nil, errors.New("at least one recipient is required")
	}
	for key, value := range req.Headers {
		key = textproto.CanonicalMIMEHeaderKey(key)
		if key == "" || strings.ContainsAny(key, ": \t\r\n") || strings.ContainsAny(value, "\r\n") {
			return nil, "", nil, errors.New("invalid extra header")
		}
		switch key {
		case "Content-Type", "Content-Transfer-Encoding", "Mime-Version", "From", "To", "Cc", "Bcc":
			return nil, "", nil, fmt.Errorf("header %s is managed by the send API", key)
		}
		fields[key] = value
	}
	for key, value := range fields {
		if strings.ContainsAny(value, "\r\n") {
			return nil, "", nil, fmt.Errorf("invalid %s header", key)
		}
	}
	var body bytes.Buffer
	if len(req.Attachments) == 0 && !(req.Text != "" && req.HTML != "") {
		media, text := "text/plain", req.Text
		if req.HTML != "" {
			media, text = "text/html", req.HTML
		}
		contentType, data, err := encodePart(media, text)
		if err != nil {
			return nil, "", nil, err
		}
		fields["Content-Type"] = contentType
		fields["Content-Transfer-Encoding"] = "quoted-printable"
		body.Write(data)
	} else {
		writer := multipart.NewWriter(&body)
		fields["Content-Type"] = mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": writer.Boundary()})
		if len(req.Attachments) == 0 {
			fields["Content-Type"] = mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": writer.Boundary()})
		}
		bodyWriter := writer
		var alternative bytes.Buffer
		if len(req.Attachments) > 0 && req.Text != "" && req.HTML != "" {
			bodyWriter = multipart.NewWriter(&alternative)
		}
		for _, part := range []struct{ media, text string }{{"text/plain", req.Text}, {"text/html", req.HTML}} {
			if part.text == "" {
				continue
			}
			media, data, err := encodePart(part.media, part.text)
			if err != nil {
				return nil, "", nil, err
			}
			w, err := bodyWriter.CreatePart(textproto.MIMEHeader{"Content-Type": {media}, "Content-Transfer-Encoding": {"quoted-printable"}})
			if err != nil {
				return nil, "", nil, err
			}
			w.Write(data)
		}
		if bodyWriter != writer {
			if err := bodyWriter.Close(); err != nil {
				return nil, "", nil, err
			}
			part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": bodyWriter.Boundary()})}})
			if err != nil {
				return nil, "", nil, err
			}
			part.Write(alternative.Bytes())
		}
		for _, attachment := range req.Attachments {
			if attachment.Filename == "" || strings.ContainsAny(attachment.Filename+attachment.ContentID, "\r\n") {
				return nil, "", nil, errors.New("attachment filename is required and fields must be single-line")
			}
			data, err := base64.StdEncoding.DecodeString(attachment.Content)
			if err != nil {
				return nil, "", nil, errors.New("attachment Content must be base64")
			}
			media := attachment.ContentType
			if media == "" {
				media = http.DetectContentType(data)
			}
			if _, _, err := mime.ParseMediaType(media); err != nil {
				return nil, "", nil, errors.New("invalid attachment content type")
			}
			disposition := "attachment"
			headers := textproto.MIMEHeader{"Content-Type": {media}, "Content-Transfer-Encoding": {"base64"}}
			if attachment.ContentID != "" {
				disposition = "inline"
				headers.Set("Content-ID", "<"+strings.Trim(attachment.ContentID, "<>")+">")
			}
			headers.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.Filename}))
			w, err := writer.CreatePart(headers)
			if err != nil {
				return nil, "", nil, err
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			for len(encoded) > 76 {
				fmt.Fprint(w, encoded[:76]+"\r\n")
				encoded = encoded[76:]
			}
			fmt.Fprint(w, encoded)
		}
		if err := writer.Close(); err != nil {
			return nil, "", nil, err
		}
	}
	var message bytes.Buffer
	keys := []string{}
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&message, "%s: %s\r\n", key, fields[key])
	}
	message.WriteString("\r\n")
	message.Write(body.Bytes())
	return message.Bytes(), from, recipients, nil
}

func (a *httpAPI) send(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := decodeJSON(w, r, a.store.config.MaxSize*2, &req); err != nil {
		jsonResponse(w, 400, map[string]string{"Error": err.Error()})
		return
	}
	raw, from, to, err := req.mimeMessage()
	if err != nil {
		jsonResponse(w, 400, map[string]string{"Error": err.Error()})
		return
	}
	username, _, _ := r.BasicAuth()
	m, err := a.store.append(raw, appendOptions{Folder: a.store.config.SMTPFolder, From: from, To: to, Username: username, Tags: req.Tags, Notify: true})
	if err != nil {
		jsonResponse(w, 400, map[string]string{"Error": err.Error()})
		return
	}
	if err := a.store.deliver(r.Context(), m); err != nil {
		jsonResponse(w, 502, map[string]string{"Error": "configured delivery failed"})
		return
	}
	jsonResponse(w, 200, map[string]string{"ID": m.ID, "MessageID": m.Detail.MessageID})
}
