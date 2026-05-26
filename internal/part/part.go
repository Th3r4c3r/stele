// Package part holds the parts master data for the pilot.
// See docs/adr/0013-pilot-vmoto.md.
package part

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Part mirrors a parts row.
type Part struct {
	PN           string
	Description  string
	Category     *string
	PriceEUR     *float64 // dealer reference price
	SupersedesPN *string
}

var ErrNotFound = errors.New("part: not found")

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) ByPN(ctx context.Context, pn string) (Part, error) {
	var p Part
	err := r.pool.QueryRow(ctx, `
		SELECT pn, description, category, price_eur, supersedes_pn
		FROM parts WHERE pn = $1
	`, pn).Scan(&p.PN, &p.Description, &p.Category, &p.PriceEUR, &p.SupersedesPN)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("part.ByPN: %w", err)
	}
	return p, nil
}

func (r *Repo) List(ctx context.Context) ([]Part, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT pn, description, category, price_eur, supersedes_pn
		FROM parts ORDER BY pn
	`)
	if err != nil {
		return nil, fmt.Errorf("part.List: %w", err)
	}
	defer rows.Close()
	var out []Part
	for rows.Next() {
		var p Part
		if err := rows.Scan(&p.PN, &p.Description, &p.Category, &p.PriceEUR, &p.SupersedesPN); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM parts`).Scan(&n)
	return n, err
}

func (r *Repo) Upsert(ctx context.Context, p Part) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO parts (pn, description, category, price_eur, supersedes_pn)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (pn) DO UPDATE
		   SET description = EXCLUDED.description,
		       category    = EXCLUDED.category,
		       price_eur   = EXCLUDED.price_eur,
		       supersedes_pn = EXCLUDED.supersedes_pn
	`, p.PN, p.Description, p.Category, p.PriceEUR, p.SupersedesPN)
	if err != nil {
		return fmt.Errorf("part.Upsert %s: %w", p.PN, err)
	}
	return nil
}

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

// ImportCSV reads header: pn,description,category,price_eur,supersedes_pn
// Required: pn, description.
func (r *Repo) ImportCSV(ctx context.Context, body io.Reader) (ImportReport, error) {
	var rep ImportReport
	cr := csv.NewReader(body)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return rep, fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, req := range []string{"pn", "description"} {
		if _, ok := idx[req]; !ok {
			return rep, fmt.Errorf("missing required column: %s", req)
		}
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
		p := Part{
			PN:          strings.TrimSpace(rec[idx["pn"]]),
			Description: strings.TrimSpace(rec[idx["description"]]),
		}
		if p.PN == "" || p.Description == "" {
			rep.Errors = append(rep.Errors, ImportError{Line: line, Reason: "pn and description required"})
			rep.RowsSkipped++
			continue
		}
		if c, ok := optCell(rec, idx, "category"); ok {
			p.Category = &c
		}
		if pr, ok := optCell(rec, idx, "price_eur"); ok {
			if f, perr := strconv.ParseFloat(pr, 64); perr == nil {
				p.PriceEUR = &f
			}
		}
		if s, ok := optCell(rec, idx, "supersedes_pn"); ok {
			p.SupersedesPN = &s
		}
		existed, _ := r.exists(ctx, p.PN)
		if err := r.Upsert(ctx, p); err != nil {
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

func (r *Repo) exists(ctx context.Context, pn string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM parts WHERE pn = $1`, pn).Scan(&n)
	return n > 0, err
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
