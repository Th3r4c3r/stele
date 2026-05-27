// Stele entrypoint. Single binary that serves HTTP and also supports
// the `replay` sub-command for projection rebuilds.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/auth"
	"github.com/Th3r4c3r/stele/internal/dealer"
	"github.com/Th3r4c3r/stele/internal/document"
	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/mail"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/internal/newplat"
	"github.com/Th3r4c3r/stele/internal/part"
	"github.com/Th3r4c3r/stele/internal/projection"
	"github.com/Th3r4c3r/stele/internal/telemetry"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/vehicle"
	"github.com/Th3r4c3r/stele/internal/web"
	"github.com/Th3r4c3r/stele/migrations"
)

const banner = "Stele alive"

// version is set via -ldflags at build time.
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "replay":
			os.Exit(runReplay(os.Args[2:]))
		case "-h", "--help", "help":
			printUsage()
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "unknown sub-command %q\n", os.Args[1])
			printUsage()
			os.Exit(2)
		}
	}

	os.Exit(runServer())
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  stele                       # run HTTP server")
	fmt.Fprintln(os.Stderr, "  stele replay <projector>    # rebuild one projection from scratch")
	fmt.Fprintln(os.Stderr, "  stele replay --all          # rebuild every registered projection")
}

func runServer() int {
	addr := envOr("STELE_ADDR", ":8080")
	dbURL := os.Getenv("STELE_DATABASE_URL")
	if dbURL == "" {
		slog.Error("STELE_DATABASE_URL not set")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("running migrations")
	if err := migrate.Up(migrations.FS, dbURL); err != nil {
		slog.Error("migrations failed", "err", err)
		return 1
	}

	pool, err := openPool(ctx, dbURL)
	if err != nil {
		slog.Error("db pool", "err", err)
		return 1
	}
	defer pool.Close()

	store := event.NewPostgresStore(pool)

	docsDir := envOr("STELE_DOCS_DIR", "/data/documents")
	docsMax, err := strconv.ParseInt(envOr("STELE_DOCS_MAX_BYTES", "26214400"), 10, 64)
	if err != nil {
		slog.Error("STELE_DOCS_MAX_BYTES invalid", "err", err)
		return 1
	}
	docStorage, err := document.NewStorage(docsDir, docsMax)
	if err != nil {
		slog.Error("documents storage", "err", err, "hint", "bind-mount a host dir to "+docsDir)
		return 1
	}

	runner := projection.NewRunner(store, pool)
	runner.Register(projection.EventCountByType())
	runner.Register(fault.CurrentCasesProjector())
	runner.Register(fault.CasePartsProjector(pool))
	runner.Register(document.CurrentDocumentsProjector(docStorage))
	runnerWG := runner.Start(ctx)
	slog.Info("projection runner started", "projectors", runner.Names())

	userRepo := userpkg.NewRepo(pool)
	dealerRepo := dealer.NewRepo(pool)
	vehicleRepo := vehicle.NewRepo(pool)
	partRepo := part.NewRepo(pool)
	resolver := fault.NewPgResolver(pool)

	secret := []byte(os.Getenv("STELE_SESSION_SECRET"))
	sessions, err := auth.NewSessions(pool, secret)
	if err != nil {
		slog.Error("session secret invalid", "err", err,
			"hint", "STELE_SESSION_SECRET must be at least 32 bytes; generate one with 'openssl rand -base64 32'")
		return 1
	}
	go func() {
		// Best-effort housekeeping; loss on restart is fine.
		if n, err := sessions.PurgeExpired(ctx); err == nil && n > 0 {
			slog.Info("sessions purged on boot", "n", n)
		}
	}()
	resets := auth.NewResetTokens(pool)
	rateLimit := auth.NewLoginRateLimit()
	mailer := mail.FromEnv()
	baseURL := envOr("STELE_BASE_URL", "https://stele.178-105-44-164.nip.io")

	// Telemetry (newplat) is optional. Three configuration modes:
	//   1. STELE_NEWPLAT_ACCOUNT + _PASSWORD set: auto-refresh client,
	//      Logs in at first use, re-logs on 401. STELE_NEWPLAT_TOKEN
	//      may be empty or pre-seeded to skip the first round-trip.
	//   2. Only STELE_NEWPLAT_TOKEN set: static token, no refresh.
	//      Manual rotation when it expires.
	//   3. Neither: telemetry routes disabled; rest of app unchanged.
	telemetryRepo := telemetry.NewRepo(pool)
	var telemetrySvc *telemetry.Service
	switch {
	case os.Getenv("STELE_NEWPLAT_ACCOUNT") != "" && os.Getenv("STELE_NEWPLAT_PASSWORD") != "":
		client := newplat.NewWithCredentials(
			os.Getenv("STELE_NEWPLAT_TOKEN"),
			newplat.Credentials{
				CustomerName: envOr("STELE_NEWPLAT_CUSTOMER", "VMOTO"),
				CustomerID:   envOr("STELE_NEWPLAT_CUSTOMER_ID", "1"),
				UserAccount:  os.Getenv("STELE_NEWPLAT_ACCOUNT"),
				UserPassword: os.Getenv("STELE_NEWPLAT_PASSWORD"),
			})
		telemetrySvc = telemetry.NewService(client, telemetryRepo)
		slog.Info("telemetry: newplat client with auto-refresh configured",
			"account", os.Getenv("STELE_NEWPLAT_ACCOUNT"))
	case os.Getenv("STELE_NEWPLAT_TOKEN") != "":
		telemetrySvc = telemetry.NewService(newplat.New(os.Getenv("STELE_NEWPLAT_TOKEN")), telemetryRepo)
		slog.Info("telemetry: newplat client with static token configured (no auto-refresh)")
	default:
		slog.Info("telemetry: no newplat credentials, /admin/telemetry disabled")
	}

	mux := http.NewServeMux()
	web.Mount(mux, web.Deps{
		Pool:         pool,
		Store:        store,
		Resolver:     resolver,
		Users:        userRepo,
		Dealers:      dealerRepo,
		Vehicles:     vehicleRepo,
		Parts:        partRepo,
		Sessions:     sessions,
		Resets:       resets,
		RateLimit:    rateLimit,
		MailSender:   mailer,
		DocStore:     docStorage,
		Telemetry:    telemetryRepo,
		TelemetrySvc: telemetrySvc,
		BaseURL:      baseURL,
	})
	// Operational endpoint (public, no auth). Debug endpoints removed
	// in M7 — the dashboard + replay command cover their use cases.
	mux.HandleFunc("GET /healthz", healthzHandler(pool))

	srv := &http.Server{
		Addr:              addr,
		Handler:           withRequestLog(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("stele listening", "addr", addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
		return 1
	}
	runnerWG.Wait()
	return 0
}

func runReplay(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "replay: missing projector name (or --all)")
		printUsage()
		return 2
	}
	dbURL := os.Getenv("STELE_DATABASE_URL")
	if dbURL == "" {
		slog.Error("STELE_DATABASE_URL not set")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := migrate.Up(migrations.FS, dbURL); err != nil {
		slog.Error("migrations failed", "err", err)
		return 1
	}
	pool, err := openPool(ctx, dbURL)
	if err != nil {
		slog.Error("db pool", "err", err)
		return 1
	}
	defer pool.Close()

	store := event.NewPostgresStore(pool)
	runner := projection.NewRunner(store, pool)
	runner.Register(projection.EventCountByType())
	runner.Register(fault.CurrentCasesProjector())
	runner.Register(fault.CasePartsProjector(pool))
	// replay also needs the storage so DocumentRedacted events unlink
	// the file on disk (idempotent: missing-file is treated as no-op).
	replayDocs, err := document.NewStorage(
		envOr("STELE_DOCS_DIR", "/data/documents"),
		26214400,
	)
	if err != nil {
		slog.Error("replay docs storage", "err", err)
		return 1
	}
	runner.Register(document.CurrentDocumentsProjector(replayDocs))

	targets := args
	if args[0] == "--all" {
		targets = runner.Names()
	}
	for _, name := range targets {
		slog.Info("replaying", "projector", name)
		if err := runner.ResetCursor(ctx, name); err != nil {
			slog.Error("reset cursor", "projector", name, "err", err)
			return 1
		}
		if err := runner.RunOnce(ctx, name); err != nil {
			slog.Error("replay run", "projector", name, "err", err)
			return 1
		}
		slog.Info("replay complete", "projector", name)
	}
	return 0
}

func openPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if m := os.Getenv("STELE_DB_POOL_MAX"); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil {
			return nil, fmt.Errorf("STELE_DB_POOL_MAX: %w", err)
		}
		cfg.MaxConns = int32(n)
	} else {
		cfg.MaxConns = 10
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

func healthzHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "db unreachable: %v\n", err)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
