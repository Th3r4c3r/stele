// Package dashboard computes the KPIs and tables shown on /dashboard.
// One Service method per visual block; the handler stitches them
// together. No cache: queries are cheap (< 50 ms at current volume).
//
// See docs/adr/0012-dashboard-cleanup.md.
package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// KPIs is the four-card header.
type KPIs struct {
	TotalOpen   int
	MyOpen      int
	OpenedLast7 int
	ClosedLast7 int
}

// KindCount is one row in the classification-mix table.
type KindCount struct {
	Kind  string
	Count int
}

// QueueRow is one row in the assignee queue table.
type QueueRow struct {
	UserID   uuid.UUID
	UserName string
	Role     string
	Open     int
}

// DealerRow is one row in the top-dealers table.
type DealerRow struct {
	Dealer string
	Region string
	Open   int
	Closed int
	Total  int
}

// DayActivity is one bar in the activity sparkline (last N days).
type DayActivity struct {
	Day   time.Time // midnight UTC
	Count int
}

// KPIs builds the four headline numbers for the current user.
func (s *Service) KPIs(ctx context.Context, currentUserID uuid.UUID) (KPIs, error) {
	var k KPIs
	weekAgo := time.Now().UTC().Add(-7 * 24 * time.Hour)

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM current_cases WHERE status IN ('triage','classified')`,
	).Scan(&k.TotalOpen); err != nil {
		return k, fmt.Errorf("dashboard.KPIs total_open: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM current_cases
		WHERE status IN ('triage','classified') AND assignee_id = $1
	`, currentUserID).Scan(&k.MyOpen); err != nil {
		return k, fmt.Errorf("dashboard.KPIs my_open: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM current_cases WHERE opened_at >= $1
	`, weekAgo).Scan(&k.OpenedLast7); err != nil {
		return k, fmt.Errorf("dashboard.KPIs opened_last7: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM current_cases WHERE closed_at >= $1
	`, weekAgo).Scan(&k.ClosedLast7); err != nil {
		return k, fmt.Errorf("dashboard.KPIs closed_last7: %w", err)
	}
	return k, nil
}

// ClassificationMix returns the kind distribution across all cases
// that have been classified (closed or open). NULL kind (cases
// closed without classification) is folded as "unclassified".
func (s *Service) ClassificationMix(ctx context.Context) ([]KindCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(kind, 'unclassified'), count(*)
		FROM current_cases
		GROUP BY 1
		ORDER BY 2 DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("dashboard.ClassificationMix: %w", err)
	}
	defer rows.Close()
	var out []KindCount
	for rows.Next() {
		var k KindCount
		if err := rows.Scan(&k.Kind, &k.Count); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// QueuePerAssignee returns each active user's open case count,
// sorted desc. Inactive users with zero open are omitted.
func (s *Service) QueuePerAssignee(ctx context.Context) ([]QueueRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.name, u.role, COUNT(c.id) AS open_count
		FROM users u
		LEFT JOIN current_cases c
		       ON c.assignee_id = u.id
		      AND c.status IN ('triage','classified')
		WHERE u.deactivated_at IS NULL
		GROUP BY u.id, u.name, u.role
		ORDER BY open_count DESC, u.name
	`)
	if err != nil {
		return nil, fmt.Errorf("dashboard.QueuePerAssignee: %w", err)
	}
	defer rows.Close()
	var out []QueueRow
	for rows.Next() {
		var q QueueRow
		if err := rows.Scan(&q.UserID, &q.UserName, &q.Role, &q.Open); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// TopDealersLast30 returns the dealers with most cases in the last
// 30 days, plus their region. Limited to 5 rows.
func (s *Service) TopDealersLast30(ctx context.Context) ([]DealerRow, error) {
	since := time.Now().UTC().Add(-30 * 24 * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT c.dealer,
		       COALESCE(d.region, '?')                                              AS region,
		       SUM(CASE WHEN c.status IN ('triage','classified') THEN 1 ELSE 0 END) AS open_n,
		       SUM(CASE WHEN c.status = 'closed'                  THEN 1 ELSE 0 END) AS closed_n,
		       count(*)                                                              AS total_n
		FROM current_cases c
		LEFT JOIN dealers d ON d.code = c.dealer
		WHERE c.opened_at >= $1
		GROUP BY c.dealer, d.region
		ORDER BY total_n DESC
		LIMIT 5
	`, since)
	if err != nil {
		return nil, fmt.Errorf("dashboard.TopDealersLast30: %w", err)
	}
	defer rows.Close()
	var out []DealerRow
	for rows.Next() {
		var d DealerRow
		if err := rows.Scan(&d.Dealer, &d.Region, &d.Open, &d.Closed, &d.Total); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ActivityLast7Days returns one row per day for the last 7 days,
// filled with 0 where no events were recorded. Useful for the
// sparkline.
func (s *Service) ActivityLast7Days(ctx context.Context) ([]DayActivity, error) {
	rows, err := s.pool.Query(ctx, `
		WITH days AS (
		    SELECT date_trunc('day', now()) - (n || ' days')::interval AS day
		    FROM generate_series(0, 6) AS n
		)
		SELECT days.day AT TIME ZONE 'UTC' AS day,
		       COALESCE(count(e.id), 0)    AS n
		FROM days
		LEFT JOIN events e
		       ON e.recorded_at >= days.day
		      AND e.recorded_at <  days.day + interval '1 day'
		GROUP BY days.day
		ORDER BY days.day
	`)
	if err != nil {
		return nil, fmt.Errorf("dashboard.ActivityLast7Days: %w", err)
	}
	defer rows.Close()
	var out []DayActivity
	for rows.Next() {
		var d DayActivity
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
