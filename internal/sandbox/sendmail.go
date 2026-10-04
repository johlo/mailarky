package sandbox

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Sendmail mode accepts envelope recipients or -t to extract To/Cc/Bcc.
func sendmail(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("sendmail", flag.ContinueOnError)
	fs.SetOutput(output)
	host := os.Getenv("MAILARKY_SENDMAIL_SMTP_ADDR")
	if host == "" {
		host = "localhost:1025"
	}
	fs.StringVar(&host, "S", host, "SMTP host:port")
	caFile := fs.String("ca", os.Getenv("MAILARKY_SENDMAIL_CA"), "CA certificate for SMTP STARTTLS")
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
	return submitMail(host, *sender, recipients, raw, *caFile)
}

func submitMail(address, from string, to []string, raw []byte, caFile string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		// The container ships this public test CA alongside the server certificate.
		if caFile == "" {
			if _, err := os.Stat("/certs/server.crt"); err == nil {
				caFile = "/certs/server.crt"
			}
		}
		cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return errors.New("sendmail CA file has no certificates")
			}
			cfg.RootCAs = roots
		}
		if err := client.StartTLS(cfg); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write(raw); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
