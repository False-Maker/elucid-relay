package httpserver

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	utls "github.com/refraction-networking/utls"
)

const (
	codexRustlsJA3Hash = "d39e1be3241d516b1f714bd47c2bc968"
	node24JA3Hash      = "44f88fca027f27bab4bb08d4af15f23e"
)

type tlsFingerprintConnContextKey struct{}

func codexOfficialHTTPTransport(route routeInfo, disableCompression bool) (*http.Transport, error) {
	var dialTLS func(context.Context, string, string) (net.Conn, error)
	if routeTLSFingerprintProfile(route) != "" {
		dialTLS = routeUTLSDialTLSContext(route)
	}
	return upstreamTransport(route, disableCompression, dialTLS)
}

func configureCodexOfficialWebSocketDialer(route routeInfo, dialer *websocket.Dialer) {
	configureTLSFingerprintWebSocketDialer(route, dialer)
}

func configureTLSFingerprintWebSocketDialer(route routeInfo, dialer *websocket.Dialer) {
	if dialer == nil || routeTLSFingerprintProfile(route) == "" || (strings.TrimSpace(route.ProxyURL) != "" && !isDirectProxyMode(route.ProxyURL)) {
		return
	}
	dialer.NetDialTLSContext = routeUTLSDialTLSContext(route)
}

func codexTLSFingerprintEnabled(route routeInfo) bool {
	return routeTLSFingerprintProfile(route) != ""
}

func routeTLSFingerprintProfile(route routeInfo) string {
	profile := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		routeMetadataString(route, routeTLSMetadataNamespace(route), "tls_fingerprint", "tls_client_hello", "ja3_profile", "tls_profile"),
		routeMetadataString(route, "tls", "fingerprint", "profile", "client_hello", "ja3_profile"),
		routeMetadataString(route, "official_client", "tls_fingerprint", "tls_client_hello", "ja3_profile", "tls_profile"),
	)))
	switch profile {
	case "off", "false", "disabled", "disable", "none", "go", "golang", "crypto_tls":
		return ""
	case "rustls", "codex", "codex_rustls":
		return "codex_rustls"
	case "node", "nodejs", "node24", "node_24", "claude", "claude_code", "official", "chrome", "chromium":
		return "node24"
	case "":
		switch {
		case isCodexOfficialRoute(route):
			return "codex_rustls"
		case isClaudeCodeRoute(route), isGeminiOfficialTLSRoute(route), isAgentOfficialLikeRoute(route):
			return "node24"
		default:
			return ""
		}
	default:
		return profile
	}
}

func routeTLSMetadataNamespace(route routeInfo) string {
	switch {
	case isCodexOfficialRoute(route):
		return "codex"
	case isClaudeCodeRoute(route):
		return "claude"
	case isGeminiOfficialTLSRoute(route):
		return "gemini"
	case strings.Contains(strings.ToLower(route.ProviderType), "antigravity"):
		return "antigravity"
	case strings.Contains(strings.ToLower(route.ProviderType), "kiro"):
		return "kiro"
	case strings.Contains(strings.ToLower(route.ProviderType), "windsurf") || strings.Contains(strings.ToLower(route.ProviderType), "codeium"):
		return "windsurf"
	case isGitHubCopilotRoute(route):
		return "github"
	default:
		return "official_client"
	}
}

func isGeminiOfficialTLSRoute(route routeInfo) bool {
	providerType := strings.ToLower(strings.TrimSpace(route.ProviderType))
	tokenProvider := strings.ToLower(strings.TrimSpace(route.TokenProvider))
	return isGeminiCLIRoute(route) ||
		strings.Contains(providerType, "gemini") ||
		strings.Contains(tokenProvider, "gemini") ||
		strings.Contains(tokenProvider, "google_gemini")
}

func isAgentOfficialLikeRoute(route routeInfo) bool {
	providerType := strings.ToLower(strings.TrimSpace(route.ProviderType))
	tokenProvider := strings.ToLower(strings.TrimSpace(route.TokenProvider))
	switch {
	case strings.Contains(providerType, "antigravity"), strings.Contains(tokenProvider, "antigravity"):
		return true
	case strings.Contains(providerType, "kiro"), strings.Contains(tokenProvider, "kiro"):
		return true
	case strings.Contains(providerType, "windsurf"), strings.Contains(providerType, "codeium"):
		return true
	case strings.Contains(tokenProvider, "windsurf"), strings.Contains(tokenProvider, "codeium"):
		return true
	default:
		return false
	}
}

func routeUTLSDialTLSContext(route routeInfo) func(context.Context, string, string) (net.Conn, error) {
	profile := routeTLSFingerprintProfile(route)
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		conn, ok := ctx.Value(tlsFingerprintConnContextKey{}).(net.Conn)
		if !ok || conn == nil {
			var dialer net.Dialer
			var err error
			conn, err = dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
			defer conn.SetDeadline(time.Time{})
		}

		host := tlsServerNameFromAddr(addr)
		uconn := utls.UClient(conn, &utls.Config{
			ServerName: host,
			MinVersion: utls.VersionTLS12,
			MaxVersion: utls.VersionTLS13,
		}, utls.HelloCustom)
		spec := routeClientHelloSpec(profile, host)
		if err := uconn.ApplyPreset(&spec); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if err := uconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return uconn, nil
	}
}

