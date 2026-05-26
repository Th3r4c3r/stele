// Package vehicle holds the vehicle + model master data for the pilot.
// See docs/adr/0013-pilot-vmoto.md.
package vehicle

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Model mirrors a vehicle_models row.
type Model struct {
	Code        string
	Name        string
	Generation  *string
	Segment     *string
	CapacityKWh *float64
}

// Vehicle mirrors a vehicles row enriched with the model name when joined.
type Vehicle struct {
	VIN              string
	ModelCode        string
	ModelName        string // populated on join lookups
	ManufacturedYear *int
	SoldAt           *time.Time
	Country          *string
}

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("vehicle: not found")

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// ByVIN returns the vehicle with its joined model name.
func (r *Repo) ByVIN(ctx context.Context, vin string) (Vehicle, error) {
	var v Vehicle
	err := r.pool.QueryRow(ctx, `
		SELECT v.vin, v.model_code, m.name, v.manufactured_year, v.sold_at, v.country
		FROM vehicles v
		JOIN vehicle_models m ON m.code = v.model_code
		WHERE v.vin = $1
	`, vin).Scan(&v.VIN, &v.ModelCode, &v.ModelName, &v.ManufacturedYear, &v.SoldAt, &v.Country)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, fmt.Errorf("vehicle.ByVIN: %w", err)
	}
	return v, nil
}

// ListModels returns all model rows ordered by code (small list).
func (r *Repo) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT code, name, generation, segment, capacity_kwh
		FROM vehicle_models ORDER BY code
	`)
	if err != nil {
		return nil, fmt.Errorf("vehicle.ListModels: %w", err)
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.Code, &m.Name, &m.Generation, &m.Segment, &m.CapacityKWh); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountVehicles returns total fleet size (master), used by analytics.
func (r *Repo) CountVehicles(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM vehicles`).Scan(&n)
	return n, err
}

// UpsertModel inserts or updates a model row by code. Idempotent.
func (r *Repo) UpsertModel(ctx context.Context, m Model) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vehicle_models (code, name, generation, segment, capacity_kwh)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (code) DO UPDATE
		   SET name = EXCLUDED.name,
		       generation = EXCLUDED.generation,
		       segment = EXCLUDED.segment,
		       capacity_kwh = EXCLUDED.capacity_kwh
	`, m.Code, m.Name, m.Generation, m.Segment, m.CapacityKWh)
	if err != nil {
		return fmt.Errorf("vehicle.UpsertModel %s: %w", m.Code, err)
	}
	return nil
}

// UpsertVehicle inserts or updates a vehicle row by VIN. Idempotent.
func (r *Repo) UpsertVehicle(ctx context.Context, v Vehicle) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vehicles (vin, model_code, manufactured_year, sold_at, country)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (vin) DO UPDATE
		   SET model_code = EXCLUDED.model_code,
		       manufactured_year = EXCLUDED.manufactured_year,
		       sold_at = EXCLUDED.sold_at,
		       country = EXCLUDED.country
	`, v.VIN, v.ModelCode, v.ManufacturedYear, v.SoldAt, v.Country)
	if err != nil {
		return fmt.Errorf("vehicle.UpsertVehicle %s: %w", v.VIN, err)
	}
	return nil
}

// ImportReport summarises a CSV upload result.
type ImportReport struct {
	RowsInserted int
	RowsUpdated  int
	RowsSkipped  int
	Errors       []ImportError
}

type ImportError struct {
	Line   int
	Reason string
}

