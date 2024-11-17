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

func (db *DB) GetGroup(ctx context.Context, groupId string) (*pb.Group, error) {
	row := db.db.QueryRowContext(ctx, `SELECT id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec FROM groups WHERE id = ?`, groupId)
	var group pb.Group
	err := row.Scan(&group.Id, &group.CreatorId, &group.Title, &group.KillProofId, &group.KillProofMinimum, &group.CreatedAtSec)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &group, nil
}

func (db *DB) DeleteGroup(ctx context.Context, groupId string) error {
	_, err := db.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, groupId)
	return err
}

func (db *DB) ListGroups(ctx context.Context) ([]*pb.Group, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec FROM groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*pb.Group
	for rows.Next() {
		var group pb.Group
		if err := rows.Scan(&group.Id, &group.CreatorId, &group.Title, &group.KillProofId, &group.KillProofMinimum, &group.CreatedAtSec); err != nil {
			return nil, err
		}
		groups = append(groups, &group)
	}
	return groups, nil
}

func (db *DB) SaveApplication(ctx context.Context, application *pb.GroupApplication, groupId string) error {
	_, err := db.db.ExecContext(ctx, `
		INSERT INTO applications (id, group_id, account_name)
		VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			account_name=excluded.account_name
	`, application.Id, groupId, application.AccountName)
	return err
}

func (db *DB) GetApplication(ctx context.Context, applicationId string) (*pb.GroupApplication, error) {
	row := db.db.QueryRowContext(ctx, `SELECT id, account_name, group_id FROM applications WHERE id = ?`, applicationId)
	var application pb.GroupApplication
	err := row.Scan(&application.Id, &application.AccountName, &application.GroupId)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &application, nil
}

func (db *DB) DeleteApplication(ctx context.Context, applicationId string) error {
	_, err := db.db.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, applicationId)
	return err
}

func (db *DB) ListApplications(ctx context.Context, groupId string) ([]*pb.GroupApplication, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT id, account_name FROM applications WHERE group_id = ?`, groupId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var applications []*pb.GroupApplication
	for rows.Next() {
		var application pb.GroupApplication
		if err := rows.Scan(&application.Id, &application.AccountName); err != nil {
			return nil, err
		}
		applications = append(applications, &application)
	}
	return applications, nil
}
