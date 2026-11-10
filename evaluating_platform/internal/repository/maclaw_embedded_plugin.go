package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/maclaw"
)

// MaclawEmbeddedPluginRepository is the P28 state spine: one row per package
// (current state machine position + the safe auto-gate summary), an append-only
// event log, and the tester whitelist. Rejected packages live here with
// resource_id=” — the invalid payload is NEVER persisted (the store exit
// refused it), but the reason must still be visible to the expert for the
// revision loop.
type MaclawEmbeddedPluginRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawEmbeddedPluginRepository(pool *pgxpool.Pool) *MaclawEmbeddedPluginRepository {
	return &MaclawEmbeddedPluginRepository{pool: pool}
}

const embeddedPluginColumns = `owner_user_id, resource_id, package_id, name, summary, state, version, auto_gate,
	review_reason, reviewer_user_id, build_requested_by, build_requested_at,
	final_approved_by, final_approved_at, created_at, updated_at`

func (r *MaclawEmbeddedPluginRepository) Insert(ctx context.Context, row maclaw.EmbeddedPluginStateRow) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO maclaw_embedded_plugins (`+embeddedPluginColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NOW(),NOW())`,
		row.OwnerUserID, row.ResourceID, row.PackageID, row.Name, row.Summary, row.State, row.Version, row.AutoGate,
		row.ReviewReason, row.ReviewerUserID, row.BuildRequestedBy, row.BuildRequestedAt,
		row.FinalApprovedBy, row.FinalApprovedAt)
	if err != nil {
		return fmt.Errorf("insert embedded plugin state: %w", err)
	}
	return nil
}

