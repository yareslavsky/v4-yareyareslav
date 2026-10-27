package httplog

import (
	"log/slog"
	"marketplace/gateway/pkg/requestid"
	"net/http"
	"time"

	"github.com/go-chi/chi/middleware"
)

func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			writer := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				status := writer.Status()
				if status == 0 {
					status = http.StatusOK
				}
				level := slog.LevelInfo
				if status >= http.StatusInternalServerError {
					level = slog.LevelError
				}
				logger.LogAttrs(r.Context(), level, "HTTP request",
					slog.String("request_id", requestid.ID(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", status),
					slog.Float64("dur_ms", float64(time.Since(started))/float64(time.Millisecond)),
					slog.String("remote_addr", r.RemoteAddr),
				)
			}()
			next.ServeHTTP(writer, r)
		})
	}
}
