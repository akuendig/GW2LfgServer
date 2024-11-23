package database

import (
	"context"
	"database/sql"
	pb "gw2lfgserver/pb"
	"log/slog"
	"time"

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
			created_at_sec INTEGER NOT NULL,
			updated_at_sec INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS applications (
			id TEXT PRIMARY KEY,
			group_id TEXT NOT NULL,
			account_name TEXT NOT NULL,
			created_at_sec INTEGER NOT NULL,
			updated_at_sec INTEGER NOT NULL,
			FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_groups_creator ON groups(creator_id);
		CREATE INDEX IF NOT EXISTS idx_applications_group ON applications(group_id);
	`
	_, err := db.Exec(schema)
	return err
}

// GroupOperations contains all group-related database operations
func (db *DB) SaveGroup(ctx context.Context, group *pb.Group) (*pb.Group, error) {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.SaveGroup", slog.Duration("elapsed", time.Since(start))) }()
	query := `
        INSERT INTO groups (id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec, updated_at_sec)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            title = excluded.title,
            kill_proof_id = excluded.kill_proof_id,
            kill_proof_minimum = excluded.kill_proof_minimum,
			updated_at_sec = excluded.updated_at_sec
        RETURNING id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec, updated_at_sec
    `
	var savedGroup pb.Group
	err := db.db.QueryRowContext(ctx, query,
		group.Id,
		group.CreatorId,
		group.Title,
		group.KillProofId,
		group.KillProofMinimum,
		group.CreatedAtSec,
		group.UpdatedAtSec,
	).Scan(
		&savedGroup.Id,
		&savedGroup.CreatorId,
		&savedGroup.Title,
		&savedGroup.KillProofId,
		&savedGroup.KillProofMinimum,
		&savedGroup.CreatedAtSec,
		&savedGroup.UpdatedAtSec,
	)
	if err != nil {
		return nil, err
	}
	return &savedGroup, nil
}

func (db *DB) DeleteGroup(ctx context.Context, groupId string) error {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.DeleteGroup", slog.Duration("elapsed", time.Since(start))) }()
	_, err := db.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, groupId)
	return err
}

func (db *DB) DeleteGroupsUpdatedBefore(ctx context.Context, t time.Time) ([]*pb.Group, error) {
	start := time.Now()
	defer func() {
		slog.InfoContext(ctx, "db.DeleteGroupsUpdatedBefore", slog.Duration("elapsed", time.Since(start)))
	}()
	query := `
        DELETE FROM groups 
        WHERE updated_at_sec < ? 
        RETURNING id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec, updated_at_sec
    `
	rows, err := db.db.QueryContext(ctx, query, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deletedGroups []*pb.Group
	for rows.Next() {
		var group pb.Group
		if err := rows.Scan(
			&group.Id,
			&group.CreatorId,
			&group.Title,
			&group.KillProofId,
			&group.KillProofMinimum,
			&group.CreatedAtSec,
			&group.UpdatedAtSec,
		); err != nil {
			return nil, err
		}
		deletedGroups = append(deletedGroups, &group)
	}

	return deletedGroups, rows.Err()
}

func (db *DB) GetGroup(ctx context.Context, id string) (*pb.Group, error) {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.GetGroup", slog.Duration("elapsed", time.Since(start))) }()
	query := `
		SELECT id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec, updated_at_sec
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
		&group.UpdatedAtSec,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &group, err
}

func (db *DB) ListGroups(ctx context.Context) ([]*pb.Group, error) {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.ListGroups", slog.Duration("elapsed", time.Since(start))) }()
	query := `
		SELECT id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec, updated_at_sec
		FROM groups 
		ORDER BY updated_at_sec DESC
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
			&group.UpdatedAtSec,
		); err != nil {
			return nil, err
		}
		groups = append(groups, &group)
	}
	return groups, rows.Err()
}

// ApplicationOperations contains all application-related database operations
func (db *DB) SaveApplication(ctx context.Context, app *pb.GroupApplication, groupID string) (*pb.GroupApplication, error) {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.SaveApplication", slog.Duration("elapsed", time.Since(start))) }()
	query := `
        INSERT INTO applications (id, group_id, account_name, created_at_sec, updated_at_sec)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            account_name = excluded.account_name
			updated_at_sec = excluded.updated_at_sec
        RETURNING id, group_id, account_name, created_at_sec, updated_at_sec
    `
	var savedApp pb.GroupApplication
	err := db.db.QueryRowContext(ctx, query,
		app.Id,
		groupID,
		app.AccountName,
	).Scan(
		&savedApp.Id,
		&savedApp.GroupId,
		&savedApp.AccountName,
		&savedApp.CreatedAtSec,
		&savedApp.UpdatedAtSec,
	)
	if err != nil {
		return nil, err
	}
	return &savedApp, nil
}

func (db *DB) GetApplication(ctx context.Context, applicationId string) (*pb.GroupApplication, error) {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.GetApplication", slog.Duration("elapsed", time.Since(start))) }()
	query := `SELECT id, account_name, group_id, created_at_sec, updated_at_sec FROM applications WHERE id = ?`
	var application pb.GroupApplication
	err := db.db.QueryRowContext(ctx, query, applicationId).Scan(
		&application.Id,
		&application.AccountName,
		&application.GroupId,
		&application.CreatedAtSec,
		&application.UpdatedAtSec,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &application, err
}

func (db *DB) DeleteApplication(ctx context.Context, applicationId string) error {
	start := time.Now()
	defer func() { slog.InfoContext(ctx, "db.DeleteApplication", slog.Duration("elapsed", time.Since(start))) }()
	_, err := db.db.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, applicationId)
	return err
}

func (db *DB) DeleteApplicationsUpdatedBefore(ctx context.Context, t time.Time) ([]*pb.GroupApplication, error) {
	start := time.Now()
	defer func() {
		slog.InfoContext(ctx, "db.DeleteApplicationsUpdatedBefore", slog.Duration("elapsed", time.Since(start)))
	}()
	query := `
        DELETE FROM applications 
        WHERE updated_at_sec < ? 
        RETURNING id, group_id, account_name, created_at_sec, updated_at_sec
    `
	rows, err := db.db.QueryContext(ctx, query, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deletedApps []*pb.GroupApplication
	for rows.Next() {
		var app pb.GroupApplication
		if err := rows.Scan(
			&app.Id,
			&app.GroupId,
			&app.AccountName,
			&app.CreatedAtSec,
			&app.UpdatedAtSec,
		); err != nil {
			return nil, err
		}
		deletedApps = append(deletedApps, &app)
	}

	return deletedApps, rows.Err()
}

func (db *DB) ListApplicationsForGroup(ctx context.Context, groupID string) ([]*pb.GroupApplication, error) {
	start := time.Now()
	defer func() {
		slog.InfoContext(ctx, "db.ListApplicationsForGroup", slog.Duration("elapsed", time.Since(start)))
	}()
	query := `SELECT id, group_id, account_name, created_at_sec, updated_at_sec FROM applications WHERE group_id = ?`
	rows, err := db.db.QueryContext(ctx, query, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []*pb.GroupApplication
	for rows.Next() {
		var app pb.GroupApplication
		if err := rows.Scan(
			&app.Id,
			&app.GroupId,
			&app.AccountName,
			&app.CreatedAtSec,
			&app.UpdatedAtSec); err != nil {
			return nil, err
		}
		apps = append(apps, &app)
	}
	return apps, rows.Err()
}

func (db *DB) ListApplicationsForAccount(ctx context.Context, accountName string) ([]*pb.GroupApplication, error) {
	start := time.Now()
	defer func() {
		slog.InfoContext(ctx, "db.ListApplicationsForGroup", slog.Duration("elapsed", time.Since(start)))
	}()
	query := `SELECT id, group_id, account_name, created_at_sec, updated_at_sec FROM applications WHERE account_name = ?`
	rows, err := db.db.QueryContext(ctx, query, accountName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []*pb.GroupApplication
	for rows.Next() {
		var app pb.GroupApplication
		if err := rows.Scan(
			&app.Id,
			&app.GroupId,
			&app.AccountName,
			&app.CreatedAtSec,
			&app.UpdatedAtSec); err != nil {
			return nil, err
		}
		app.GroupId = accountName
		apps = append(apps, &app)
	}
	return apps, rows.Err()
}

type TouchResult struct {
	Groups       []*pb.Group
	Applications []*pb.GroupApplication
}

func (db *DB) TouchGroupsAndApplications(ctx context.Context, accountName string) (*TouchResult, error) {
	start := time.Now()
	defer func() {
		slog.InfoContext(ctx, "db.TouchGroupsAndApplications", slog.Duration("elapsed", time.Since(start)))
	}()

	result := &TouchResult{}

	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Update and get groups
	updateTime := time.Now().Unix()
	rows, err := tx.QueryContext(ctx, `
        UPDATE groups 
        SET updated_at_sec = ? 
        WHERE creator_id = ?
        RETURNING id, creator_id, title, kill_proof_id, kill_proof_minimum, created_at_sec, updated_at_sec
    `, updateTime, accountName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var group pb.Group
		if err := rows.Scan(
			&group.Id,
			&group.CreatorId,
			&group.Title,
			&group.KillProofId,
			&group.KillProofMinimum,
			&group.CreatedAtSec,
			&group.UpdatedAtSec,
		); err != nil {
			return nil, err
		}
		result.Groups = append(result.Groups, &group)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	// Update and get applications
	rows, err = tx.QueryContext(ctx, `
        UPDATE applications 
        SET updated_at_sec = ? 
        WHERE account_name = ?
        RETURNING id, group_id, account_name, created_at_sec, updated_at_sec
    `, updateTime, accountName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var app pb.GroupApplication
		if err := rows.Scan(
			&app.Id,
			&app.GroupId,
			&app.AccountName,
			&app.CreatedAtSec,
			&app.UpdatedAtSec,
		); err != nil {
			return nil, err
		}
		result.Applications = append(result.Applications, &app)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

func (db *DB) Close() error {
	return db.db.Close()
}
