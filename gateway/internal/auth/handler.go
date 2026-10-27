package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"marketplace/gateway/pkg/request"
	"marketplace/gateway/pkg/response"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi"
	"github.com/go-playground/validator"
	"golang.org/x/crypto/bcrypt"
)

type Service interface {
	Register(ctx context.Context, r RegisterUserRequest) (RegisterUserResponse, error)
	Login(ctx context.Context, r LoginUserRequest) (LoginUserResponse, error)
	Refresh(ctx context.Context, r RefreshTokenRequest) (RefreshTokenResponse, error)
}

type AuthHandler struct {
	validate *validator.Validate
	service  Service
}

func NewAuthHandler(
	r chi.Router,
	validate *validator.Validate,
	s Service,
) *AuthHandler {
	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		return strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
	})
	h := &AuthHandler{
		service:  s,
		validate: validate,
	}

	authRouter := chi.NewRouter()
	authRouter.Post("/register", h.Register)
	authRouter.Post("/login", h.Login)
	authRouter.Post("/refresh", h.Refresh)

	r.Mount("/auth", authRouter)

	return h
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	registerReq, err := request.Body[RegisterUserRequest](w, r)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	if err := h.validate.Struct(registerReq); err != nil {
		writeValidationError(w, err)
		return
	}

	result, err := h.service.Register(r.Context(), registerReq)
	if err != nil {
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			response.ValidationError(w, []response.FieldError{{Field: "password", Message: "must be at most 72 bytes"}})
			return
		}
		if errors.Is(err, ErrEmailAlreadyExists) {
			writeConflictError(w, err.Error())
			return
		}
		writeInternalError(w, err.Error())
		return
	}

	resultJson, err := json.Marshal(result)
	if err != nil {
		writeInternalError(w, err.Error())
		return
	}

	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if _, err := w.Write(resultJson); err != nil {
		log.Printf("write register response: %v", err)
	}

}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	loginReq, err := request.Body[LoginUserRequest](w, r)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	if err := h.validate.Struct(loginReq); err != nil {
		writeValidationError(w, err)
		return
	}

	result, err := h.service.Login(r.Context(), loginReq)
	if err != nil {
		if errors.Is(err, ErrWrongPassword) || errors.Is(err, ErrNoUserWithSuchEmail) {
			writeUnauthenticatedError(w, err.Error())
			return
		}

		writeInternalError(w, err.Error())
		return
	}

	resultJson, err := json.Marshal(result)
	if err != nil {
		writeInternalError(w, err.Error())
		return
	}

	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(resultJson); err != nil {
		log.Printf("write login response: %v", err)
	}
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	refreshReq, err := request.Body[RefreshTokenRequest](w, r)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	if refreshReq.RefreshToken == "" {
		writeUnauthenticatedError(w, "refresh token is required")
		return
	}

	result, err := h.service.Refresh(r.Context(), refreshReq)
	if err != nil {
		if errors.Is(err, ErrWrongTokenType) ||
			errors.Is(err, ErrExpiredToken) ||
			errors.Is(err, ErrCorruptedToken) {
			writeUnauthenticatedError(w, err.Error())
			return
		}
		writeInternalError(w, err.Error())
		return
	}

	resultJson, err := json.Marshal(result)
	if err != nil {
		writeInternalError(w, err.Error())
		return
	}

	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(resultJson); err != nil {
		log.Printf("write refresh response: %v", err)
	}
}
