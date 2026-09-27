// imap-emulator is a headless SMTP and IMAP test mail server.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/emersion/go-imap/server"
)

func run(ctx context.Context, c configuration) error {
	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return err
	}
	b, err := openMailbox(c)
	if err != nil {
		return err
	}
	defer b.Close()
	api, err := newAPI(b)
	if err != nil {
		return err
	}
	smtpServer, err := newSMTPServer(b, b.deliver)
	if err != nil {
		return err
	}
	defer smtpServer.Close()
	imapServer := server.New(b)
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
	stopWorkers := b.startWorkers(ctx)
	defer stopWorkers()
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

func main() {
	if len(os.Args) > 1 && os.Args[1] == "sendmail" {
		if err := sendmail(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		log.Print("imap-emulator 2 (SMTP, IMAP, HTTP)")
		return
	}
	c, err := loadConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, c); err != nil {
		log.Fatal(err)
	}
}
