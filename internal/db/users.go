package db

import (
	"context"
	"database/sql"
)

const (
	RoleAdmin  = "admin"
	RolePlayer = "player"
)

type User struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	MCUsername   *string `json:"mc_username"`
	PasswordHash string  `json:"-"`
	Role         string  `json:"role"`
	CreatedAt    int64   `json:"created_at"`
	CreatedBy    *string `json:"created_by"`
	LastLoginAt  *int64  `json:"last_login_at"`
}

const userCols = `id, username, mc_username, password_hash, role, created_at, created_by, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var mc, by sql.NullString
	var last sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &mc, &u.PasswordHash, &u.Role, &u.CreatedAt, &by, &last); err != nil {
		return nil, notFound(err)
	}
	u.MCUsername, u.CreatedBy, u.LastLoginAt = strPtr(mc), strPtr(by), int64Ptr(last)
	return &u, nil
}

// CreateUser inserts u, assigning ID and CreatedAt.
func (d *DB) CreateUser(ctx context.Context, u *User) error {
	u.ID, u.CreatedAt = newID(), now()
	_, err := d.ExecContext(ctx, `INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, nullStr(u.MCUsername), u.PasswordHash, u.Role, u.CreatedAt, nullStr(u.CreatedBy), nil)
	return err
}

func (d *DB) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(d.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (d *DB) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(d.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

func (d *DB) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) TouchLogin(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now(), id)
	return err
}

func (d *DB) SetUserRole(ctx context.Context, id, role string) error {
	return execOne(d.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id))
}

func (d *DB) SetPasswordHash(ctx context.Context, id, hash string) error {
	return execOne(d.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id))
}

func (d *DB) DeleteUser(ctx context.Context, id string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id))
}

func execOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
