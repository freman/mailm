package state

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS migrations (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    source_folder       TEXT    NOT NULL,
    source_uid          INTEGER NOT NULL,
    source_uidvalidity  INTEGER NOT NULL,
    dest_folder         TEXT    NOT NULL,
    dest_uid            INTEGER,
    migrated_at         DATETIME NOT NULL,
    deleted_from_source BOOLEAN NOT NULL DEFAULT 0,
    UNIQUE(source_folder, source_uid, source_uidvalidity)
);
`

// DB wraps the SQLite state database.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite state file and applies the schema.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening state DB %q: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying state DB schema: %w", err)
	}
	return &DB{db: db}, nil
}

// AlreadyMigrated returns true if this source message has been successfully copied before.
func (d *DB) AlreadyMigrated(sourceFolder string, sourceUID, uidValidity uint32) (bool, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM migrations WHERE source_folder=? AND source_uid=? AND source_uidvalidity=?`,
		sourceFolder, sourceUID, uidValidity,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RecordMigration inserts a successful copy record. Duplicate inserts are silently ignored.
func (d *DB) RecordMigration(sourceFolder string, sourceUID, uidValidity uint32, destFolder string) error {
	_, err := d.db.Exec(
		`INSERT OR IGNORE INTO migrations(source_folder, source_uid, source_uidvalidity, dest_folder, migrated_at)
         VALUES(?,?,?,?,?)`,
		sourceFolder, sourceUID, uidValidity, destFolder, time.Now().UTC(),
	)
	return err
}

// MarkDeleted updates the record to indicate the source message was expunged.
func (d *DB) MarkDeleted(sourceFolder string, sourceUID, uidValidity uint32) error {
	_, err := d.db.Exec(
		`UPDATE migrations SET deleted_from_source=1 WHERE source_folder=? AND source_uid=? AND source_uidvalidity=?`,
		sourceFolder, sourceUID, uidValidity,
	)
	return err
}

// Close closes the database.
func (d *DB) Close() error {
	return d.db.Close()
}