// ImportModelsCSV reads CSV with header: code,name,generation,segment,capacity_kwh
// All but code and name may be empty. Idempotent: re-uploading the same
// file produces 0 inserted + N updated.
func (r *Repo) ImportModelsCSV(ctx context.Context, body io.Reader) (ImportReport, error) {
	var rep ImportReport
	cr := csv.NewReader(body)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return rep, fmt.Errorf("read header: %w", err)
	}
	idx, err := headerIndex(header, []string{"code", "name"}, []string{"generation", "segment", "capacity_kwh"})
	if err != nil {
		return rep, err
	}
	line := 1
	for {
		line++
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: err.Error()})
			rep.RowsSkipped++
			continue
		}
		m := Model{
			Code: strings.TrimSpace(rec[idx["code"]]),
			Name: strings.TrimSpace(rec[idx["name"]]),
		}
		if m.Code == "" || m.Name == "" {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: "code and name required"})
			rep.RowsSkipped++
			continue
		}
		if g, ok := optCell(rec, idx, "generation"); ok {
			m.Generation = &g
		}
		if s, ok := optCell(rec, idx, "segment"); ok {
			m.Segment = &s
		}
		if c, ok := optCell(rec, idx, "capacity_kwh"); ok {
			if f, perr := strconv.ParseFloat(c, 64); perr == nil {
				m.CapacityKWh = &f
			}
		}
		existed, err := r.modelExists(ctx, m.Code)
		if err != nil {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: err.Error()})
			rep.RowsSkipped++
			continue
		}
		if err := r.UpsertModel(ctx, m); err != nil {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: err.Error()})
			rep.RowsSkipped++
			continue
		}
		if existed {
			rep.RowsUpdated++
		} else {
			rep.RowsInserted++
		}
	}
	return rep, nil
}

// ImportVehiclesCSV reads CSV: vin,model_code,manufactured_year,sold_at,country
func (r *Repo) ImportVehiclesCSV(ctx context.Context, body io.Reader) (ImportReport, error) {
	var rep ImportReport
	cr := csv.NewReader(body)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return rep, fmt.Errorf("read header: %w", err)
	}
	idx, err := headerIndex(header, []string{"vin", "model_code"}, []string{"manufactured_year", "sold_at", "country"})
	if err != nil {
		return rep, err
	}
	// Cache model codes once per import; bounded by master size.
	knownModels := map[string]bool{}
	for _, m := range mustList(r.ListModels(ctx)) {
		knownModels[m.Code] = true
	}
	line := 1
	for {
		line++
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: err.Error()})
			rep.RowsSkipped++
			continue
		}
		v := Vehicle{
			VIN:       strings.ToUpper(strings.TrimSpace(rec[idx["vin"]])),
			ModelCode: strings.TrimSpace(rec[idx["model_code"]]),
		}
		if len(v.VIN) != 17 {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: "VIN must be 17 chars"})
			rep.RowsSkipped++
			continue
		}
		if !knownModels[v.ModelCode] {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: "unknown model_code " + v.ModelCode})
			rep.RowsSkipped++
			continue
		}
		if y, ok := optCell(rec, idx, "manufactured_year"); ok {
			if n, perr := strconv.Atoi(y); perr == nil {
				v.ManufacturedYear = &n
			}
		}
		if s, ok := optCell(rec, idx, "sold_at"); ok {
			if t, perr := time.Parse("2006-01-02", s); perr == nil {
				v.SoldAt = &t
			}
		}
		if c, ok := optCell(rec, idx, "country"); ok {
			cc := strings.ToUpper(c)
			v.Country = &cc
		}
		existed, _ := r.vehicleExists(ctx, v.VIN)
		if err := r.UpsertVehicle(ctx, v); err != nil {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: err.Error()})
			rep.RowsSkipped++
			continue
		}
		if existed {
			rep.RowsUpdated++
		} else {
			rep.RowsInserted++
		}
	}
	return rep, nil
}

func (r *Repo) modelExists(ctx context.Context, code string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM vehicle_models WHERE code = $1`, code).Scan(&n)
	return n > 0, err
}

func (r *Repo) vehicleExists(ctx context.Context, vin string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM vehicles WHERE vin = $1`, vin).Scan(&n)
	return n > 0, err
}

func mustList(list []Model, err error) []Model {
	if err != nil {
		return nil
	}
	return list
}

// headerIndex maps the column names found in the CSV header to their
// positions. Required columns must all be present; optional ones may
// be absent (then optCell returns false for them).
func headerIndex(header []string, required, optional []string) (map[string]int, error) {
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, r := range required {
		if _, ok := idx[r]; !ok {
			return nil, fmt.Errorf("missing required column: %s", r)
		}
	}
	_ = optional
	return idx, nil
}

func optCell(rec []string, idx map[string]int, col string) (string, bool) {
	i, ok := idx[col]
	if !ok || i >= len(rec) {
		return "", false
	}
	v := strings.TrimSpace(rec[i])
	if v == "" {
		return "", false
	}
	return v, true
}
