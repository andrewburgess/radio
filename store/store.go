package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"andrewburgess.io/radio/spotify"
	_ "modernc.org/sqlite"
)

// migrations is the ordered list of SQL statements that bring the database from
// version 0 to the current version. Each entry is one migration; its index+1
// is the version number it produces. Never edit an existing entry — append a
// new one instead.
var migrations = []string{
	// v1 — initial schema
	`CREATE TABLE IF NOT EXISTS stations (
		id           INTEGER PRIMARY KEY,
		angle_bucket INTEGER NOT NULL,
		mode         TEXT    NOT NULL CHECK(mode IN ('music', 'podcast')),
		playlist_uri TEXT,
		label        TEXT,
		UNIQUE(angle_bucket, mode)
	);
	CREATE TABLE IF NOT EXISTS playlist_cache (
		playlist_uri      TEXT    PRIMARY KEY,
		snapshot_id       TEXT    NOT NULL,
		tracks_json       TEXT    NOT NULL,
		total_duration_ms INTEGER NOT NULL,
		cached_at         INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS tokens (
		id            INTEGER PRIMARY KEY CHECK(id = 1),
		access_token  TEXT    NOT NULL,
		refresh_token TEXT    NOT NULL,
		expires_at    INTEGER NOT NULL
	);`,
}

// Store is the SQLite-backed persistence layer. It implements
// spotify.TokenStore and spotify.PlaylistCacheStore, and provides station CRUD.
type Store struct {
	db *sql.DB
}

// Station is one row from the stations table.
type Station struct {
	Bucket      int
	Mode        string
	PlaylistURI string // empty string means unassigned (plays static)
	Label       string
}

// New opens (or creates) the SQLite database at dbPath and runs any pending
// migrations to bring the schema up to date.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	s := &Store{db: db}
	if err := s.applyMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// applyMigrations creates the schema_version table if needed, then runs any
// migrations whose index is >= the current version, each in its own
// transaction. The version is updated after each migration succeeds.
func (s *Store) applyMigrations() error {
	// Bootstrap: create the version tracking table outside of any migration.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_version: %w", err)
	}

	// Read current version (0 if the table is empty).
	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("store: read schema version: %w", err)
	}

	for i, migration := range migrations {
		if i < version {
			continue // already applied
		}
		next := i + 1

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: migration %d: begin tx: %w", next, err)
		}
		if _, err := tx.Exec(migration); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("store: migration %d: %w", next, err)
		}
		// Upsert the version row.
		if version == 0 {
			_, err = tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, next)
		} else {
			_, err = tx.Exec(`UPDATE schema_version SET version = ?`, next)
		}
		if err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("store: migration %d: update version: %w", next, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: migration %d: commit: %w", next, err)
		}
		version = next
		slog.Info("store: applied migration", "version", next)
	}
	return nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- spotify.TokenStore ---

// Load returns the persisted token, or nil if none has been saved yet.
func (s *Store) Load() (*spotify.Token, error) {
	var t spotify.Token
	var expiresUnix int64
	err := s.db.QueryRow(
		`SELECT access_token, refresh_token, expires_at FROM tokens WHERE id = 1`,
	).Scan(&t.AccessToken, &t.RefreshToken, &expiresUnix)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: load token: %w", err)
	}
	t.ExpiresAt = time.Unix(expiresUnix, 0)
	return &t, nil
}

// Save upserts the token into the tokens table (always row id=1).
func (s *Store) Save(t *spotify.Token) error {
	_, err := s.db.Exec(`
		INSERT INTO tokens (id, access_token, refresh_token, expires_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			access_token  = excluded.access_token,
			refresh_token = excluded.refresh_token,
			expires_at    = excluded.expires_at
	`, t.AccessToken, t.RefreshToken, t.ExpiresAt.Unix())
	if err != nil {
		return fmt.Errorf("store: save token: %w", err)
	}
	return nil
}

// --- spotify.PlaylistCacheStore ---

// Get returns the cached entry for playlistURI, or nil if not cached.
func (s *Store) Get(playlistURI string) (*spotify.CachedPlaylist, error) {
	var entry spotify.CachedPlaylist
	var tracksJSON string
	var cachedAtUnix int64
	err := s.db.QueryRow(`
		SELECT snapshot_id, tracks_json, total_duration_ms, cached_at
		FROM playlist_cache WHERE playlist_uri = ?
	`, playlistURI).Scan(&entry.SnapshotID, &tracksJSON, &entry.TotalDurationMs, &cachedAtUnix)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get playlist cache: %w", err)
	}
	if err := json.Unmarshal([]byte(tracksJSON), &entry.Tracks); err != nil {
		return nil, fmt.Errorf("store: parse tracks json: %w", err)
	}
	entry.CachedAt = time.Unix(cachedAtUnix, 0)
	return &entry, nil
}

