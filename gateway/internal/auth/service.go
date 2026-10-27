package auth

import (
	"context"
	"errors"
	"fmt"
	"marketplace/gateway/pkg/jwt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var ErrNoUserWithSuchEmail = errors.New("no users with such email")
var ErrWrongPassword = errors.New("wrong password")
var ErrWrongTokenType = errors.New("wrong token type")
var ErrExpiredToken = errors.New("expired token")
var ErrCorruptedToken = errors.New("corrupted token")

type TokenManager interface {
	GenerateAccessToken(userID, role string) (string, error)
	GenerateRefreshToken(userID, role string) (string, error)
	Parse(token string) (*jwt.Claims, error)
}

type Repository interface {
	CreateUser(ctx context.Context, user User) (User, error)
	FindUser(ctx context.Context, email string) (User, error)
}

type AuthService struct {
	tokenManager TokenManager
	repository   Repository
}

func NewAuthService(repository Repository, tokenManager TokenManager) *AuthService {
	return &AuthService{
		tokenManager: tokenManager,
		repository:   repository,
	}
}

func (s *AuthService) generateTokens(id, role string) (string, string, error) {
	accessToken, err := s.tokenManager.GenerateAccessToken(id, role)
	if err != nil {
		return "", "", err
	}

	refreshToken, err := s.tokenManager.GenerateRefreshToken(id, role)
	if err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}

func (s *AuthService) Register(ctx context.Context, req RegisterUserRequest) (RegisterUserResponse, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return RegisterUserResponse{}, fmt.Errorf("AuthService register: %w", err)
	}

	user := User{
		Email:        strings.ToLower(strings.TrimSpace(req.Email)),
		PasswordHash: string(passwordHash),
		Role:         req.Role,
	}

	newUser, err := s.repository.CreateUser(ctx, user)
	if err != nil {
		return RegisterUserResponse{}, fmt.Errorf("AuthService register: %w", err)
	}

	accessToken, refreshToken, err := s.generateTokens(string(*newUser.ID), newUser.Role)
	if err != nil {
		return RegisterUserResponse{}, fmt.Errorf("AuthService register: %w", err)
	}

	return RegisterUserResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		User: UserResponse{
			ID:    string(*newUser.ID),
			Email: user.Email,
			Role:  user.Role,
		},
	}, nil
}

func (s *AuthService) Login(ctx context.Context, req LoginUserRequest) (LoginUserResponse, error) {
	user, err := s.repository.FindUser(ctx, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return LoginUserResponse{}, ErrNoUserWithSuchEmail
		}
		return LoginUserResponse{}, fmt.Errorf("login: find user: %w", err)
	}

	err = bcrypt.CompareHashAndPassword(
		[]byte(user.PasswordHash),
		[]byte(req.Password),
	)
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return LoginUserResponse{}, fmt.Errorf("login: compare passwords: %w", ErrWrongPassword)
		}
		return LoginUserResponse{}, fmt.Errorf("login: compare passwords: %w", ErrWrongPassword)
	}

	accessToken, refreshToken, err := s.generateTokens(string(*user.ID), user.Role)
	if err != nil {
		return LoginUserResponse{}, fmt.Errorf("login: generateTokens: %w", err)
	}
	return LoginUserResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}

func (s *AuthService) Refresh(ctx context.Context, req RefreshTokenRequest) (RefreshTokenResponse, error) {
	claims, err := s.tokenManager.Parse(req.RefreshToken)
	if err != nil {
		return RefreshTokenResponse{}, fmt.Errorf("refresh: parse token: %w", ErrCorruptedToken)
	}
	if claims.TokenType != "refresh" {
		return RefreshTokenResponse{}, fmt.Errorf("refresh: parse token: %w", ErrWrongTokenType)
	}
	if claims.ExpiresAt == nil || !time.Now().Before(claims.ExpiresAt.Time) {
		return RefreshTokenResponse{}, fmt.Errorf("refresh: parse token: %w", ErrExpiredToken)
	}

	accessToken, refreshToken, err := s.generateTokens(claims.Subject, claims.Role)
	if err != nil {
		return RefreshTokenResponse{}, fmt.Errorf("refresh: generate tokens: %w", err)
	}

	return RefreshTokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}
