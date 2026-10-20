package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/httpapi"
)

const serveUsage = "usage: rencrow-world serve --config PATH"

func runServe(args []string, _ io.Writer, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--config" || args[1] == "" {
		writeError(stderr, serveUsage)
		return 2
	}
	cfg, err := httpapi.LoadConfig(args[1])
	if err != nil {
		writeError(stderr, "invalid http configuration")
		return 1
	}
	srv, err := httpapi.New(cfg)
	if err != nil {
		writeError(stderr, "could not open the world store")
		return 1
	}
	defer srv.Close()

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		writeError(stderr, "could not listen on the configured address")
		return 1
	}
	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	fmt.Fprintf(stderr, "rencrow-world: listening on %s\n", listener.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			writeError(stderr, "http server stopped unexpectedly")
			return 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
	}
	return 0
}
