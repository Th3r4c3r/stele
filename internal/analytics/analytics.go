// Package analytics computes failure-rate and cost analytics over the
// case_parts + current_cases + vehicles join graph. Powers /analytics.
//
// All queries are read-only and run on the live read models. They are
// cheap at pilot scale (~40k vehicles, ~200 cases, < 1k case_parts):
// no caching layer is necessary yet. See ROADMAP M10.
package analytics

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// TopPNRow is one row of the "top failing PN per model" table.
type TopPNRow struct {
	ModelCode   string
	ModelName   string
	PN          string
	Description string
	CaseCount   int
	TotalQty    int
	TotalCost   float64
}

// FailureRateRow is one row of the "failure rate per fault_code per
// model+year" table. Per1000 = cases * 1000 / fleet, the canonical
// reliability metric.
type FailureRateRow struct {
	ModelCode        string
	ModelName        string
	ManufacturedYear int
	FaultCode        string
	Cases            int
	Fleet            int
	Per1000          float64
}

// AvgCostRow is one row of the "avg cost per kind/model" table.
// Replaced parts only: quotes are not realised cost.
type AvgCostRow struct {
	Kind       string
	ModelCode  string
	ModelName  string
	CaseCount  int
	AvgCostEUR float64
	TotalCost  float64
}

// TopFailingPN returns the (model, PN) combos with the most replaced
// case_parts in the last `days` days. Quotes are excluded: only
// physical replacements count as failures.
func (s *Service) TopFailingPN(ctx context.Context, days, limit int) ([]TopPNRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT vm.code, vm.name, cp.pn,
		       COALESCE(p.description, '') AS description,
		       COUNT(DISTINCT cp.case_id)  AS case_count,
		       SUM(cp.qty)                 AS total_qty,
		       SUM(cp.cost_at_event)       AS total_cost
		FROM case_parts cp
		JOIN current_cases cc  ON cc.id   = cp.case_id
		JOIN vehicles v        ON v.vin   = cc.vin
		JOIN vehicle_models vm ON vm.code = v.model_code
		LEFT JOIN parts p      ON p.pn    = cp.pn
		WHERE cp.kind = 'replaced'
		  AND cp.recorded_at >= now() - ($1::int || ' days')::interval
		GROUP BY vm.code, vm.name, cp.pn, p.description
		ORDER BY case_count DESC, total_cost DESC
		LIMIT $2
	`, days, limit)
	if err != nil {
		return nil, fmt.Errorf("analytics.TopFailingPN: %w", err)
	}
	defer rows.Close()
	var out []TopPNRow
	for rows.Next() {
		var r TopPNRow
		if err := rows.Scan(&r.ModelCode, &r.ModelName, &r.PN, &r.Description,
			&r.CaseCount, &r.TotalQty, &r.TotalCost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailureRateByFaultModel returns the top `limit` (model, year, fault)
// cohorts by per-1000-vehicles failure rate. Cohorts with empty fleet
// (no vehicles with a known manufactured_year) are excluded.
func (s *Service) FailureRateByFaultModel(ctx context.Context, limit int) ([]FailureRateRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH cohort AS (
		    SELECT model_code, manufactured_year, COUNT(*) AS fleet
		    FROM vehicles
		    WHERE manufactured_year IS NOT NULL
		    GROUP BY model_code, manufactured_year
		),
		incidents AS (
		    SELECT v.model_code, v.manufactured_year, cc.fault_code,
		           COUNT(*) AS cases
		    FROM current_cases cc
		    JOIN vehicles v ON v.vin = cc.vin
		    WHERE v.manufactured_year IS NOT NULL
		    GROUP BY v.model_code, v.manufactured_year, cc.fault_code
		)
		SELECT i.model_code, vm.name, i.manufactured_year, i.fault_code,
		       i.cases, c.fleet,
		       (i.cases::numeric * 1000.0 / c.fleet)::numeric(10,3) AS per_1000
		FROM incidents i
		JOIN cohort c        ON c.model_code = i.model_code AND c.manufactured_year = i.manufactured_year
		JOIN vehicle_models vm ON vm.code     = i.model_code
		ORDER BY per_1000 DESC, i.cases DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("analytics.FailureRateByFaultModel: %w", err)
	}
	defer rows.Close()
	var out []FailureRateRow
	for rows.Next() {
		var r FailureRateRow
		if err := rows.Scan(&r.ModelCode, &r.ModelName, &r.ManufacturedYear, &r.FaultCode,
			&r.Cases, &r.Fleet, &r.Per1000); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AvgCostByKindModel averages the replaced-parts cost per case for
// each (kind, model) combo. Quoted parts excluded: they are estimates
// the customer may never have accepted.
func (s *Service) AvgCostByKindModel(ctx context.Context, limit int) ([]AvgCostRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH per_case AS (
		    SELECT case_id, SUM(cost_at_event) AS total_cost
		    FROM case_parts
		    WHERE kind = 'replaced'
		    GROUP BY case_id
		)
		SELECT cc.kind, vm.code, vm.name,
		       COUNT(DISTINCT cc.id) AS case_count,
		       AVG(per_case.total_cost)::numeric(12,2) AS avg_cost,
		       SUM(per_case.total_cost)::numeric(12,2) AS total_cost
		FROM per_case
		JOIN current_cases cc  ON cc.id   = per_case.case_id
		JOIN vehicles v        ON v.vin   = cc.vin
		JOIN vehicle_models vm ON vm.code = v.model_code
		WHERE cc.kind IS NOT NULL
		GROUP BY cc.kind, vm.code, vm.name
		ORDER BY total_cost DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("analytics.AvgCostByKindModel: %w", err)
	}
	defer rows.Close()
	var out []AvgCostRow
	for rows.Next() {
		var r AvgCostRow
		if err := rows.Scan(&r.Kind, &r.ModelCode, &r.ModelName,
			&r.CaseCount, &r.AvgCostEUR, &r.TotalCost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
