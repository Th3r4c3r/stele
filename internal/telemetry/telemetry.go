// Package telemetry projects newplat detail responses into the
// vehicle_telemetry table (one row per VIN, upserted). See ADR-014.
//
// Service.Sync is the single entry point used by web handlers; it
// fetches via newplat.Client, maps the typed Detail into table
// columns, and upserts. Errors are typed so handlers can show
// targeted messages (token expired vs VIN unknown vs newplat down).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Th3r4c3r/stele/internal/newplat"
)

// Snapshot mirrors a vehicle_telemetry row, read side. Nullable
// fields use Go zero values via *T pointers where the UI needs to
// distinguish "not present" from "zero".
type Snapshot struct {
	VIN              string
	SnapshotAt       time.Time
	IsOnline         bool
	IMEI             *string
	ICCID            *string
	SimEndTime       *time.Time
	AgreementEndTime *time.Time
	LastOnlineAt     *time.Time
	LoginTime        *time.Time
	SOCPct           *int
	EnduranceKm      *int
	TotalMileageKm   *float64
	Latitude         *float64
	Longitude        *float64
	BMSTemperature   *int
	GSMSignal        *int
	GPSSatellites    *int
	FotaVersion      *string
}

// ErrNotFound is returned by Repo.ByVIN when no snapshot exists yet.
var ErrNotFound = errors.New("telemetry: no snapshot for VIN")

// Repo persists and reads vehicle_telemetry rows.
type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Service ties the newplat client + repo together for one-call sync.
type Service struct {
	client *newplat.Client
	repo   *Repo
}

func NewService(client *newplat.Client, repo *Repo) *Service {
	return &Service{client: client, repo: repo}
}

// Sync fetches one VIN from newplat and upserts the snapshot.
// Returns the freshly-stored Snapshot on success. The newplat
// sentinel errors (ErrTokenInvalid, ErrNotFound) bubble up unchanged
// so the caller can distinguish "stop the batch" from "skip this one".
func (s *Service) Sync(ctx context.Context, vin string) (*Snapshot, error) {
	detail, err := s.client.FetchVIN(ctx, vin)
	if err != nil {
		return nil, err
	}
	snap := projectDetail(detail)
	if err := s.repo.Upsert(ctx, snap, detail.RawPayload); err != nil {
		return nil, fmt.Errorf("telemetry.Sync upsert: %w", err)
	}
	return snap, nil
}

// SyncResult summarises a batch sync run for the UI.
type SyncResult struct {
	Attempted int
	Synced    int
	NotFound  int
	Errors    []SyncError
	// TokenExpired is true if newplat ever returned 401/403 in the
	// batch. The handler short-circuits and surfaces a banner.
	TokenExpired bool
}

// SyncError reports one failure within a batch.
type SyncError struct {
	VIN    string
	Reason string
}

// SyncBatch syncs every VIN in vins, accumulating per-VIN outcomes.
// Stops early if the token is invalidated (no point burning the rest
// of the batch when every call will 401). Respects ctx cancellation.
func (s *Service) SyncBatch(ctx context.Context, vins []string) SyncResult {
	var r SyncResult
	for _, vin := range vins {
		if ctx.Err() != nil {
			r.Errors = append(r.Errors, SyncError{VIN: vin, Reason: "context cancelled"})
			break
		}
		r.Attempted++
		_, err := s.Sync(ctx, vin)
		switch {
		case errors.Is(err, newplat.ErrTokenInvalid):
			r.TokenExpired = true
			r.Errors = append(r.Errors, SyncError{VIN: vin, Reason: "token expired"})
			return r // short-circuit: don't burn the batch.
		case errors.Is(err, newplat.ErrNotFound):
			r.NotFound++
		case err != nil:
			// Per-VIN errors are easy to miss when summarised in the
			// audit row ("1 errors"). Log each one so post-mortems
			// have a paper trail; the audit row is for the overview.
			slog.Error("telemetry.SyncBatch per-VIN failure",
				"vin", vin, "err", err)
			r.Errors = append(r.Errors, SyncError{VIN: vin, Reason: err.Error()})
		default:
			r.Synced++
		}
	}
	return r
}

// projectDetail maps the newplat Detail into a Snapshot. All time
// fields are normalised to UTC because newplat returns local-ish
// strings; storing UTC keeps comparisons sane.
func projectDetail(d *newplat.Detail) *Snapshot {
	s := &Snapshot{
		VIN:        d.VIN,
		SnapshotAt: time.Now().UTC(),
		IsOnline:   d.IsOnline,
	}
	if d.Device != nil {
		s.IMEI = strPtr(d.Device.DeviceNo)
		s.ICCID = strPtr(d.Device.ICCID)
		if t := newplat.ParseNewplatTime(d.Device.SimEndTime); !t.IsZero() {
			s.SimEndTime = &t
		}
		if t := newplat.ParseNewplatTime(d.Device.AgreementEndTime); !t.IsZero() {
			s.AgreementEndTime = &t
		}
	}
	if d.Pojo != nil {
		if t := newplat.ParseNewplatTime(d.Pojo.LastGpsTime); !t.IsZero() {
			s.LastOnlineAt = &t
		}
		if t := newplat.ParseNewplatTime(d.Pojo.LoginTime); !t.IsZero() {
			s.LoginTime = &t
		}
		s.SOCPct = intPtr(d.Pojo.NowElec)
		s.EnduranceKm = intPtr(d.Pojo.Endurance)
		if d.Pojo.Latitude != 0 {
			lat := d.Pojo.Latitude
			s.Latitude = &lat
		}
		if d.Pojo.Longitude != 0 {
			lon := d.Pojo.Longitude
			s.Longitude = &lon
		}
		s.BMSTemperature = intPtr(d.Pojo.BmsTemperature)
		s.GSMSignal = intPtr(d.Pojo.Gsm)
		s.GPSSatellites = intPtr(d.Pojo.Gps)
		s.FotaVersion = strPtr(d.Pojo.FotaVer)
	}
	if d.CarBaseInfo.TotalMileage > 0 {
		mi := d.CarBaseInfo.TotalMileage
		s.TotalMileageKm = &mi
	}
	return s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}

