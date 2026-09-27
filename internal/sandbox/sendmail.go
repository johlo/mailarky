package sandbox

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
)

// Sendmail mode accepts envelope recipients or -t to extract To/Cc/Bcc.
func sendmail(args []string) error {
	fs := flag.NewFlagSet("sendmail", flag.ContinueOnError)
	host := os.Getenv("MP_SENDMAIL_SMTP_ADDR")
	if host == "" {
		host = "localhost:1025"
	}
	fs.StringVar(&host, "S", host, "SMTP host:port")
	sender := fs.String("f", "", "envelope sender")
	headers := fs.Bool("t", false, "read recipients from headers")
	fs.Bool("i", false, "ignore dot lines")
	fs.Bool("oi", false, "ignore dot lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxMessageBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxMessageBytes {
		return errors.New("message too large")
	}
	raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n"))
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	recipients := append([]string{}, fs.Args()...)
	if *sender == "" {
		addresses, err := msg.Header.AddressList("From")
		if err != nil || len(addresses) == 0 {
			return errors.New("sender is required")
		}
		*sender = addresses[0].Address
	}
	if *headers {
		for _, key := range []string{"To", "Cc", "Bcc"} {
			if msg.Header.Get(key) == "" {
				continue
			}
			addresses, err := msg.Header.AddressList(key)
			if err != nil {
				return err
			}
			for _, a := range addresses {
				recipients = append(recipients, a.Address)
			}
		}
	}
	if len(recipients) == 0 {
		return errors.New("at least one recipient is required")
	}
	raw = replaceHeaders(raw, map[string]string{"Bcc": ""})
	return smtp.SendMail(host, nil, *sender, recipients, raw)
}
