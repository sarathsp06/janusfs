// Package history is the SQLite-backed rollup
// store that records per-operation counters, session metadata, and coverage
// snapshots. It is the only package that executes SQL. File
// contents are never stored — only paths, counts, and timestamps.
package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/sarathsp06/janusfs/internal/obs"
)

// Store persists event rollups to SQLite. Zero value is not usable;
// call Open.
type Store struct {
	db           *sqlx.DB
	mu           sync.Mutex
	buf          []obs.Event
	closeCh      chan struct{}
	doneCh       chan struct{}
	retention    time.Duration
	root         string
	dbPath       string
	sessionID    string
	sessionStart time.Time
	totalOps     uint64
}

// Open creates or opens the history DB at dbPath, runs migrations, and
// starts the batched writer goroutine. If dbPath is empty, Open returns nil
// so --no-history yields a no-op store.
func Open(root, dbPath string, retentionDays int) (*Store, error) {
	if dbPath == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("history: create dir: %w", err)
	}

	db, err := sqlx.Open("sqlite", dbPath+
		"?_pragma=journal_mode(WAL)"+
		"&_pragma=busy_timeout(5000)"+
		"&_pragma=synchronous(NORMAL)"+
		"&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("history: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("history: migrate: %w", err)
	}

	sessionID := fmt.Sprintf("%s_%d", filepath.Base(root), time.Now().UnixNano())

	s := &Store{
		db:           db,
		closeCh:      make(chan struct{}),
		doneCh:       make(chan struct{}),
		retention:    time.Duration(retentionDays) * 24 * time.Hour,
		root:         root,
		dbPath:       dbPath,
		sessionID:    sessionID,
		sessionStart: time.Now().UTC(),
	}

	if err := s.insertSessionStart(); err != nil {
		db.Close()
		return nil, fmt.Errorf("history: session start: %w", err)
	}

	if err := s.prune(); err != nil {
		db.Close()
		return nil, fmt.Errorf("history: initial prune: %w", err)
	}

	go s.batchedWriter()

	return s, nil
}

// Close flushes buffered events, stops the writer, records session end, and
// closes the DB.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	select {
	case <-s.closeCh:
		s.mu.Unlock()
		return nil
	default:
		close(s.closeCh)
	}
	s.mu.Unlock()
	<-s.doneCh
	return s.db.Close()
}

// Record is called by the event-bus fan-out goroutine for every event. It
// buffers and returns immediately; the batched writer flushes periodically, so a
// slow or failed flush never reaches back into a FUSE handler.
func (s *Store) Record(e obs.Event) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.buf = append(s.buf, e)
	if len(s.buf) >= 1000 {
		buf := s.buf
		s.buf = nil
		s.mu.Unlock()
		s.flush(buf)
		return
	}
	s.mu.Unlock()
}

// opRow is a single op_rollups insert row.
type opRow struct {
	TS        int64  `db:"ts"`
	Path      string `db:"path"`
	Op        string `db:"op"`
	Decision  string `db:"decision"`
	Bytes     int64  `db:"bytes"`
	LatencyUs int64  `db:"latency_us"`
}

func (s *Store) batchedWriter() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer close(s.doneCh)
	for {
		select {
		case <-s.closeCh:
			s.mu.Lock()
			buf := s.buf
			s.buf = nil
			s.mu.Unlock()
			if len(buf) > 0 {
				s.flush(buf)
			}
			s.endSession()
			return
		case <-ticker.C:
			s.mu.Lock()
			buf := s.buf
			s.buf = nil
			s.mu.Unlock()
			if len(buf) > 0 {
				s.flush(buf)
			}
		}
	}
}