// Transition performs an optimistic-concurrency state move + event append in
// ONE transaction: the UPDATE carries `WHERE state = ANY(expected)` so two
// admins racing the same package produce exactly one winner (0 rows →
// ErrEmbeddedStateConflict), and the event row can never exist without the
// state move that caused it.
func (r *MaclawEmbeddedPluginRepository) Transition(ctx context.Context, packageID string, expected []string, to string,
	patch map[string]any, event string, actor *uuid.UUID, reason string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin embedded transition: %w", err)
	}
	defer tx.Rollback(ctx)

	set := "state = $2, updated_at = NOW()"
	args := []any{packageID, to}
	for col, val := range patch { // caller passes only the columns it owns
		args = append(args, val)
		set += fmt.Sprintf(", %s = $%d", col, len(args))
	}
	args = append(args, expected)
	res, err := tx.Exec(ctx, fmt.Sprintf(`
		UPDATE maclaw_embedded_plugins SET %s
		WHERE package_id = $1 AND state = ANY($%d)`, set, len(args)), args...)
	if err != nil {
		return fmt.Errorf("transition embedded plugin: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrEmbeddedStateConflict
	}
	// version comes along for the event line from the row we just moved
	var version string
	if err := tx.QueryRow(ctx, `SELECT version FROM maclaw_embedded_plugins WHERE package_id = $1`, packageID).Scan(&version); err != nil {
		return fmt.Errorf("read embedded version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO maclaw_embedded_plugin_events (package_id, version, event, actor_user_id, reason)
		VALUES ($1,$2,$3,$4,$5)`, packageID, version, event, actor, reason); err != nil {
		return fmt.Errorf("append embedded event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit embedded transition: %w", err)
	}
	return nil
}

// ErrEmbeddedStateConflict: the package moved under us (another admin or a
// concurrent re-upload). Callers surface it as 409, never as a 500.
var ErrEmbeddedStateConflict = fmt.Errorf("embedded plugin state conflict")

func (r *MaclawEmbeddedPluginRepository) GetByPackageID(ctx context.Context, packageID string) (*maclaw.EmbeddedPluginStateRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+embeddedPluginColumns+` FROM maclaw_embedded_plugins WHERE package_id = $1`, packageID)
	return scanEmbeddedRow(row)
}

func (r *MaclawEmbeddedPluginRepository) GetByResourceID(ctx context.Context, resourceID string) (*maclaw.EmbeddedPluginStateRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+embeddedPluginColumns+` FROM maclaw_embedded_plugins WHERE resource_id = $1`, resourceID)
	return scanEmbeddedRow(row)
}

func scanEmbeddedRow(row pgx.Row) (*maclaw.EmbeddedPluginStateRow, error) {
	var out maclaw.EmbeddedPluginStateRow
	err := row.Scan(&out.OwnerUserID, &out.ResourceID, &out.PackageID, &out.Name, &out.Summary, &out.State, &out.Version,
		&out.AutoGate, &out.ReviewReason, &out.ReviewerUserID, &out.BuildRequestedBy,
		&out.BuildRequestedAt, &out.FinalApprovedBy, &out.FinalApprovedAt, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// ListByOwner is the expert portal list: their packages (any state, newest
// first), including rejected ones that have no resource row.
func (r *MaclawEmbeddedPluginRepository) ListByOwner(ctx context.Context, owner uuid.UUID, limit int) ([]maclaw.EmbeddedPluginStateRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+embeddedPluginColumns+` FROM maclaw_embedded_plugins
		WHERE owner_user_id = $1 ORDER BY updated_at DESC LIMIT $2`, owner, limit)
	if err != nil {
		return nil, fmt.Errorf("list embedded plugins: %w", err)
	}
	defer rows.Close()
	return collectEmbeddedRows(rows)
}

// ListAll is the admin review queue (optional state filter, newest first).
func (r *MaclawEmbeddedPluginRepository) ListAll(ctx context.Context, state string, limit int) ([]maclaw.EmbeddedPluginStateRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args := []any{}
	where := ""
	if state != "" {
		where = " WHERE state = $1"
		args = append(args, state)
	}
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, `
		SELECT `+embeddedPluginColumns+` FROM maclaw_embedded_plugins`+where+`
		ORDER BY updated_at DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list all embedded plugins: %w", err)
	}
	defer rows.Close()
	return collectEmbeddedRows(rows)
}

func collectEmbeddedRows(rows pgx.Rows) ([]maclaw.EmbeddedPluginStateRow, error) {
	out := []maclaw.EmbeddedPluginStateRow{}
	for rows.Next() {
		item, err := scanEmbeddedRow(rows)
		if err != nil {
			return nil, err
		}
		if item != nil {
			out = append(out, *item)
		}
	}
	return out, rows.Err()
}

// AppendEvent / ListEvents: the audit spine (rule 1: history is readable).

func (r *MaclawEmbeddedPluginRepository) AppendEvent(ctx context.Context, packageID, version, event string, actor *uuid.UUID, reason string) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO maclaw_embedded_plugin_events (package_id, version, event, actor_user_id, reason)
		VALUES ($1,$2,$3,$4,$5)`, packageID, version, event, actor, reason); err != nil {
		return fmt.Errorf("append embedded event: %w", err)
	}
	return nil
}

func (r *MaclawEmbeddedPluginRepository) ListEvents(ctx context.Context, packageID string, limit int) ([]maclaw.EmbeddedPluginEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT version, event, actor_user_id, reason, created_at
		FROM maclaw_embedded_plugin_events
		WHERE package_id = $1 ORDER BY id ASC LIMIT $2`, packageID, limit)
	if err != nil {
		return nil, fmt.Errorf("list embedded events: %w", err)
	}
	defer rows.Close()
	out := []maclaw.EmbeddedPluginEvent{}
	for rows.Next() {
		var e maclaw.EmbeddedPluginEvent
		if err := rows.Scan(&e.Version, &e.Event, &e.Actor, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Tester whitelist: who may see/select a package while it is in `testing`.

func (r *MaclawEmbeddedPluginRepository) AddTester(ctx context.Context, userID, grantedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO maclaw_embedded_testers (platform_user_id, granted_by)
		VALUES ($1,$2) ON CONFLICT (platform_user_id) DO NOTHING`, userID, grantedBy)
	if err != nil {
		return fmt.Errorf("add embedded tester: %w", err)
	}
	return nil
}

func (r *MaclawEmbeddedPluginRepository) RemoveTester(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM maclaw_embedded_testers WHERE platform_user_id = $1`, userID); err != nil {
		return fmt.Errorf("remove embedded tester: %w", err)
	}
	return nil
}

func (r *MaclawEmbeddedPluginRepository) IsTester(ctx context.Context, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM maclaw_embedded_testers WHERE platform_user_id = $1)`, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check embedded tester: %w", err)
	}
	return exists, nil
}

func (r *MaclawEmbeddedPluginRepository) ListTesters(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT platform_user_id FROM maclaw_embedded_testers ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list embedded testers: %w", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
