package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrEmailAlreadyExists = errors.New("email already exists")
var ErrUserNotFound = errors.New("user not found")

type AuthDBRepository struct {
	pool *pgxpool.Pool
}

func NewAuthDBRepository(pool *pgxpool.Pool) *AuthDBRepository {
	return &AuthDBRepository{
		pool: pool,
	}
}

func (r *AuthDBRepository) CreateUser(ctx context.Context, user User) (User, error) {
	const query = `
		INSERT INTO users (id, email, password_hash, role)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id
	`

	var id string

	err := r.pool.QueryRow(
		ctx,
		query,
		user.Email,
		user.PasswordHash,
		user.Role,
	).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			pgErr.Code == "23505" &&
			pgErr.ConstraintName == "users_email_key" {
			return User{}, ErrEmailAlreadyExists
		}

		return User{}, fmt.Errorf("create user: %w", err)
	}

	userID := UserID(id)
	user.ID = &userID

	return user, nil
}

func (r *AuthDBRepository) FindUser(ctx context.Context, email string) (User, error) {
	const query = `
		SELECT id, email, password_hash, role
		FROM users u
		WHERE email = $1
	`
	var (
		id           string
		userEmail    string
		passwordHash string
		role         string
	)

	err := r.pool.QueryRow(
		ctx,
		query,
		email,
	).Scan(
		&id,
		&userEmail,
		&passwordHash,
		&role,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("find user: %w", err)
	}

	userID := UserID(id)

	return User{
		ID:           &userID,
		Email:        userEmail,
		PasswordHash: passwordHash,
		Role:         role,
	}, nil
}
