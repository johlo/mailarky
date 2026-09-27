// Package emulator implements the shared SMTP, IMAP and HTTP test mail service.
package emulator

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
	_ "time/tzdata"

	"github.com/emersion/go-imap/server"
)

func run(ctx context.Context, c configuration) error {
	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return err
	}
	manager, err := openMailboxManager(ctx, c)
	if err != nil {
		return err
	}
	defer manager.Close()
	b := manager.lookup("default").store
	api, err := newAPI(b)
	if err != nil {
		return err
	}
	api.manager = manager
	smtpServer, err := newSMTPServer(b, b.deliver, manager)
	if err != nil {
		return err
	}
	defer smtpServer.Close()
	imapServer := server.New(manager)
	imapServer.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	defer imapServer.Close()
	imapListener, err := tls.Listen("tcp", c.IMAPAddress, imapServer.TLSConfig)
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
// fails. The sendmail and version commands return without starting the service.
func Run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "sendmail" {
		return sendmail(args[1:])
	}
	if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
		log.Print("imap-emulator 2 (SMTP, IMAP, HTTP)")
		return nil
	}
	c, err := loadConfig(args)
	if err != nil {
		return err
	}
	return run(ctx, c)
}
