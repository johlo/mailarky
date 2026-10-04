// mailarky is a headless SMTP and IMAP test mail server.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/johlo/mailarky/internal/sandbox"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sandbox.Run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
