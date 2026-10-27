package recoverer

import (
	"log/slog"
	"marketplace/gateway/pkg/requestid"
	"marketplace/gateway/pkg/response"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/middleware"
)

func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				if value := recover(); value != nil {
					if value == http.ErrAbortHandler {
						panic(value)
					}
					logger.ErrorContext(r.Context(), "HTTP handler panic",
						"request_id", requestid.ID(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
						"panic", value,
						"stack", string(debug.Stack()),
					)
					if writer.Status() != 0 {
						panic(http.ErrAbortHandler)
					}
					if err := response.Body(writer, response.ErrorBody{
						Error: "internal", Message: "internal server error",
					}, http.StatusInternalServerError); err != nil {
						logger.ErrorContext(r.Context(), "write panic response", "request_id", requestid.ID(r.Context()), "error", err)
					}
				}
			}()
			next.ServeHTTP(writer, r)
		})
	}
}
