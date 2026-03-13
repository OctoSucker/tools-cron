package cron

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Job struct {
	ID        string `json:"id"`
	Spec      string `json:"spec"`
	TaskInput string `json:"task_input"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

type Store struct {
	mu sync.RWMutex
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS cron_jobs (
			id TEXT PRIMARY KEY,
			spec TEXT NOT NULL,
			task_input TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL
		);
	`)
	return err
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) Add(id, spec, taskInput string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("store closed")
	}
	now := time.Now().Unix()
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO cron_jobs (id, spec, task_input, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, spec, taskInput, en, now,
	)
	if err != nil {
		return fmt.Errorf("cron store add: %w", err)
	}
	return nil
}

func (s *Store) List() ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, fmt.Errorf("store closed")
	}
	rows, err := s.db.Query(
		`SELECT id, spec, task_input, enabled, created_at FROM cron_jobs ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("cron store list: %w", err)
	}
	defer rows.Close()
	var list []Job
	for rows.Next() {
		var j Job
		var en int
		if err := rows.Scan(&j.ID, &j.Spec, &j.TaskInput, &en, &j.CreatedAt); err != nil {
			return nil, err
		}
		j.Enabled = en != 0
		list = append(list, j)
	}
	return list, rows.Err()
}

func (s *Store) Get(id string) (Job, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return Job{}, false, fmt.Errorf("store closed")
	}
	var j Job
	var en int
	err := s.db.QueryRow(
		`SELECT id, spec, task_input, enabled, created_at FROM cron_jobs WHERE id = ?`,
		id,
	).Scan(&j.ID, &j.Spec, &j.TaskInput, &en, &j.CreatedAt)
	if err == sql.ErrNoRows {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("cron store get: %w", err)
	}
	j.Enabled = en != 0
	return j, true, nil
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("store closed")
	}
	_, err := s.db.Exec(`DELETE FROM cron_jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("cron store remove: %w", err)
	}
	return nil
}

func (s *Store) SetEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("store closed")
	}
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(`UPDATE cron_jobs SET enabled = ? WHERE id = ?`, en, id)
	if err != nil {
		return fmt.Errorf("cron store set_enabled: %w", err)
	}
	return nil
}
