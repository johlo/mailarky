// Package sandbox implements the shared SMTP, IMAP and HTTP test mail service.
package sandbox

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
	_ "time/tzdata"
)

func run(ctx context.Context, c configuration) error {
	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return fmt.Errorf("load IMAP TLS certificate: %w; set --imap-tls-cert and --imap-tls-key (or MAILARKY_IMAP_CERT and MAILARKY_IMAP_KEY); see mailarky --help for setup", err)
	}
	manager, err := openService(ctx, c)
	if err != nil {
		return err
	}
	defer manager.Close()
	api := newAPI(manager)
	smtpServer, err := newSMTPServer(manager)
	if err != nil {
		return err
	}
	defer smtpServer.Close()
	imapServer := newIMAPServer(manager)
	imapTLSConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	defer imapServer.Close()
	imapListener, err := tls.Listen("tcp", c.IMAPAddress, imapTLSConfig)
	if err != nil {
		return err
	}
	defer imapListener.Close()
	smtpListener, err := net.Listen("tcp", c.SMTPAddress)
	if err != nil {
		return err
	}
	defer smtpListener.Close()
	if c.SMTPTLS == "tls" {
		smtpListener = tls.NewListener(smtpListener, smtpServer.TLSConfig)
	}
	httpListener, err := net.Listen("tcp", c.HTTPAddress)
	if err != nil {
		return err
	}
	defer httpListener.Close()
	httpServer := &http.Server{Handler: api.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	defer httpServer.Close()
	errs := make(chan error, 3)
	go func() { errs <- imapServer.Serve(imapListener) }()
	go func() { errs <- smtpServer.Serve(smtpListener) }()
	go func() {
		if c.HTTPCert != "" {
			errs <- httpServer.ServeTLS(httpListener, c.HTTPCert, c.HTTPKey)
		} else {
			errs <- httpServer.Serve(httpListener)
		}
	}()
	log.Printf("SMTP on %s; IMAP on %s (TLS); HTTP API on %s", smtpListener.Addr(), imapListener.Addr(), httpListener.Addr())
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Run handles command arguments and serves until ctx is canceled or a listener
// fails. The sendmail, help and version commands do not start the service.
func Run(ctx context.Context, args []string) error {
	return runCommand(ctx, args, os.Stdout)
}

func runCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "sendmail":
			err := sendmail(args[1:], output)
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		case "--help", "-h", "help":
			printUsage(output)
			return nil
		case "--version", "version":
			if len(args) != 1 {
				return errors.New("version does not accept arguments")
			}
			_, err := fmt.Fprintln(output, buildVersion())
			return err
		}
	}
	c, err := loadConfig(args, output)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	return run(ctx, c)
}