func routeClientHelloSpec(profile string, serverName string) utls.ClientHelloSpec {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "codex_rustls", "rustls", "codex":
		return codexRustlsClientHelloSpec(serverName)
	default:
		return node24ClientHelloSpec(serverName)
	}
}

func node24ClientHelloSpec(serverName string) utls.ClientHelloSpec {
	extensions := make([]utls.TLSExtension, 0, 12)
	if shouldSendSNI(serverName) {
		extensions = append(extensions, &utls.SNIExtension{ServerName: serverName})
	}
	extensions = append(extensions,
		&utls.ExtendedMasterSecretExtension{},
		&utls.RenegotiationInfoExtension{},
		&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}},
		&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
		&utls.SessionTicketExtension{},
		&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
		&utls.StatusRequestExtension{},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
			utls.SignatureScheme(0x0403),
			utls.SignatureScheme(0x0804),
			utls.SignatureScheme(0x0401),
			utls.SignatureScheme(0x0503),
			utls.SignatureScheme(0x0805),
			utls.SignatureScheme(0x0501),
			utls.SignatureScheme(0x0806),
			utls.SignatureScheme(0x0601),
			utls.SignatureScheme(0x0201),
		}},
		&utls.SCTExtension{},
		&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519}}},
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}},
	)

	return utls.ClientHelloSpec{
		CipherSuites: []uint16{
			0x1301,
			0x1302,
			0x1303,
			0xc02b,
			0xc02f,
			0xc02c,
			0xc030,
			0xcca9,
			0xcca8,
			0xc009,
			0xc013,
			0xc00a,
			0xc014,
			0x009c,
			0x009d,
			0x002f,
			0x0035,
			0x00ff,
		},
		CompressionMethods: []uint8{0},
		Extensions:         extensions,
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS13,
	}
}

func codexUTLSDialTLSContext(route routeInfo) func(context.Context, string, string) (net.Conn, error) {
	if routeTLSFingerprintProfile(route) == "" {
		route.ProviderType = "codex_compatible"
	}
	return routeUTLSDialTLSContext(route)
}

func tlsServerNameFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return strings.Trim(host, "[]")
}

func codexRustlsClientHelloSpec(serverName string) utls.ClientHelloSpec {
	extensions := make([]utls.TLSExtension, 0, 11)
	if shouldSendSNI(serverName) {
		extensions = append(extensions, &utls.SNIExtension{ServerName: serverName})
	}
	extensions = append(extensions,
		&utls.SupportedPointsExtension{SupportedPoints: []uint8{0, 1, 2}},
		&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
			utls.X25519,
			utls.CurveP256,
			utls.CurveID(0x001e),
			utls.CurveP521,
			utls.CurveP384,
			utls.CurveID(0x0100),
			utls.CurveID(0x0101),
			utls.CurveID(0x0102),
			utls.CurveID(0x0103),
			utls.CurveID(0x0104),
		}},
		&utls.SessionTicketExtension{},
		&utls.GenericExtension{Id: 22},
		&utls.ExtendedMasterSecretExtension{},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
			utls.SignatureScheme(0x0403),
			utls.SignatureScheme(0x0503),
			utls.SignatureScheme(0x0603),
			utls.SignatureScheme(0x0807),
			utls.SignatureScheme(0x0808),
			utls.SignatureScheme(0x0809),
			utls.SignatureScheme(0x080a),
			utls.SignatureScheme(0x080b),
			utls.SignatureScheme(0x0804),
			utls.SignatureScheme(0x0805),
			utls.SignatureScheme(0x0806),
			utls.SignatureScheme(0x0401),
			utls.SignatureScheme(0x0501),
			utls.SignatureScheme(0x0601),
			utls.SignatureScheme(0x0303),
			utls.SignatureScheme(0x0301),
			utls.SignatureScheme(0x0302),
			utls.SignatureScheme(0x0402),
			utls.SignatureScheme(0x0502),
			utls.SignatureScheme(0x0602),
		}},
		&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}},
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519}}},
		&utls.UtlsPaddingExtension{GetPaddingLen: utls.AlwaysPadToLen(512)},
	)

	return utls.ClientHelloSpec{
		CipherSuites: []uint16{
			0x1302,
			0x1303,
			0x1301,
			0xc02c,
			0xc030,
			0x009f,
			0xcca9,
			0xcca8,
			0xccaa,
			0xc02b,
			0xc02f,
			0x009e,
			0xc024,
			0xc028,
			0x006b,
			0xc023,
			0xc027,
			0x0067,
			0xc00a,
			0xc014,
			0x0039,
			0xc009,
			0xc013,
			0x0033,
			0x009d,
			0x009c,
			0x003d,
			0x003c,
			0x0035,
			0x002f,
			0x00ff,
		},
		CompressionMethods: []uint8{0},
		Extensions:         extensions,
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS13,
	}
}

func shouldSendSNI(serverName string) bool {
	return serverName != "" && net.ParseIP(serverName) == nil
}
