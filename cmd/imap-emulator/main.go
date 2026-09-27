// imap-emulator is a headless SMTP and IMAP test mail server.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/johlo/imap-emulator/internal/emulator"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := emulator.Run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
