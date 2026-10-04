// Package store persists manager state in SQLite. Configuration objects
// (agents, jobs, copy jobs, settings) are JSON documents; runs and logs are
// proper tables since they're queried and paginated.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"vaultkeeper/internal/proto"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
	mu sync.Mutex // serialises read-modify-write on documents
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{DB: db}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS objects (kind TEXT NOT NULL, id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(kind,id));
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, job_id TEXT NOT NULL DEFAULT '', copy_id TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, trigger TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, started INTEGER NOT NULL DEFAULT 0, finished INTEGER NOT NULL DEFAULT 0,
  activity INTEGER NOT NULL DEFAULT 0, summary TEXT NOT NULL DEFAULT '{}', message TEXT NOT NULL DEFAULT '',
  cancel INTEGER NOT NULL DEFAULT 0, hidden INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS runs_job ON runs(job_id, created DESC);
CREATE INDEX IF NOT EXISTS runs_status ON runs(status);
CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, level TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '', job_id TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', message TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS logs_run ON logs(run_id, id);
CREATE INDEX IF NOT EXISTS logs_ts ON logs(ts DESC);
`)
	if err == nil {
		// Additive migration for databases created before explorer support.
		_, _ = db.Exec(`ALTER TABLE runs ADD COLUMN params TEXT NOT NULL DEFAULT '{}'`)
		_, _ = db.Exec(`ALTER TABLE runs ADD COLUMN parent TEXT NOT NULL DEFAULT ''`)
		_, _ = db.Exec(`ALTER TABLE runs ADD COLUMN wait_for TEXT NOT NULL DEFAULT ''`)
	}
	return s, err
}

func (s *Store) Close() error { return s.DB.Close() }

// Put upserts a document.
func (s *Store) Put(kind, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO objects(kind,id,data) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data`, kind, id, string(b))
	return err
}

func (s *Store) Get(kind, id string, v any) error {
	var d string
	err := s.DB.QueryRow(`SELECT data FROM objects WHERE kind=? AND id=?`, kind, id).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(d), v)
}

func (s *Store) Delete(kind, id string) error {
	_, err := s.DB.Exec(`DELETE FROM objects WHERE kind=? AND id=?`, kind, id)
	return err
}

// Update performs an atomic read-modify-write on a document.
func Update[T any](s *Store, kind, id string, fn func(*T) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v T
	if err := s.Get(kind, id, &v); err != nil {
		return err
	}
	if err := fn(&v); err != nil {
		return err
	}
	return s.Put(kind, id, v)
}

