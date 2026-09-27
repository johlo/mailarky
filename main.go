// imap-emulator is a local, in-memory IMAP mailbox for development and E2E.
// Seed synthetic mail through POST /messages, then let the real backend import
// it over TLS. The server does not receive SMTP or send mail externally.
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

	"github.com/emersion/go-imap/server"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func run(ctx context.Context) error {
	cert, err := tls.LoadX509KeyPair(env("IMAP_EMULATOR_CERT", "/certs/server.crt"), env("IMAP_EMULATOR_KEY", "/certs/server.key"))
	if err != nil {
		return err
	}
	b, err := newMailboxBackend()
	if err != nil {
		return err
	}
	srv := server.New(b)
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	listener, err := tls.Listen("tcp", ":"+env("IMAP_EMULATOR_PORT", "1993"), srv.TLSConfig)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer srv.Close()
	httpListener, err := net.Listen("tcp", ":"+env("IMAP_EMULATOR_HTTP_PORT", "8026"))
	if err != nil {
		return err
	}
	defer httpListener.Close()
	httpServer := &http.Server{Handler: controlHandler(b), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	defer httpServer.Close()
	errs := make(chan error, 2)
	go func() { errs <- srv.Serve(listener) }()
	go func() { errs <- httpServer.Serve(httpListener) }()
	log.Printf("IMAP test mailbox on %s (TLS); control API on %s", listener.Addr(), httpListener.Addr())
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
