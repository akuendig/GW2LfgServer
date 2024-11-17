package database

import (
	"context"
	"database/sql"
	pb "gw2lfgserver/pb"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	db *sql.DB
}

func New(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Create tables if they don't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS groups (
			id TEXT PRIMARY KEY,
			creator_id TEXT,
			title TEXT,
			kill_proof_id TEXT,
			kill_proof_minimum INTEGER,
			created_at_sec INTEGER
		);
		CREATE TABLE IF NOT EXISTS applications (
			id TEXT PRIMARY KEY,
			group_id TEXT,
			account_name TEXT,
			FOREIGN KEY(group_id) REFERENCES groups(id)
		);
	`)
	if err != nil {
		return nil, err
	}

	return &DB{
		db: db,
	}, nil
}

func (db *DB) SaveGroup(ctx context.Context, group *pb.Group) error {
	_, err := db.db.ExecContext(ctx, `
		INSERT INTO groups (id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title,
			kill_proof_id=excluded.kill_proof_id,
			kill_proof_minimum=excluded.kill_proof_minimum
	`, group.Id, group.CreatorId, group.Title, group.KillProofId, group.KillProofMinimum, group.CreatedAtSec)
	return err
}

func (db *DB) DeleteGroup(ctx context.Context, groupId string) error {
	_, err := db.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, groupId)
	return err
}