func List[T any](s *Store, kind string) ([]T, error) {
	rows, err := s.DB.Query(`SELECT data FROM objects WHERE kind=? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(d), &v); err == nil {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

// ---- runs ----

type Run struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	JobID    string         `json:"job_id"`
	CopyID   string         `json:"copy_id,omitempty"`
	AgentID  string         `json:"agent_id"`
	Status   string         `json:"status"`
	Trigger  string         `json:"trigger"`
	Created  int64          `json:"created"`
	Started  int64          `json:"started"`
	Finished int64          `json:"finished"`
	Activity int64          `json:"activity"`
	Summary  map[string]any `json:"summary"`
	Message  string         `json:"message"`
	Cancel   bool           `json:"cancel"`
	Hidden   bool           `json:"-"`
	Params   proto.Explore  `json:"params"`
	Parent   string         `json:"parent,omitempty"`   // wake/hook runs: the run they belong to
	WaitFor  string         `json:"wait_for,omitempty"` // queued runs: id of the wake run that must finish first
}

const runCols = `id,kind,job_id,copy_id,agent_id,status,trigger,created,started,finished,activity,summary,message,cancel,hidden,params,parent,wait_for`

func scanRun(sc interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var sum string
	var cancel, hidden int
	var params string
	if err := sc.Scan(&r.ID, &r.Kind, &r.JobID, &r.CopyID, &r.AgentID, &r.Status, &r.Trigger, &r.Created, &r.Started, &r.Finished, &r.Activity, &sum, &r.Message, &cancel, &hidden, &params, &r.Parent, &r.WaitFor); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(params), &r.Params)
	r.Cancel = cancel != 0
	r.Hidden = hidden != 0
	r.Summary = map[string]any{}
	_ = json.Unmarshal([]byte(sum), &r.Summary)
	return &r, nil
}

func (s *Store) InsertRun(r *Run) error {
	pb, _ := json.Marshal(r.Params)
	_, err := s.DB.Exec(`INSERT INTO runs(id,kind,job_id,copy_id,agent_id,status,trigger,created,hidden,params,parent,wait_for) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Kind, r.JobID, r.CopyID, r.AgentID, r.Status, r.Trigger, r.Created, b2i(r.Hidden), string(pb), r.Parent, r.WaitFor)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) GetRun(id string) (*Run, error) {
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListRuns returns visible runs, newest first. Empty filters are ignored.
func (s *Store) ListRuns(jobID, kind, status string, limit int) ([]*Run, error) {
	q := `SELECT ` + runCols + ` FROM runs WHERE hidden=0`
	var args []any
	if jobID != "" {
		q += ` AND (job_id=? OR copy_id=?)`
		args = append(args, jobID, jobID)
	}
	if kind != "" {
		q += ` AND kind=?`
		args = append(args, kind)
	}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created DESC LIMIT ?`
	args = append(args, limit)
	return s.queryRuns(q, args...)
}

func (s *Store) queryRuns(q string, args ...any) ([]*Run, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRuns returns queued + running runs.
func (s *Store) ActiveRuns() ([]*Run, error) {
	return s.queryRuns(`SELECT ` + runCols + ` FROM runs WHERE status IN ('queued','running') ORDER BY created`)
}

// ClaimRun atomically takes the oldest queued run for the agent whose job has
// no running run. Returns nil if nothing is claimable.
func (s *Store) ClaimRun(agentID string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM runs q WHERE q.agent_id=? AND q.status='queued' AND q.wait_for=''
	  AND NOT EXISTS (SELECT 1 FROM runs x WHERE x.status='running' AND x.job_id=q.job_id AND x.job_id<>''
	    AND (x.kind IN ('backup','copy','prune','mirror','forget','purge') OR q.kind IN ('backup','copy','prune','mirror','forget','purge')))
	  ORDER BY q.created LIMIT 1`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	_, err = s.DB.Exec(`UPDATE runs SET status='running', started=?, activity=? WHERE id=?`, now, now, r.ID)
	r.Status, r.Started, r.Activity = "running", now, now
	return r, err
}

func (s *Store) TouchRun(id string) (cancel bool) {
	var c int
	_ = s.DB.QueryRow(`SELECT cancel FROM runs WHERE id=?`, id).Scan(&c)
	_, _ = s.DB.Exec(`UPDATE runs SET activity=? WHERE id=?`, time.Now().Unix(), id)
	return c != 0
}

func (s *Store) FinishRun(id, status, message string, summary map[string]any) error {
	b, _ := json.Marshal(summary)
	_, err := s.DB.Exec(`UPDATE runs SET status=?, message=?, summary=?, finished=? WHERE id=?`, status, message, string(b), time.Now().Unix(), id)
	return err
}

// DeleteRun removes a run and its logs (used for transient browse/search runs).
func (s *Store) DeleteRun(id string) {
	_, _ = s.DB.Exec(`DELETE FROM logs WHERE run_id=?`, id)
	_, _ = s.DB.Exec(`DELETE FROM runs WHERE id=?`, id)
}

// ReleaseWait lets a run that was waiting for its wake step start.
func (s *Store) ReleaseWait(id string) {
	_, _ = s.DB.Exec(`UPDATE runs SET wait_for='', activity=? WHERE id=? AND status='queued'`, time.Now().Unix(), id)
}

func (s *Store) RequestCancel(id string) error {
	_, err := s.DB.Exec(`UPDATE runs SET cancel=1 WHERE id=? AND status IN ('queued','running')`, id)
	return err
}

// LastRun returns the most recent finished (or running) run of a kind for a job.
func (s *Store) LastRun(jobID, kind string) *Run {
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM runs WHERE job_id=? AND kind=? AND copy_id='' ORDER BY created DESC LIMIT 1`, jobID, kind))
	if err != nil {
		return nil
	}
	return r
}

// LastSyncRun returns the latest real (non-preview) mirror run.
func (s *Store) LastSyncRun(jobID string) *Run {
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM runs WHERE job_id=? AND kind='mirror' AND params NOT LIKE '%"dry_run":true%' ORDER BY created DESC LIMIT 1`, jobID))
	if err != nil {
		return nil
	}
	return r
}

