package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type App struct {
	cfg               Config
	version           string
	db                *sql.DB
	static            fs.FS
	hub               *claimHub
	stop              chan struct{}
	stopOnce          sync.Once
	loginRate         *rateLimiter
	turnstileEndpoint string
}

func New(cfg Config, version string, static fs.FS) (*App, error) {
	dbPath := filepath.Join(cfg.DataDir, "flashshare.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Explicit BEGIN IMMEDIATE statements are kept on one connection. This is
	// sufficient for the intended small-user deployment.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA synchronous=NORMAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	a := &App{
		cfg: cfg, version: version, db: db, static: static,
		hub: newClaimHub(), stop: make(chan struct{}), loginRate: newRateLimiter(10, 5*time.Minute),
		turnstileEndpoint: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	}
	if err := a.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := a.ensureSettings(); err != nil {
		db.Close()
		return nil, err
	}
	go a.cleanupLoop()
	return a, nil
}

func (a *App) Close() error {
	a.stopOnce.Do(func() { close(a.stop) })
	a.hub.closeAll()
	return a.db.Close()
}

func (a *App) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL COLLATE NOCASE UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('admin','user')),
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			token_hash TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS shares (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			public_id TEXT NOT NULL UNIQUE,
			secret_cipher BLOB NOT NULL,
			secret_nonce BLOB NOT NULL,
			title TEXT NOT NULL,
			session_timeout INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ip_allow TEXT NOT NULL DEFAULT '[]',
			ip_deny TEXT NOT NULL DEFAULT '[]',
			default_align TEXT NOT NULL DEFAULT 'center',
			created_at INTEGER NOT NULL,
			revoked_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_shares_owner ON shares(owner_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS items (
			id TEXT PRIMARY KEY,
			share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			payload_cipher BLOB NOT NULL,
			payload_nonce BLOB NOT NULL,
			state TEXT NOT NULL DEFAULT 'available' CHECK(state IN ('available','allocated','active','destroyed')),
			created_at INTEGER NOT NULL,
			UNIQUE(share_id, position)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_items_available ON items(share_id, state, position)`,
		`CREATE TABLE IF NOT EXISTS assets (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			item_id TEXT REFERENCES items(id) ON DELETE SET NULL,
			original_name TEXT NOT NULL,
			declared_mime TEXT NOT NULL,
			detected_mime TEXT NOT NULL,
			allowed_mime TEXT NOT NULL,
			extension TEXT NOT NULL,
			size INTEGER NOT NULL,
			storage_key TEXT NOT NULL UNIQUE,
			mac TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_assets_owner ON assets(owner_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_assets_item ON assets(item_id)`,
		`CREATE TABLE IF NOT EXISTS claims (
			id TEXT PRIMARY KEY,
			share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
			item_id TEXT NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('allocated','active','destroyed')),
			allocated_at INTEGER NOT NULL,
			revealed_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL DEFAULT 0,
			lease_expires_at INTEGER NOT NULL,
			destroyed_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_claims_cleanup ON claims(state, expires_at, lease_expires_at)`,
	}
	for _, stmt := range stmts {
		if _, err := a.db.Exec(stmt); err != nil {
			return fmt.Errorf("database migration: %w", err)
		}
	}
	return nil
}

func (a *App) ensureSettings() error {
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='global'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	b, err := json.Marshal(defaultSettings())
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO settings(key,value) VALUES('global',?)`, string(b))
	return err
}

func (a *App) getSettings() (Settings, error) {
	var value string
	if err := a.db.QueryRow(`SELECT value FROM settings WHERE key='global'`).Scan(&value); err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal([]byte(value), &s); err != nil {
		return Settings{}, err
	}
	var secretValue string
	err := a.db.QueryRow(`SELECT value FROM settings WHERE key='turnstile_secret'`).Scan(&secretValue)
	if err == nil {
		var stored struct {
			Cipher string `json:"cipher"`
			Nonce  string `json:"nonce"`
		}
		if err := json.Unmarshal([]byte(secretValue), &stored); err != nil {
			return Settings{}, err
		}
		ciphertext, err := decodeBytes(stored.Cipher)
		if err != nil {
			return Settings{}, err
		}
		nonce, err := decodeBytes(stored.Nonce)
		if err != nil {
			return Settings{}, err
		}
		plain, err := a.decryptText("settings:turnstile-secret:v1", ciphertext, nonce)
		if err != nil {
			return Settings{}, err
		}
		s.TurnstileSecretKey = string(plain)
	} else if err != sql.ErrNoRows {
		return Settings{}, err
	}
	s.TurnstileSecretConfigured = s.TurnstileSecretKey != ""
	return s, nil
}

func (a *App) putSettings(s Settings) error {
	clearSecret := s.TurnstileClearSecret
	s.TurnstileClearSecret = false
	s.TurnstileSecretConfigured = false
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE settings SET value=? WHERE key='global'`, string(b)); err != nil {
		return err
	}
	if clearSecret {
		if _, err := tx.Exec(`DELETE FROM settings WHERE key='turnstile_secret'`); err != nil {
			return err
		}
	} else if s.TurnstileSecretKey != "" {
		ciphertext, nonce, err := a.encryptText("settings:turnstile-secret:v1", []byte(s.TurnstileSecretKey))
		if err != nil {
			return err
		}
		stored, err := json.Marshal(map[string]string{"cipher": encodeBytes(ciphertext), "nonce": encodeBytes(nonce)})
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO settings(key,value) VALUES('turnstile_secret',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(stored))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func userFromRow(scanner interface{ Scan(...any) error }) (User, error) {
	var u User
	var disabled int
	err := scanner.Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.CreatedAt)
	u.Disabled = disabled != 0
	return u, err
}

func (a *App) totalStoredBytes() (int64, error) {
	var total sql.NullInt64
	err := a.db.QueryRow(`SELECT SUM(size) FROM assets`).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return total.Int64, err
}

func (a *App) cleanupLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			if err := a.cleanupExpired(); err != nil {
				log.Printf("cleanup: %v", err)
			}
		}
	}
}

func (a *App) cleanupExpired() error {
	now := nowUnix()
	rows, err := a.db.Query(`SELECT id FROM claims WHERE state='allocated' AND allocated_at < ?`, now-60)
	if err != nil {
		return err
	}
	var claimIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			claimIDs = append(claimIDs, id)
		}
	}
	rows.Close()
	rows, err = a.db.Query(`SELECT id FROM claims WHERE state='active' AND (expires_at <= ? OR lease_expires_at <= ?)`, now, now)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			claimIDs = append(claimIDs, id)
		}
	}
	rows.Close()
	for _, id := range claimIDs {
		if err := a.destroyClaim(id, "expired"); err != nil {
			log.Printf("destroy claim %s: %v", id, err)
		}
	}

	var expiredShares []string
	rows, err = a.db.Query(`SELECT id FROM shares WHERE revoked_at=0 AND expires_at <= ?`, now)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			expiredShares = append(expiredShares, id)
		}
	}
	rows.Close()
	for _, id := range expiredShares {
		if err := a.destroyShareItems(id); err != nil {
			log.Printf("expire share %s: %v", id, err)
		}
	}

	_, err = a.db.Exec(`DELETE FROM auth_sessions WHERE expires_at <= ?`, now)
	if err != nil {
		return err
	}

	rows, err = a.db.Query(`SELECT id, storage_key FROM assets WHERE item_id IS NULL AND created_at < ?`, now-24*3600)
	if err != nil {
		return err
	}
	type staleAsset struct{ id, key string }
	var stale []staleAsset
	for rows.Next() {
		var v staleAsset
		if rows.Scan(&v.id, &v.key) == nil {
			stale = append(stale, v)
		}
	}
	rows.Close()
	for _, v := range stale {
		_ = removeIfExists(filepath.Join(a.cfg.DataDir, "blobs", v.key))
		_, _ = a.db.Exec(`DELETE FROM assets WHERE id=? AND item_id IS NULL`, v.id)
	}
	return nil
}

func removeIfExists(path string) error {
	err := fs.ErrInvalid
	if statErr := filesystemRemove(path); statErr == nil {
		return nil
	} else {
		err = statErr
	}
	return err
}

// filesystemRemove is isolated for tests and keeps file deletion explicit.
func filesystemRemove(path string) error {
	return removeFile(path)
}
