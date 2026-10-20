// Package announcementexec records local simulated receipts, never publication.
// Native Sessions approval remains the controller's responsibility.
package announcementexec

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

var (
	ErrUnavailable = errors.New("announcement executor: storage unavailable")
	ErrUncertain   = errors.New("announcement executor: outcome uncertain; operator inspection required")
	ErrConflict    = errors.New("announcement executor: operation already binds different bytes")
	ErrBusy        = errors.New("announcement executor: storage busy")
)

var schema = []string{
	`CREATE TABLE metadata(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL,store_id TEXT NOT NULL)`,
	`CREATE TABLE bindings(session_uid TEXT NOT NULL,operation TEXT NOT NULL,snapshot BLOB NOT NULL,PRIMARY KEY(session_uid,operation))`,
	`CREATE TABLE invocations(id INTEGER PRIMARY KEY,session_uid TEXT NOT NULL,operation TEXT NOT NULL,snapshot BLOB NOT NULL,FOREIGN KEY(session_uid,operation) REFERENCES bindings(session_uid,operation))`,
	`CREATE TABLE outcomes(session_uid TEXT NOT NULL,operation TEXT NOT NULL,receipt BLOB NOT NULL,PRIMARY KEY(session_uid,operation),FOREIGN KEY(session_uid,operation) REFERENCES bindings(session_uid,operation))`,
}

// Store owns its database and stable writer lease. Callers close a borrowing
// Sessions host before closing the Store.
type Store struct {
	db                *sql.DB
	path, id          string
	lease             *os.File
	gate              chan struct{}
	closed, uncertain bool
	closeErr          error
}

func database(path string, ro bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if ro {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
		q.Add("_pragma", "foreign_keys(1)")
		q.Add("_pragma", "synchronous(FULL)")
	}
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, ErrUnavailable
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func privateRegular(path string, empty bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || (!empty && info.Size() == 0) {
		return ErrUnavailable
	}
	return nil
}

// inspect refuses unsafe state read-only, before lock/database creation.
func inspect(root string) (bool, error) {
	for p := root; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) && p == root {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, ErrUnavailable
		}
		if p == root && info.Mode().Perm() != 0700 {
			return false, ErrUnavailable
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	exists := false
	for _, entry := range entries {
		switch entry.Name() {
		case ".executor.lock":
			if privateRegular(filepath.Join(root, entry.Name()), true) != nil {
				return false, ErrUnavailable
			}
		case "executor.db":
			if privateRegular(filepath.Join(root, entry.Name()), false) != nil {
				return false, ErrUnavailable
			}
			exists = true
		default:
			return false, ErrUnavailable // No hot-journal recovery or history adoption.
		}
	}
	return exists, nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func validateSchema(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return "", ErrUnavailable
	}
	found := map[string]bool{}
	for rows.Next() {
		var s string
		if rows.Scan(&s) != nil {
			rows.Close()
			return "", ErrUnavailable
		}
		found[s] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(found) != len(schema) {
		return "", ErrUnavailable
	}
	for _, stmt := range schema {
		if !found[stmt] {
			return "", ErrUnavailable
		}
	}
	var version int
	var id, mode, integrity string
	var count int
	if db.QueryRowContext(ctx, `SELECT count(*) FROM metadata`).Scan(&count) != nil || count != 1 {
		return "", ErrUnavailable
	}
	if db.QueryRowContext(ctx, `SELECT version,store_id FROM metadata WHERE id=1`).Scan(&version, &id) != nil || version != 1 {
		return "", ErrUnavailable
	}
	b, e := hex.DecodeString(id)
	if e != nil || len(b) != 16 || strings.ToLower(id) != id {
		return "", ErrUnavailable
	}
	if db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode) != nil || mode != "delete" {
		return "", ErrUnavailable
	}
	if db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity) != nil || integrity != "ok" {
		return "", ErrUnavailable
	}
	return id, nil
}

// Open creates a new private store or reopens its exact compatible schema. It
// never initializes an existing empty/corrupt database or repairs permissions.
func Open(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil || filepath.Dir(root) == root {
		return nil, ErrUnavailable
	}
	exists, err := inspect(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "executor.db")
	// Probe existing schema without opening a write connection or creating a lock.
	if exists {
		ro, e := database(path, true)
		if e != nil {
			return nil, e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, e = validateSchema(ctx, ro)
		cancel()
		ro.Close()
		if e != nil {
			return nil, e
		}
	}
	if err = os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return nil, ErrUnavailable
	}
	if err == nil && syncDirectory(filepath.Dir(root)) != nil {
		return nil, ErrUnavailable
	}
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Open(filepath.Join(root, ".executor.lock"), flags|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(filepath.Join(root, ".executor.lock"), flags, 0)
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	lease := os.NewFile(uintptr(fd), "executor lease")
	failLease := func() (*Store, error) { lease.Close(); return nil, ErrUnavailable }
	info, err := lease.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return failLease()
	}
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return failLease()
	}
	if _, err = inspect(root); err != nil {
		return failLease()
	}
	fd, err = unix.Open(path, flags|unix.O_CREAT|unix.O_EXCL, 0600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, flags, 0)
	}
	if err != nil {
		return failLease()
	}
	file := os.NewFile(uintptr(fd), "executor database")
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || (!created && info.Size() == 0) {
		file.Close()
		return failLease()
	}
	err = file.Sync()
	file.Close()
	if err != nil || syncDirectory(root) != nil {
		return failLease()
	}
	db, err := database(path, false)
	if err != nil {
		return failLease()
	}
	fail := func() (*Store, error) { db.Close(); return failLease() }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if created {
		err = func() error {
			tx, e := db.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			for _, stmt := range schema {
				if _, e = tx.ExecContext(ctx, stmt); e != nil {
					return e
				}
			}
			nonce := make([]byte, 16)
			if _, e = rand.Read(nonce); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO metadata VALUES(1,1,?)`, hex.EncodeToString(nonce)); e != nil {
				return e
			}
			return tx.Commit()
		}()
		if err != nil {
			return fail()
		}
	}
	id, err := validateSchema(ctx, db)
	if err != nil {
		return fail()
	}
	s := &Store{db: db, path: path, id: id, lease: lease, gate: make(chan struct{}, 1)}
	s.gate <- struct{}{}
	return s, nil
}

func (s *Store) Close() error {
	<-s.gate
	defer func() { s.gate <- struct{}{} }()
	if !s.closed {
		s.closed = true
		s.closeErr = errors.Join(s.db.Close(), s.lease.Close())
	}
	return s.closeErr
}
