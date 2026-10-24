package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AhmetYorat/soguk-zincir/backend/internal/auth"
	"github.com/AhmetYorat/soguk-zincir/backend/internal/httpx"
)

type tokenResponse struct {
	AccessToken  string     `json:"access_token"`
	TokenType    string     `json:"token_type"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RefreshToken string     `json:"refresh_token"`
	User         userPublic `json:"user"`
}

type userPublic struct {
	ID          int64             `json:"id"`
	Email       string            `json:"email"`
	FullName    string            `json:"full_name"`
	Role        auth.Role         `json:"role"`
	CustomerID  *int64            `json:"customer_id"`
	Permissions []auth.Permission `json:"permissions"`
}

var errBadCredentials = httpx.Errorf(http.StatusUnauthorized, "invalid_credentials", "e-mail or password is wrong")

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v := httpx.Validation{}
	v.Check(strings.Contains(in.Email, "@"), "email", "must be an e-mail address")
	v.Check(in.Password != "", "password", "is required")
	if err := v.Err(); err != nil {
		return err
	}
	var u userPublic
	var hash string
	var active bool
	err := s.pool.QueryRow(r.Context(), `
		SELECT id, email, full_name, role, customer_id, password_hash, active
		FROM users WHERE lower(email) = lower($1)`, in.Email).
		Scan(&u.ID, &u.Email, &u.FullName, &u.Role, &u.CustomerID, &hash, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		auth.CheckDummy(in.Password)
		return errBadCredentials
	}
	if err != nil {
		return err
	}
	ok, err := auth.CheckPassword(hash, in.Password)
	if err != nil {
		return err
	}
	if !ok || !active {
		return errBadCredentials
	}
	return s.issueTokens(w, r, u, uuid.New())
}

func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, u userPublic, family uuid.UUID) error {
	p := auth.Principal{UserID: u.ID, Role: u.Role, Email: u.Email}
	if u.CustomerID != nil {
		p.CustomerID = *u.CustomerID
	}
	access, exp, err := s.issuer.Access(p)
	if err != nil {
		return err
	}
	refresh, hash, err := auth.NewRefreshToken()
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(r.Context(), `
		INSERT INTO refresh_tokens (user_id, family, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		u.ID, family, hash, s.now().Add(s.cfg.RefreshTTL)); err != nil {
		return err
	}
	u.Permissions = auth.Permissions(u.Role)
	httpx.JSON(w, http.StatusOK, tokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresAt: exp, RefreshToken: refresh, User: u})
	return nil
}

// refresh rotates the refresh token. Presenting a token that was already
// rotated means it leaked: the whole family is revoked.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	invalid := httpx.Errorf(http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id int64
	var family uuid.UUID
	var revoked *time.Time
	var expires time.Time
	var u userPublic
	var active bool
	err = tx.QueryRow(ctx, `
		SELECT t.id, t.family, t.revoked_at, t.expires_at, u.id, u.email, u.full_name, u.role, u.customer_id, u.active
		FROM refresh_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 FOR UPDATE OF t`, auth.HashRefreshToken(in.RefreshToken)).
		Scan(&id, &family, &revoked, &expires, &u.ID, &u.Email, &u.FullName, &u.Role, &u.CustomerID, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid
	}
	if err != nil {
		return err
	}
	if revoked != nil {
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE family = $1 AND revoked_at IS NULL`, family); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		httpx.Logger(ctx).Warn("refresh token reuse detected, family revoked", "user_id", u.ID)
		return invalid
	}
	if !active || s.now().After(expires) {
		return invalid
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.issueTokens(w, r, u, family)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, err := s.pool.Exec(r.Context(), `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE family = (SELECT family FROM refresh_tokens WHERE token_hash = $1) AND revoked_at IS NULL`,
		auth.HashRefreshToken(in.RefreshToken))
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	p := principal(r.Context())
	var u userPublic
	err := s.pool.QueryRow(r.Context(), `SELECT id, email, full_name, role, customer_id FROM users WHERE id = $1`, p.UserID).
		Scan(&u.ID, &u.Email, &u.FullName, &u.Role, &u.CustomerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	u.Permissions = auth.Permissions(u.Role)
	httpx.JSON(w, http.StatusOK, u)
	return nil
}