func (s *Store) flush(events []obs.Event) {
	now := time.Now().UTC().Unix()
	rows := make([]opRow, 0, len(events))
	paths := make([]string, 0, len(events))

	for _, e := range events {
		rows = append(rows, opRow{
			TS:        now,
			Path:      e.Path,
			Op:        string(e.Op),
			Decision:  e.Decision.String(),
			Bytes:     e.Bytes,
			LatencyUs: e.LatencyUs,
		})
		paths = append(paths, e.Path)
	}

	tx, err := s.db.Beginx()
	if err != nil {
		return
	}
	defer tx.Rollback()

	if _, err := tx.NamedExec(`INSERT INTO op_rollups (ts, path, op, decision, bytes, latency_us, cnt) VALUES (:ts, :path, :op, :decision, :bytes, :latency_us, 1)`, rows); err != nil {
		return
	}

	for _, p := range paths {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coverage (path) VALUES (?)`, p); err != nil {
			return
		}
	}

	if err := tx.Commit(); err != nil {
		return
	}

	s.mu.Lock()
	s.totalOps += uint64(len(events))
	s.mu.Unlock()
}

// Stats returns current session counters.
func (s *Store) Stats(ctx context.Context) map[string]any {
	if s == nil || s.db == nil {
		return nil
	}
	out := map[string]any{
		"sessionID":    s.sessionID,
		"sessionStart": s.sessionStart.Format(time.RFC3339),
	}

	s.mu.Lock()
	out["totalOps"] = s.totalOps
	s.mu.Unlock()
	out["dbPath"] = s.dbPath

	var count int
	_ = s.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM coverage")
	out["coveredPaths"] = count

	var maxTS int64
	_ = s.db.GetContext(ctx, &maxTS, "SELECT COALESCE(MAX(ts), 0) FROM op_rollups")
	if maxTS > 0 {
		out["lastEvent"] = time.Unix(maxTS, 0).UTC().Format(time.RFC3339)
	}

	var sessionCount int
	_ = s.db.GetContext(ctx, &sessionCount, "SELECT COUNT(*) FROM sessions")
	out["pastSessions"] = sessionCount - 1

	return out
}

// OpRollup is one aggregated row from the history DB.
type OpRollup struct {
	Path         string  `json:"path" db:"path"`
	Op           string  `json:"op" db:"op"`
	Decision     string  `json:"decision" db:"decision"`
	Cnt          int64   `json:"cnt" db:"cnt"`
	Bytes        int64   `json:"bytes" db:"bytes"`
	AvgLatencyUs float64 `json:"avgLatencyUs" db:"avglatency"`
}

// Query returns rollup data filtered by time range.
func (s *Store) Query(ctx context.Context, since time.Time) ([]OpRollup, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var out []OpRollup
	if err := s.db.SelectContext(ctx, &out,
		`SELECT path, op, decision, SUM(cnt) AS cnt, SUM(bytes) AS bytes, AVG(latency_us) AS avglatency
		 FROM op_rollups WHERE ts >= ?
		 GROUP BY path, op, decision`,
		since.Unix(),
	); err != nil {
		return nil, fmt.Errorf("history: query: %w", err)
	}
	return out, nil
}

func (s *Store) insertSessionStart() error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, root, start_ts) VALUES (?, ?, ?)`,
		s.sessionID, s.root, s.sessionStart.Unix(),
	)
	return err
}

func (s *Store) endSession() {
	if s.db == nil {
		return
	}
	end := time.Now().UTC().Unix()
	_, _ = s.db.Exec(
		`UPDATE sessions SET end_ts = ?, total_ops = ? WHERE id = ?`,
		end, s.totalOps, s.sessionID,
	)
}

func (s *Store) prune() error {
	if s.retention <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-s.retention).Unix()
	if _, err := s.db.Exec(`DELETE FROM op_rollups WHERE ts < ?`, cutoff); err != nil {
		return fmt.Errorf("history: prune op_rollups: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE start_ts < ?`, cutoff); err != nil {
		return fmt.Errorf("history: prune sessions: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM coverage WHERE path NOT IN (SELECT DISTINCT path FROM op_rollups)`); err != nil {
		return fmt.Errorf("history: prune coverage: %w", err)
	}
	return nil
}

func migrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`,
		`INSERT OR IGNORE INTO schema_version (version) VALUES (0)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			root TEXT NOT NULL,
			start_ts INTEGER NOT NULL,
			end_ts INTEGER,
			total_ops INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS op_rollups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			path TEXT NOT NULL,
			op TEXT NOT NULL,
			decision TEXT NOT NULL,
			bytes INTEGER DEFAULT 0,
			latency_us INTEGER DEFAULT 0,
			cnt INTEGER DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS coverage (
			path TEXT PRIMARY KEY,
			first_seen INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_op_rollups_ts ON op_rollups(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_op_rollups_path ON op_rollups(path)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("history: migrate: %w (stmt: %q)", err, stmt[:60])
		}
	}
	return nil
}
