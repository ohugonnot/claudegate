package job

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a SQLite-backed implementation of Store.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the SQLite database at dbPath and runs migrations.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	// WAL mode for better concurrent read performance.
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	s := &SQLiteStore{db: db}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id              TEXT PRIMARY KEY,
			prompt          TEXT NOT NULL,
			system_prompt   TEXT NOT NULL DEFAULT '',
			model           TEXT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'queued',
			result          TEXT NOT NULL DEFAULT '',
			error           TEXT NOT NULL DEFAULT '',
			callback_url    TEXT NOT NULL DEFAULT '',
			metadata        TEXT,
			response_format TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL,
			started_at      DATETIME,
			completed_at    DATETIME
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_status       ON jobs(status);
		CREATE INDEX IF NOT EXISTS idx_jobs_created_at   ON jobs(created_at);
		CREATE INDEX IF NOT EXISTS idx_jobs_completed_at ON jobs(completed_at);
	`)
	if err != nil {
		return err
	}
	// Add response_format column to existing databases that predate this migration.
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN response_format TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *SQLiteStore) Create(ctx context.Context, j *Job) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs
			(id, prompt, system_prompt, model, status, result, error, callback_url, metadata, response_format, created_at)
		VALUES
			(?, ?, ?, ?, ?, '', '', ?, ?, ?, ?)
	`,
		j.ID,
		j.Prompt,
		j.SystemPrompt,
		j.Model,
		StatusQueued,
		j.CallbackURL,
		nullableJSON(j.Metadata),
		j.ResponseFormat,
		j.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, prompt, system_prompt, model, status, result, error,
		       callback_url, metadata, response_format, created_at, started_at, completed_at
		FROM jobs WHERE id = ?
	`, id)

	j := &Job{}
	var metadata sql.NullString
	var startedAt, completedAt sql.NullTime

	err := row.Scan(
		&j.ID, &j.Prompt, &j.SystemPrompt, &j.Model, &j.Status,
		&j.Result, &j.Error, &j.CallbackURL, &metadata,
		&j.ResponseFormat, &j.CreatedAt, &startedAt, &completedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", id, err)
	}

	if metadata.Valid {
		j.Metadata = []byte(metadata.String)
	}
	if startedAt.Valid {
		t := startedAt.Time
		j.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		j.CompletedAt = &t
	}
	return j, nil
}

func (s *SQLiteStore) UpdateStatus(ctx context.Context, id string, status Status, result, errMsg string) error {
	now := time.Now().UTC()

	var completedAt interface{}
	if status.IsTerminal() {
		completedAt = now
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, result = ?, error = ?, completed_at = ?
		WHERE id = ?
	`, status, result, errMsg, completedAt, id)
	if err != nil {
		return fmt.Errorf("update status for job %s: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) MarkProcessing(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, started_at = ? WHERE id = ?
	`, StatusProcessing, now, id)
	if err != nil {
		return fmt.Errorf("mark processing for job %s: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ResetProcessing moves all jobs stuck in "processing" back to "queued".
// Returns the IDs of the affected jobs so the caller can re-enqueue them.
func (s *SQLiteStore) ResetProcessing(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM jobs WHERE status = ?`, StatusProcessing)
	if err != nil {
		return nil, fmt.Errorf("query processing jobs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan job id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate processing jobs: %w", err)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, started_at = NULL WHERE status = ?
	`, StatusQueued, StatusProcessing)
	if err != nil {
		return nil, fmt.Errorf("reset processing jobs: %w", err)
	}
	return ids, nil
}

// List returns jobs ordered by created_at DESC with pagination, and the total count.
func (s *SQLiteStore) List(ctx context.Context, limit, offset int) ([]*Job, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, prompt, system_prompt, model, status, result, error,
		       callback_url, metadata, response_format, created_at, started_at, completed_at
		FROM jobs
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		j := &Job{}
		var metadata sql.NullString
		var startedAt, completedAt sql.NullTime

		if err := rows.Scan(
			&j.ID, &j.Prompt, &j.SystemPrompt, &j.Model, &j.Status,
			&j.Result, &j.Error, &j.CallbackURL, &metadata,
			&j.ResponseFormat, &j.CreatedAt, &startedAt, &completedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan job: %w", err)
		}

		if metadata.Valid {
			j.Metadata = []byte(metadata.String)
		}
		if startedAt.Valid {
			t := startedAt.Time
			j.StartedAt = &t
		}
		if completedAt.Valid {
			t := completedAt.Time
			j.CompletedAt = &t
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate jobs: %w", err)
	}

	return jobs, total, nil
}

func (s *SQLiteStore) DeleteTerminalBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM jobs
		WHERE status IN (?, ?, ?)
		AND completed_at IS NOT NULL
		AND completed_at < ?
	`, StatusCompleted, StatusFailed, StatusCancelled, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete terminal jobs: %w", err)
	}
	return res.RowsAffected()
}

// nullableJSON returns nil if b is empty, otherwise returns the raw bytes as a string.
func nullableJSON(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
