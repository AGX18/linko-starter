package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	// logger.Printf("Linko is running on http://localhost:%d", *httpPort)
	cancel()
	// logger.Printf("Linko is shutting down")
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	var loggerStandard = log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
	logsFile, err := os.OpenFile("linko.access.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		loggerStandard.Printf("failed to open log file: %v", err)
		return 1
	}
	defer logsFile.Close()
	var logger = log.New(logsFile, "INFO: ", log.LstdFlags)
	var loggerAccess = log.New(logsFile, "INFO: ", log.LstdFlags)
	st, err := store.New(dataDir, loggerStandard)
	if err != nil {
		loggerStandard.Printf("failed to create store: %v", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, loggerAccess)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()
	logger.Printf("Linko is running on http://localhost:%d", httpPort)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Printf("failed to shutdown server: %v", err)
		return 1
	}
	if serverErr != nil {
		logger.Printf("server error: %v", serverErr)
		return 1
	}
	return 0
}