// Set upserts a cache entry for playlistURI.
func (s *Store) Set(playlistURI string, entry spotify.CachedPlaylist) error {
	tracksJSON, err := json.Marshal(entry.Tracks)
	if err != nil {
		return fmt.Errorf("store: marshal tracks: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO playlist_cache (playlist_uri, snapshot_id, tracks_json, total_duration_ms, cached_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(playlist_uri) DO UPDATE SET
			snapshot_id       = excluded.snapshot_id,
			tracks_json       = excluded.tracks_json,
			total_duration_ms = excluded.total_duration_ms,
			cached_at         = excluded.cached_at
	`, playlistURI, entry.SnapshotID, string(tracksJSON), entry.TotalDurationMs, entry.CachedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: set playlist cache: %w", err)
	}
	return nil
}

// All returns all cached playlist entries.
func (s *Store) All() (map[string]spotify.CachedPlaylist, error) {
	rows, err := s.db.Query(`
		SELECT playlist_uri, snapshot_id, tracks_json, total_duration_ms, cached_at
		FROM playlist_cache
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list playlist cache: %w", err)
	}
	defer rows.Close()

	result := make(map[string]spotify.CachedPlaylist)
	for rows.Next() {
		var uri string
		var entry spotify.CachedPlaylist
		var tracksJSON string
		var cachedAtUnix int64
		if err := rows.Scan(&uri, &entry.SnapshotID, &tracksJSON, &entry.TotalDurationMs, &cachedAtUnix); err != nil {
			return nil, fmt.Errorf("store: scan playlist cache: %w", err)
		}
		if err := json.Unmarshal([]byte(tracksJSON), &entry.Tracks); err != nil {
			return nil, fmt.Errorf("store: parse tracks json: %w", err)
		}
		entry.CachedAt = time.Unix(cachedAtUnix, 0)
		result[uri] = entry
	}
	return result, rows.Err()
}

// --- Station methods ---

// GetStation returns the station for the given bucket and mode, or nil if unassigned.
func (s *Store) GetStation(bucket int, mode string) (*Station, error) {
	var st Station
	err := s.db.QueryRow(`
		SELECT angle_bucket, mode, COALESCE(playlist_uri, ''), COALESCE(label, '')
		FROM stations WHERE angle_bucket = ? AND mode = ?
	`, bucket, mode).Scan(&st.Bucket, &st.Mode, &st.PlaylistURI, &st.Label)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get station: %w", err)
	}
	return &st, nil
}

// SetStation upserts the playlist URI (and optional label) for a bucket/mode.
// An empty playlistURI clears the assignment (bucket will play static).
func (s *Store) SetStation(bucket int, mode, playlistURI, label string) error {
	var uriArg, labelArg any
	if playlistURI != "" {
		uriArg = playlistURI
	}
	if label != "" {
		labelArg = label
	}
	_, err := s.db.Exec(`
		INSERT INTO stations (angle_bucket, mode, playlist_uri, label)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(angle_bucket, mode) DO UPDATE SET
			playlist_uri = excluded.playlist_uri,
			label        = excluded.label
	`, bucket, mode, uriArg, labelArg)
	if err != nil {
		return fmt.Errorf("store: set station: %w", err)
	}
	return nil
}

// DeleteStation removes the station assignment for the given bucket and mode.
func (s *Store) DeleteStation(bucket int, mode string) error {
	_, err := s.db.Exec(`DELETE FROM stations WHERE angle_bucket = ? AND mode = ?`, bucket, mode)
	if err != nil {
		return fmt.Errorf("store: delete station: %w", err)
	}
	return nil
}

// ListStations returns all station assignments for a mode, ordered by bucket.
func (s *Store) ListStations(mode string) ([]Station, error) {
	rows, err := s.db.Query(`
		SELECT angle_bucket, mode, COALESCE(playlist_uri, ''), COALESCE(label, '')
		FROM stations WHERE mode = ? ORDER BY angle_bucket
	`, mode)
	if err != nil {
		return nil, fmt.Errorf("store: list stations: %w", err)
	}
	defer rows.Close()

	var stations []Station
	for rows.Next() {
		var st Station
		if err := rows.Scan(&st.Bucket, &st.Mode, &st.PlaylistURI, &st.Label); err != nil {
			return nil, fmt.Errorf("store: scan station: %w", err)
		}
		stations = append(stations, st)
	}
	return stations, rows.Err()
}
