package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkerr"
	"boot.dev/linko/internal/store"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

type multiError interface {
	error
	Unwrap() []error
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logger, closeLogger, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))

	env := os.Getenv("ENV")
	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", env),
		slog.String("hostname", hostname),
	)

	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		return 1
	}
	defer func() {
		if err := closeLogger(); err != nil {
			logger.Error("failed to close logger", slog.String("err", err.Error()))
		}
	}()

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error("failed to create store", slog.String("err", err.Error()))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()
	logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", httpPort), slog.Int("port", httpPort))

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server", slog.String("err", err.Error()))
		return 1
	}
	if serverErr != nil {
		logger.Error("server error", slog.String("err", serverErr.Error()))
		return 1
	}
	return 0
}

type closeFunc func() error

func initializeLogger(logFile string) (*slog.Logger, closeFunc, error) {
	noColor := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
	debugHandler := tint.NewHandler(os.Stderr, &tint.Options{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor:     noColor,
	})

	var infoHandler slog.Handler

	var logger *slog.Logger

	var loggerfile *lumberjack.Logger

	if logFile == "" {
		logger = slog.New(debugHandler)
	} else {
		loggerfile = &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}
		infoHandler = slog.NewJSONHandler(loggerfile, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})
		logger = slog.New(slog.NewMultiHandler(
			debugHandler,
			infoHandler,
		))
	}

	return logger, func() error {
		if logFile == "" {
			return nil
		}
		loggerfile.Close()
		return nil
	}, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}

		base := []slog.Attr{}

		if err2, ok := errors.AsType[multiError](err); ok {
			for i, subErr := range err2.Unwrap() {
				base = append(base, slog.GroupAttrs(
					fmt.Sprintf("error_%d", i+1),
					errorAttrs(subErr)...))
			}
		} else {
			base = errorAttrs(err)
			out := slog.GroupAttrs("error", base...)
			fmt.Fprintf(os.Stderr, "OUT key=%q kind=%v len(base)=%d\n", out.Key, out.Value.Kind(), len(base))
			return out
		}
		return slog.GroupAttrs("errors", base...)
	}

	return a
}

func errorAttrs(err error) []slog.Attr {
	if err == nil {
		return []slog.Attr{}
	}
	// build and return attrs for a single error
	base := []slog.Attr{slog.String("message", err.Error())}

	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		base = append(base, slog.String("stack_trace", fmt.Sprintf("%+v", stackErr.StackTrace())))
	}
	attrs := linkerr.Attrs(err)
	return append(base, attrs...)
}
