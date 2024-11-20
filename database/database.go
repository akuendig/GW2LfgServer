package database

import (
	"context"
	"database/sql"
	pb "gw2lfgserver/pb"

	_ "github.com/mattn/go-sqlite3"
)

// DB represents a database connection with CRUD operations for groups and applications
type DB struct {
	db *sql.DB
}

// Config holds database configuration
type Config struct {
	Path string
}

// New creates a new database connection and initializes tables
func New(cfg Config) (*DB, error) {
	db, err := sql.Open("sqlite3", cfg.Path)
	if err != nil {
		return nil, err
	}

	if err := initializeTables(db); err != nil {
		return nil, err
	}

	return &DB{db: db}, nil
}

func initializeTables(db *sql.DB) error {
	schema := `
		CREATE TABLE IF NOT EXISTS groups (
			id TEXT PRIMARY KEY,
			creator_id TEXT NOT NULL,
			title TEXT NOT NULL,
			kill_proof_id TEXT,
			kill_proof_minimum INTEGER DEFAULT 0,
			created_at_sec INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS applications (
			id TEXT PRIMARY KEY,
			group_id TEXT NOT NULL,
			account_name TEXT NOT NULL,
			FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_groups_creator ON groups(creator_id);
		CREATE INDEX IF NOT EXISTS idx_applications_group ON applications(group_id);
	`
	_, err := db.Exec(schema)
	return err
}

// GroupOperations contains all group-related database operations
func (db *DB) SaveGroup(ctx context.Context, group *pb.Group) error {
	query := `
		INSERT INTO groups (id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			kill_proof_id = excluded.kill_proof_id,
			kill_proof_minimum = excluded.kill_proof_minimum
	`
	_, err := db.db.ExecContext(ctx, query,
		group.Id,
		group.CreatorId,
		group.Title,
		group.KillProofId,
		group.KillProofMinimum,
		group.CreatedAtSec,
	)
	return err
}

func (db *DB) DeleteGroup(ctx context.Context, groupId string) error {
	_, err := db.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, groupId)
	return err
}

func (db *DB) GetGroup(ctx context.Context, id string) (*pb.Group, error) {
	query := `
		SELECT id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec 
		FROM groups 
		WHERE id = ?
	`
	var group pb.Group
	err := db.db.QueryRowContext(ctx, query, id).Scan(
		&group.Id,
		&group.CreatorId,
		&group.Title,
		&group.KillProofId,
		&group.KillProofMinimum,
		&group.CreatedAtSec,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &group, err
}

func (db *DB) ListGroups(ctx context.Context) ([]*pb.Group, error) {
	query := `
		SELECT id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec 
		FROM groups 
		ORDER BY created_at_sec DESC
	`
	rows, err := db.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*pb.Group
	for rows.Next() {
		var group pb.Group
		if err := rows.Scan(
			&group.Id,
			&group.CreatorId,
			&group.Title,
			&group.KillProofId,
			&group.KillProofMinimum,
			&group.CreatedAtSec,
		); err != nil {
			return nil, err
		}
		groups = append(groups, &group)
	}
	return groups, rows.Err()
}

// ApplicationOperations contains all application-related database operations
func (db *DB) SaveApplication(ctx context.Context, app *pb.GroupApplication, groupID string) error {
	query := `
		INSERT INTO applications (id, group_id, account_name) 
		VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET account_name = excluded.account_name
	`
	_, err := db.db.ExecContext(ctx, query, app.Id, groupID, app.AccountName)
	return err
}

func (db *DB) GetApplication(ctx context.Context, applicationId string) (*pb.GroupApplication, error) {
	query := `SELECT id, account_name, group_id FROM applications WHERE id = ?`
	var application pb.GroupApplication
	err := db.db.QueryRowContext(ctx, query, applicationId).Scan(
		&application.Id,
		&application.AccountName,
		&application.GroupId,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &application, err
}

func (db *DB) DeleteApplication(ctx context.Context, applicationId string) error {
	_, err := db.db.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, applicationId)
	return err
}

func (db *DB) ListApplications(ctx context.Context, groupID string) ([]*pb.GroupApplication, error) {
	query := `SELECT id, account_name FROM applications WHERE group_id = ?`
	rows, err := db.db.QueryContext(ctx, query, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []*pb.GroupApplication
	for rows.Next() {
		var app pb.GroupApplication
		if err := rows.Scan(&app.Id, &app.AccountName); err != nil {
			return nil, err
		}
		app.GroupId = groupID
		apps = append(apps, &app)
	}
	return apps, rows.Err()
}
