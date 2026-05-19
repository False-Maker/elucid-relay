package httpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
)

func classifyNetworkError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(strings.ToLower(err.Error()), "unexpected eof") {
		return "unexpected_eof"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" {
			if errors.Is(opErr.Err, syscall.ECONNREFUSED) {
				return "connection_refused"
			}
			return "connect_error"
		}
	}
	var tlsRecordHeader *tls.RecordHeaderError
	if errors.As(err, &tlsRecordHeader) {
		return "tls_error"
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return "tls_hostname_error"
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return "tls_certificate_error"
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return "tls_certificate_error"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "tls_certificate_error"
	}
	return classifyNetworkErrorText(err.Error())
}

func classifyNetworkErrorText(message string) string {
	text := strings.ToLower(strings.TrimSpace(message))
	switch {
	case text == "":
		return ""
	case strings.Contains(text, "unexpected eof"):
		return "unexpected_eof"
	case strings.Contains(text, "timeout") || strings.Contains(text, "deadline exceeded"):
		return "timeout"
	case strings.Contains(text, "no such host") || strings.Contains(text, "dns"):
		return "dns_error"
	case strings.Contains(text, "connection refused"):
		return "connection_refused"
	case strings.Contains(text, "connection reset"):
		return "connection_reset"
	case strings.Contains(text, "tls") || strings.Contains(text, "certificate") || strings.Contains(text, "handshake"):
		return "tls_error"
	case strings.Contains(text, "proxy connect") || strings.Contains(text, "proxy"):
		return "proxy_error"
	case strings.Contains(text, "socks5"):
		return "proxy_error"
	default:
		return "network_error"
	}
}

func upstreamFailureCode(err error) string {
	var appErr appError
	if errors.As(err, &appErr) {
		return appErr.code
	}
	kind := classifyNetworkError(err)
	if kind == "" {
		return "upstream_error"
	}
	return "upstream_" + kind
}

func upstreamFailureMessage(err error) string {
	var appErr appError
	if errors.As(err, &appErr) {
		return appErr.message
	}
	kind := classifyNetworkError(err)
	switch kind {
	case "timeout":
		return "Upstream request timed out."
	case "unexpected_eof":
		return "Upstream closed the connection unexpectedly."
	case "dns_error":
		return "Upstream DNS resolution failed."
	case "connection_refused":
		return "Upstream connection was refused."
	case "connection_reset":
		return "Upstream connection was reset."
	case "tls_error", "tls_hostname_error", "tls_certificate_error":
		return "Upstream TLS handshake failed."
	case "proxy_error":
		return "Upstream proxy connection failed."
	default:
		return "Upstream request failed."
	}
}

func upstreamStatusErrorCode(status int, body []byte) string {
	text := strings.ToLower(string(body))
	if status == http.StatusBadRequest && isAnthropicSignatureErrorText(text) {
		return "anthropic_signature_error"
	}
	switch status {
	case http.StatusUnauthorized:
		return "upstream_authentication_failed"
	case http.StatusForbidden:
		if strings.Contains(text, "cloudflare") || strings.Contains(text, "challenge") {
			return "upstream_challenge"
		}
		return "upstream_forbidden"
	case http.StatusTooManyRequests:
		return "upstream_rate_limited"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "upstream_unavailable"
	default:
		if status >= 500 {
			return "upstream_server_error"
		}
		return "upstream_rejected"
	}
}

func isAnthropicSignatureErrorText(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	hasThinking := strings.Contains(text, "thinking") || strings.Contains(text, "redacted_thinking")
	switch {
	case strings.Contains(text, "signature") && hasThinking:
		return true
	case strings.Contains(text, "expected") && hasThinking:
		return true
	case strings.Contains(text, "cannot be modified") && hasThinking:
		return true
	case strings.Contains(text, "empty content") && hasThinking:
		return true
	case strings.Contains(text, "non-empty content") && hasThinking:
		return true
	default:
		return false
	}
}