func (s *Store) RecentSyncStatuses(jobID string, n int) []string {
	rows, err := s.DB.Query(`SELECT status FROM runs WHERE job_id=? AND kind='mirror' AND params NOT LIKE '%"dry_run":true%' AND status IN ('success','warning','failed') ORDER BY created DESC LIMIT ?`, jobID, n)
	out := []string{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		_ = rows.Scan(&st)
		out = append([]string{st}, out...)
	}
	return out
}

func (s *Store) LastCopyRun(copyID string) *Run {
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM runs WHERE copy_id=? AND kind='copy' ORDER BY created DESC LIMIT 1`, copyID))
	if err != nil {
		return nil
	}
	return r
}

// RecentStatuses returns the last n finished statuses (oldest first) for sparklines.
func (s *Store) RecentStatuses(jobID, kind, copyID string, n int) []string {
	rows, err := s.DB.Query(`SELECT status FROM runs WHERE job_id=? AND kind=? AND copy_id=? AND status IN ('success','warning','failed') ORDER BY created DESC LIMIT ?`, jobID, kind, copyID, n)
	out := []string{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		_ = rows.Scan(&st)
		out = append([]string{st}, out...)
	}
	return out
}

// ---- logs ----

type LogEntry struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"` // unix ms
	Level   string `json:"level"`
	RunID   string `json:"run_id"`
	JobID   string `json:"job_id"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

func (s *Store) AddLog(l LogEntry) {
	if l.TS == 0 {
		l.TS = time.Now().UnixMilli()
	}
	_, _ = s.DB.Exec(`INSERT INTO logs(ts,level,run_id,job_id,source,message) VALUES(?,?,?,?,?,?)`, l.TS, l.Level, l.RunID, l.JobID, l.Source, l.Message)
}

func (s *Store) AddLogs(ls []LogEntry) {
	tx, err := s.DB.Begin()
	if err != nil {
		return
	}
	st, _ := tx.Prepare(`INSERT INTO logs(ts,level,run_id,job_id,source,message) VALUES(?,?,?,?,?,?)`)
	for _, l := range ls {
		_, _ = st.Exec(l.TS, l.Level, l.RunID, l.JobID, l.Source, l.Message)
	}
	st.Close()
	_ = tx.Commit()
}

func (s *Store) RunLogs(runID string) []LogEntry {
	return s.queryLogs(`SELECT id,ts,level,run_id,job_id,source,message FROM logs WHERE run_id=? ORDER BY id`, runID)
}

// SearchLogs returns newest-first logs at or above minLevels (a set of level names).
func (s *Store) SearchLogs(levels []string, jobID, q string, before int64, limit int) []LogEntry {
	qs := `SELECT id,ts,level,run_id,job_id,source,message FROM logs WHERE 1=1`
	var args []any
	if len(levels) > 0 {
		qs += ` AND level IN (`
		for i, l := range levels {
			if i > 0 {
				qs += ","
			}
			qs += "?"
			args = append(args, l)
		}
		qs += `)`
	}
	if jobID != "" {
		qs += ` AND job_id=?`
		args = append(args, jobID)
	}
	if q != "" {
		qs += ` AND message LIKE ?`
		args = append(args, "%"+q+"%")
	}
	if before > 0 {
		qs += ` AND id<?`
		args = append(args, before)
	}
	qs += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return s.queryLogs(qs, args...)
}

func (s *Store) queryLogs(q string, args ...any) []LogEntry {
	rows, err := s.DB.Query(q, args...)
	out := []LogEntry{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var l LogEntry
		_ = rows.Scan(&l.ID, &l.TS, &l.Level, &l.RunID, &l.JobID, &l.Source, &l.Message)
		out = append(out, l)
	}
	return out
}

func (s *Store) Purge(logDays int) {
	cut := time.Now().AddDate(0, 0, -logDays)
	_, _ = s.DB.Exec(`DELETE FROM logs WHERE ts<?`, cut.UnixMilli())
	_, _ = s.DB.Exec(`DELETE FROM runs WHERE created<? AND status IN ('success','warning','failed')`, cut.AddDate(0, 0, -logDays).Unix())
}

// Item is one document for ReplaceObjects.
type Item struct {
	Kind, ID string
	V        any
}

// ReplaceObjects atomically replaces every document of the given kinds.
func (s *Store) ReplaceObjects(kinds []string, items []Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range kinds {
		if _, err := tx.Exec(`DELETE FROM objects WHERE kind=?`, k); err != nil {
			return err
		}
	}
	for _, it := range items {
		b, err := json.Marshal(it.V)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO objects(kind,id,data) VALUES(?,?,?)`, it.Kind, it.ID, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
