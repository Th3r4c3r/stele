package analytics

import (
	"context"
	"fmt"
)

// StageDurationRow is one row of the "time in stage" view.
// CompletedVisits counts how many times a case has left this stage
// (i.e., has a successor transition); in-progress visits to the
// current stage are excluded so the averages reflect closed history.
type StageDurationRow struct {
	Stage           string
	CompletedVisits int
	AvgDays         float64
	MedianDays      float64
	P90Days         float64
}

// CycleTimeRow is one row of the open-to-closed key-to-key view.
// Kind is optional ("" for the overall row); the handler renders an
// overall summary plus a per-kind breakdown.
type CycleTimeRow struct {
	Kind         string
	ClosedCases  int
	AvgDays      float64
	MedianDays   float64
	P90Days      float64
}

// StuckRow is one row of the "currently stuck" view: per-stage
// counts of OPEN cases whose stage has not advanced in a while.
type StuckRow struct {
	Stage          string
	OpenCases      int
	AvgDaysStuck   float64
	MaxDaysStuck   float64
}

// StageDurations returns one row per stage with avg/median/p90 of
// time-in-stage across COMPLETED visits. Reads events directly so
// historical stages (including "new") are covered uniformly.
func (s *Service) StageDurations(ctx context.Context) ([]StageDurationRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH transitions AS (
		    SELECT aggregate_id AS case_id,
		           occurred_at  AS at,
		           id           AS event_id,
		           CASE
		               WHEN type = 'CaseOpened'   THEN 'new'
		               WHEN type = 'StageChanged' THEN payload->>'to'
		           END AS entered_stage
		    FROM events
		    WHERE type IN ('CaseOpened', 'StageChanged')
		),
		with_next AS (
		    SELECT case_id, entered_stage, at,
		           LEAD(at) OVER (
		               PARTITION BY case_id ORDER BY at, event_id
		           ) AS next_at
		    FROM transitions
		),
		completed AS (
		    SELECT entered_stage AS stage,
		           EXTRACT(EPOCH FROM (next_at - at)) / 86400.0 AS days
		    FROM with_next
		    WHERE next_at IS NOT NULL
		      AND entered_stage IS NOT NULL
		)
		SELECT stage,
		       count(*) AS visits,
		       AVG(days)::numeric(10,2) AS avg_days,
		       (percentile_cont(0.5) WITHIN GROUP (ORDER BY days))::numeric(10,2) AS median_days,
		       (percentile_cont(0.9) WITHIN GROUP (ORDER BY days))::numeric(10,2) AS p90_days
		FROM completed
		GROUP BY stage
		ORDER BY CASE stage
		    WHEN 'new'           THEN 1
		    WHEN 'diagnosis'     THEN 2
		    WHEN 'parts_ordered' THEN 3
		    WHEN 'parts_waiting' THEN 4
		    WHEN 'repair'        THEN 5
		    WHEN 'resolved'      THEN 6
		    ELSE                       9
		END
	`)
	if err != nil {
		return nil, fmt.Errorf("analytics.StageDurations: %w", err)
	}
	defer rows.Close()
	var out []StageDurationRow
	for rows.Next() {
		var r StageDurationRow
		if err := rows.Scan(&r.Stage, &r.CompletedVisits, &r.AvgDays, &r.MedianDays, &r.P90Days); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CycleTime returns the key-to-key (opened → closed) stats for all
// closed cases, plus an optional per-kind breakdown. The "" kind row
// is the overall aggregate so the UI can show a headline number.
func (s *Service) CycleTime(ctx context.Context) ([]CycleTimeRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH closed AS (
		    SELECT COALESCE(kind, 'unclassified') AS kind,
		           EXTRACT(EPOCH FROM (closed_at - opened_at)) / 86400.0 AS days
		    FROM current_cases
		    WHERE status = 'closed' AND closed_at IS NOT NULL
		)
		SELECT ''   AS kind,
		       count(*),
		       AVG(days)::numeric(10,2),
		       (percentile_cont(0.5) WITHIN GROUP (ORDER BY days))::numeric(10,2),
		       (percentile_cont(0.9) WITHIN GROUP (ORDER BY days))::numeric(10,2)
		FROM closed
		UNION ALL
		SELECT kind,
		       count(*),
		       AVG(days)::numeric(10,2),
		       (percentile_cont(0.5) WITHIN GROUP (ORDER BY days))::numeric(10,2),
		       (percentile_cont(0.9) WITHIN GROUP (ORDER BY days))::numeric(10,2)
		FROM closed
		GROUP BY kind
		ORDER BY 1
	`)
	if err != nil {
		return nil, fmt.Errorf("analytics.CycleTime: %w", err)
	}
	defer rows.Close()
	var out []CycleTimeRow
	for rows.Next() {
		var r CycleTimeRow
		if err := rows.Scan(&r.Kind, &r.ClosedCases, &r.AvgDays, &r.MedianDays, &r.P90Days); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Stuck returns per-stage counts of OPEN cases that have not moved
// in a while, plus the average and maximum days since last
// transition. Closed cases are excluded.
func (s *Service) Stuck(ctx context.Context) ([]StuckRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT stage,
		       count(*),
		       AVG(EXTRACT(EPOCH FROM (now() - stage_changed_at)) / 86400.0)::numeric(10,2),
		       MAX(EXTRACT(EPOCH FROM (now() - stage_changed_at)) / 86400.0)::numeric(10,2)
		FROM current_cases
		WHERE status <> 'closed'
		GROUP BY stage
		ORDER BY CASE stage
		    WHEN 'new'           THEN 1
		    WHEN 'diagnosis'     THEN 2
		    WHEN 'parts_ordered' THEN 3
		    WHEN 'parts_waiting' THEN 4
		    WHEN 'repair'        THEN 5
		    WHEN 'resolved'      THEN 6
		    ELSE                       9
		END
	`)
	if err != nil {
		return nil, fmt.Errorf("analytics.Stuck: %w", err)
	}
	defer rows.Close()
	var out []StuckRow
	for rows.Next() {
		var r StuckRow
		if err := rows.Scan(&r.Stage, &r.OpenCases, &r.AvgDaysStuck, &r.MaxDaysStuck); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