// --- Repo ---

// Upsert persists snap. raw is the full newplat detail.data node
// stored in raw_payload for future re-projection.
func (r *Repo) Upsert(ctx context.Context, s *Snapshot, raw []byte) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vehicle_telemetry (
			vin, snapshot_at, is_online, imei, iccid,
			sim_end_time, agreement_end_time, last_online_at, login_time,
			soc_pct, endurance_km, total_mileage_km,
			latitude, longitude, bms_temperature, gsm_signal, gps_satellites,
			fota_version, raw_payload
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (vin) DO UPDATE SET
			snapshot_at        = EXCLUDED.snapshot_at,
			is_online          = EXCLUDED.is_online,
			imei               = EXCLUDED.imei,
			iccid              = EXCLUDED.iccid,
			sim_end_time       = EXCLUDED.sim_end_time,
			agreement_end_time = EXCLUDED.agreement_end_time,
			last_online_at     = EXCLUDED.last_online_at,
			login_time         = EXCLUDED.login_time,
			soc_pct            = EXCLUDED.soc_pct,
			endurance_km       = EXCLUDED.endurance_km,
			total_mileage_km   = EXCLUDED.total_mileage_km,
			latitude           = EXCLUDED.latitude,
			longitude          = EXCLUDED.longitude,
			bms_temperature    = EXCLUDED.bms_temperature,
			gsm_signal         = EXCLUDED.gsm_signal,
			gps_satellites     = EXCLUDED.gps_satellites,
			fota_version       = EXCLUDED.fota_version,
			raw_payload        = EXCLUDED.raw_payload
	`, s.VIN, s.SnapshotAt, s.IsOnline, s.IMEI, s.ICCID,
		s.SimEndTime, s.AgreementEndTime, s.LastOnlineAt, s.LoginTime,
		s.SOCPct, s.EnduranceKm, s.TotalMileageKm,
		s.Latitude, s.Longitude, s.BMSTemperature, s.GSMSignal, s.GPSSatellites,
		s.FotaVersion, raw)
	if err != nil {
		return fmt.Errorf("telemetry.Upsert %s: %w", s.VIN, err)
	}
	return nil
}

// ByVIN returns the snapshot for vin, or ErrNotFound.
func (r *Repo) ByVIN(ctx context.Context, vin string) (*Snapshot, error) {
	var s Snapshot
	err := r.pool.QueryRow(ctx, selectColumns+` WHERE vin = $1`, vin).Scan(
		&s.VIN, &s.SnapshotAt, &s.IsOnline, &s.IMEI, &s.ICCID,
		&s.SimEndTime, &s.AgreementEndTime, &s.LastOnlineAt, &s.LoginTime,
		&s.SOCPct, &s.EnduranceKm, &s.TotalMileageKm,
		&s.Latitude, &s.Longitude, &s.BMSTemperature, &s.GSMSignal, &s.GPSSatellites,
		&s.FotaVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry.ByVIN: %w", err)
	}
	return &s, nil
}

// ListRecent returns up to limit snapshots, newest first.
func (r *Repo) ListRecent(ctx context.Context, limit int) ([]Snapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, selectColumns+` ORDER BY snapshot_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("telemetry.ListRecent: %w", err)
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(
			&s.VIN, &s.SnapshotAt, &s.IsOnline, &s.IMEI, &s.ICCID,
			&s.SimEndTime, &s.AgreementEndTime, &s.LastOnlineAt, &s.LoginTime,
			&s.SOCPct, &s.EnduranceKm, &s.TotalMileageKm,
			&s.Latitude, &s.Longitude, &s.BMSTemperature, &s.GSMSignal, &s.GPSSatellites,
			&s.FotaVersion,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CaseVINs returns the distinct VINs that appear on open (triage or
// classified) cases. Used by the "sync case VINs" batch trigger.
func (r *Repo) CaseVINs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT vin
		FROM current_cases
		WHERE status IN ('triage', 'classified') AND vin IS NOT NULL
		ORDER BY vin
	`)
	if err != nil {
		return nil, fmt.Errorf("telemetry.CaseVINs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const selectColumns = `
	SELECT vin, snapshot_at, is_online, imei, iccid,
	       sim_end_time, agreement_end_time, last_online_at, login_time,
	       soc_pct, endurance_km, total_mileage_km,
	       latitude, longitude, bms_temperature, gsm_signal, gps_satellites,
	       fota_version
	FROM vehicle_telemetry`
