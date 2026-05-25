// Stele entrypoint. Single binary that serves HTTP and also supports
// the `replay` sub-command for projection rebuilds.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/event"
	"github.com/Th3r4c3r/stele/internal/fault"
	"github.com/Th3r4c3r/stele/internal/migrate"
	"github.com/Th3r4c3r/stele/internal/projection"
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

	runner := projection.NewRunner(store, pool)
	runner.Register(projection.EventCountByType())
	runner.Register(fault.CurrentCasesProjector())
	runnerWG := runner.Start(ctx)
	slog.Info("projection runner started", "projectors", runner.Names())

	mux := http.NewServeMux()
	// Warranty UI (/, /claims/...) plus static assets.
	web.Mount(mux, pool, store)
	// Operational + debug endpoints retained from M1.
	mux.HandleFunc("GET /healthz", healthzHandler(pool))
	mux.HandleFunc("POST /debug/event", appendDebugEvent(store))
	mux.HandleFunc("GET /debug/events", listDebugEvents(store))
	mux.HandleFunc("GET /debug/projections", listProjections(pool))

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

type debugEventReq struct {
	AggregateType string          `json:"aggregate_type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

func appendDebugEvent(store *event.PostgresStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req debugEventReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.AggregateType == "" || req.Type == "" {
			http.Error(w, "aggregate_type and type are required", http.StatusBadRequest)
			return
		}
		if req.AggregateID == uuid.Nil {
			req.AggregateID = uuid.Must(uuid.NewV7())
		}
		ev := event.Event{
			AggregateType: req.AggregateType,
			AggregateID:   req.AggregateID,
			Type:          req.Type,
			Payload:       req.Payload,
			OccurredAt:    req.OccurredAt,
		}
		evs := []event.Event{ev}
		if err := store.Append(r.Context(), evs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(evs[0])
	}
}

func listDebugEvents(store *event.PostgresStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		var out []event.Event
		for ev, err := range store.Stream(r.Context(), event.StreamOptions{BatchSize: limit}) {
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, ev)
			if len(out) >= limit {
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

type projectionCount struct {
	AggregateType string `json:"aggregate_type"`
	Type          string `json:"type"`
	Count         int64  `json:"count"`
}

func listProjections(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), `
			SELECT aggregate_type, type, count
			FROM projection_event_counts
			ORDER BY aggregate_type, type
		`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []projectionCount{}
		for rows.Next() {
			var pc projectionCount
			if err := rows.Scan(&pc.AggregateType, &pc.Type, &pc.Count); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, pc)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
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
