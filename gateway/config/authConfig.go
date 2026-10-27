package config

import (
	"net/http"
)

type Middleware interface {
	CheckUser(next http.Handler, role string) http.HandlerFunc
}

type AuthConfig struct {
	Middleware Middleware
}

func NewAuthConfig(middleware Middleware) AuthConfig {
	return AuthConfig{
		Middleware: middleware,
	}
}
