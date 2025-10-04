package database

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/lib/pq"
)

// DB wraps the database connection
type DB struct {
	conn *sql.DB
}

// New creates a new database connection
func New(dsn string) (*DB, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db := &DB{conn: conn}
	
	// Initialize tables and run migrations
	if err := db.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}
	
	if err := db.migrateSchema(); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	return db, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.conn.Close()
}

// GetConnection returns the underlying database connection
func (db *DB) GetConnection() *sql.DB {
	return db.conn
}

// createTables creates the necessary tables
func (db *DB) createTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS voice_hours (
			user_id TEXT NOT NULL,
			guild_id TEXT NOT NULL,
			total_seconds BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, guild_id)
		)`,
		`CREATE TABLE IF NOT EXISTS activity_hours (
			user_id TEXT NOT NULL,
			activity_name TEXT NOT NULL,
			total_seconds BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, activity_name)
		)`,
		`CREATE TABLE IF NOT EXISTS voice_channel_hours (
			user_id TEXT NOT NULL,
			guild_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			total_seconds BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, guild_id, channel_id)
		)`,
	}

	for _, query := range queries {
		if _, err := db.conn.Exec(query); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	return nil
}

// migrateSchema handles database schema migrations
func (db *DB) migrateSchema() error {
	migrations := []string{
		// Ensure total_seconds column exists (for very old versions)
		`ALTER TABLE voice_hours ADD COLUMN IF NOT EXISTS total_seconds BIGINT NOT NULL DEFAULT 0`,
		
		// Migrate from total_minutes to total_seconds if old schema exists
		`UPDATE voice_hours SET total_seconds = total_minutes * 60 WHERE total_seconds = 0 AND EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_name='voice_hours' AND column_name='total_minutes'
		)`,
		`ALTER TABLE voice_hours DROP COLUMN IF EXISTS total_minutes`,
		
		// Add guild_id column if not exists in voice_hours
		`ALTER TABLE voice_hours ADD COLUMN IF NOT EXISTS guild_id TEXT`,
		
		// Migrate old data that stored 'guild:user' in user_id
		`UPDATE voice_hours SET guild_id = split_part(user_id, ':', 1) WHERE guild_id IS NULL AND position(':' in user_id) > 0`,
		`UPDATE voice_hours SET user_id = split_part(user_id, ':', 2) WHERE position(':' in user_id) > 0`,
		
		// Fill empty values and make NOT NULL
		`UPDATE voice_hours SET guild_id = COALESCE(guild_id, '')`,
		`ALTER TABLE voice_hours ALTER COLUMN user_id SET NOT NULL`,
		`ALTER TABLE voice_hours ALTER COLUMN guild_id SET NOT NULL`,
		
		// Ensure composite primary key (user_id, guild_id)
		`DO $$
		DECLARE
			pk_name text;
		BEGIN
			SELECT conname INTO pk_name FROM pg_constraint
			WHERE contype = 'p' AND conrelid = 'voice_hours'::regclass;
			IF pk_name IS NOT NULL THEN
				EXECUTE format('ALTER TABLE voice_hours DROP CONSTRAINT %I', pk_name);
			END IF;
		END$$;`,
		`ALTER TABLE voice_hours ADD CONSTRAINT voice_hours_pkey PRIMARY KEY (user_id, guild_id)`,
		
		// Migrate old activity_hours (if has guild_id) to global aggregated
		`CREATE TABLE IF NOT EXISTS activity_hours_new (
			user_id TEXT NOT NULL,
			activity_name TEXT NOT NULL,
			total_seconds BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, activity_name)
		)`,
		
		// Aggregate from old schema to new
		`INSERT INTO activity_hours_new (user_id, activity_name, total_seconds)
		SELECT user_id, activity_name, SUM(total_seconds)
		FROM activity_hours
		GROUP BY user_id, activity_name
		ON CONFLICT (user_id, activity_name) DO UPDATE SET total_seconds = activity_hours_new.total_seconds + EXCLUDED.total_seconds`,
		
		// Replace table
		`DROP TABLE IF EXISTS activity_hours`,
		`ALTER TABLE activity_hours_new RENAME TO activity_hours`,
	}

	for _, migration := range migrations {
		if _, err := db.conn.Exec(migration); err != nil {
			log.Printf("Warning: Migration failed (this might be expected): %v", err)
		}
	}

	return nil
}
