package main

import (
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

type messageRequest struct {
	Folder       string     `json:"folder"`
	From         string     `json:"from"`
	To           []string   `json:"to"`
	Cc           []string   `json:"cc"`
	Bcc          []string   `json:"bcc"`
	Subject      string     `json:"subject"`
	MessageID    string     `json:"message_id"`
	Date         *time.Time `json:"date"`
	InternalDate *time.Time `json:"internal_date"`
	Body         string     `json:"body"`
	Flags        []string   `json:"flags"`
}

func (req *messageRequest) rawMessage() (string, time.Time, error) {
	var raw strings.Builder
	for _, header := range []struct {
		name string
		list []string
	}{{"From", []string{req.From}}, {"To", req.To}, {"Cc", req.Cc}, {"Bcc", req.Bcc}} {
		var parsed []string
		for _, value := range header.list {
			if strings.ContainsAny(value, "\r\n") {
				return "", time.Time{}, fmt.Errorf("invalid %s address", header.name)
			}
			address, err := mail.ParseAddress(value)
			if err != nil {
				return "", time.Time{}, fmt.Errorf("invalid %s address", header.name)
			}
			parsed = append(parsed, address.String())
		}
		if len(parsed) != 0 {
			fmt.Fprintf(&raw, "%s: %s\r\n", header.name, strings.Join(parsed, ", "))
		}
	}
	if len(req.To)+len(req.Cc)+len(req.Bcc) == 0 || strings.ContainsAny(req.Subject, "\r\n") {
		return "", time.Time{}, fmt.Errorf("recipients are required and subject must be a single line")
	}
	if req.MessageID == "" {
		req.MessageID = uuid.NewString() + "@imap.example.test"
	}
	req.MessageID = strings.TrimSuffix(strings.TrimPrefix(req.MessageID, "<"), ">")
	if req.MessageID == "" || strings.ContainsAny(req.MessageID, "<> \t\r\n") {
		return "", time.Time{}, fmt.Errorf("invalid message_id")
	}
	date := time.Now().UTC()
	if req.Date != nil {
		date = req.Date.UTC()
	}
	internalDate := date
	if req.InternalDate != nil {
		internalDate = req.InternalDate.UTC()
	}
	fmt.Fprintf(&raw, "Subject: %s\r\nMessage-ID: <%s>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		mime.QEncoding.Encode("UTF-8", req.Subject), req.MessageID, date.Format(time.RFC1123Z), req.Body)
	return raw.String(), internalDate, nil
}

func controlHandler(b *mailboxBackend) http.Handler {
	a, err := newAPI(b)
	if err != nil {
		panic(err)
	}
	return a.handler()
}
