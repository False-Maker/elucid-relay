package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

type contextKey string

const requestIDKey contextKey = "request_id"

type envelope struct {
	Data any `json:"data,omitempty"`
	Meta any `json:"meta,omitempty"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
}

type appError struct {
	status  int
	code    string
	message string
	typ     string
}

func (e appError) Error() string {
	return e.message
}

func badRequest(message string) appError {
	return appError{status: http.StatusBadRequest, code: "bad_request", message: message, typ: "invalid_request_error"}
}

func requestEntityTooLarge(message string) appError {
	return appError{status: http.StatusRequestEntityTooLarge, code: "request_body_too_large", message: message, typ: "invalid_request_error"}
}

func unauthorized(message string) appError {
	return appError{status: http.StatusUnauthorized, code: "unauthorized", message: message, typ: "authentication_error"}
}

func forbidden(message string) appError {
	return appError{status: http.StatusForbidden, code: "forbidden", message: message, typ: "permission_error"}
}

func notFound(message string) appError {
	return appError{status: http.StatusNotFound, code: "not_found", message: message, typ: "invalid_request_error"}
}

func conflict(message string) appError {
	return appError{status: http.StatusConflict, code: "conflict", message: message, typ: "invalid_request_error"}
}

func billingError(code string, message string) appError {
	return appError{status: http.StatusPaymentRequired, code: code, message: message, typ: "billing_error"}
}

func upstreamUnavailable(code string, message string) appError {
	return appError{status: http.StatusServiceUnavailable, code: code, message: message, typ: "upstream_error"}
}

func writeJSON(w http.ResponseWriter, status int, data any, meta any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data, Meta: meta})
}

func writeRawJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var appErr appError
	if !errors.As(err, &appErr) {
		appErr = appError{
			status:  http.StatusInternalServerError,
			code:    "internal_error",
			message: "Internal server error.",
			typ:     "server_error",
		}
		slog.Error("request failed", "error", err, "request_id", requestIDFromContext(r.Context()))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: apiError{
			Code:      appErr.code,
			Message:   appErr.message,
			Type:      appErr.typ,
			RequestID: requestIDFromContext(r.Context()),
		},
	})
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return badRequest("Invalid JSON request body.")
	}
	return nil
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			generated, err := security.NewOpaqueToken("req_", 16)
			if err != nil {
				writeError(w, r, err)
				return
			}
			requestID = generated
		}

		w.Header().Set("X-Request-Id", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID)))
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic recovered", "panic", recovered, "stack", string(debug.Stack()))
				writeError(w, r, errors.New("panic recovered"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey).(string)
	return requestID
}
