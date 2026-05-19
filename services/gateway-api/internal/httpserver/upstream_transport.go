package httpserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	upstreamHTTPClientTimeout = 120 * time.Second
	upstreamDialTimeout       = 10 * time.Second
	upstreamKeepAlive         = 30 * time.Second
	upstreamTLSHandshake      = 10 * time.Second
	upstreamIdleConnTimeout   = 90 * time.Second
)

type upstreamClientPool struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

type pooledHTTPClient struct {
	*http.Client
	timeout time.Duration
}

func (client pooledHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if client.timeout <= 0 {
		return client.Client.Do(req)
	}
	next := *client.Client
	next.Timeout = client.timeout
	return next.Do(req)
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newUpstreamClientPool() *upstreamClientPool {
	return &upstreamClientPool{clients: map[string]*http.Client{}}
}

func withHTTPClientTimeout(client *http.Client, timeout time.Duration) httpDoer {
	if timeout <= 0 {
		return client
	}
	return pooledHTTPClient{Client: client, timeout: timeout}
}

func (pool *upstreamClientPool) client(route routeInfo, disableCompression bool) (*http.Client, error) {
	if pool == nil {
		return routeHTTPClient(route, disableCompression)
	}
	key, err := upstreamClientPoolKey(route, disableCompression)
	if err != nil {
		return nil, err
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if client := pool.clients[key]; client != nil {
		return client, nil
	}
	client, err := routeHTTPClient(route, disableCompression)
	if err != nil {
		return nil, err
	}
	pool.clients[key] = client
	return client, nil
}

func upstreamClientPoolKey(route routeInfo, disableCompression bool) (string, error) {
	normalizedProxy, err := normalizeProxyURL(route.ProxyURL)
	if err != nil {
		return "", err
	}
	tlsProfile := "go"
	if profile := routeTLSFingerprintProfile(route); profile != "" {
		tlsProfile = profile
	}
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(route.ProviderType)),
		normalizedProxy,
		fmt.Sprintf("disable_compression=%t", disableCompression),
		tlsProfile,
	}, "|"), nil
}

func routeHTTPClient(route routeInfo, disableCompression bool) (*http.Client, error) {
	transport, err := routeHTTPTransport(route, disableCompression)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   upstreamHTTPClientTimeout,
		Transport: transport,
	}, nil
}

func routeHTTPTransport(route routeInfo, disableCompression bool) (*http.Transport, error) {
	if isCodexOfficialRoute(route) {
		return codexOfficialHTTPTransport(route, disableCompression)
	}
	if routeTLSFingerprintProfile(route) != "" {
		return upstreamTransport(route, disableCompression, routeUTLSDialTLSContext(route))
	}
	return defaultHTTPTransport(route, disableCompression)
}

func defaultHTTPTransport(route routeInfo, disableCompression bool) (*http.Transport, error) {
	return upstreamTransport(route, disableCompression, nil)
}

func upstreamTransport(route routeInfo, disableCompression bool, dialTLS func(context.Context, string, string) (net.Conn, error)) (*http.Transport, error) {
	directProxy := isDirectProxyMode(route.ProxyURL)
	proxyURL, err := parseRouteProxyURL(route)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: upstreamDialTimeout, KeepAlive: upstreamKeepAlive}).DialContext,
		TLSHandshakeTimeout:   upstreamTLSHandshake,
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       upstreamIdleConnTimeout,
		MaxIdleConns:          240,
		MaxIdleConnsPerHost:   120,
		MaxConnsPerHost:       240,
		ForceAttemptHTTP2:     dialTLS == nil,
		DisableCompression:    disableCompression,
	}
	if proxyURL == nil {
		if directProxy {
			transport.Proxy = nil
		}
		if dialTLS != nil {
			transport.DialTLSContext = dialTLS
		}
		return transport, nil
	}
	transport.Proxy = nil
	switch proxyURL.Scheme {
	case "http", "https":
		if dialTLS != nil {
			transport.DialTLSContext = httpProxyDialTLSContext(proxyURL, dialTLS)
		} else {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	case "socks5", "socks5h":
		dialContext, err := socksProxyDialContext(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.DialContext = dialContext
		if dialTLS != nil {
			transport.DialTLSContext = func(ctx context.Context, network string, addr string) (net.Conn, error) {
				conn, err := dialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return dialTLSOverConn(ctx, conn, addr, dialTLS)
			}
		}
	default:
		return nil, badRequest("Invalid upstream proxy URL.")
	}
	return transport, nil
}

func parseRouteProxyURL(route routeInfo) (*url.URL, error) {
	return parseProxyURL(route.ProxyURL)
}

func parseProxyURL(raw string) (*url.URL, error) {
	normalized, err := normalizeProxyURL(raw)
	if err != nil {
		return nil, err
	}
	if normalized == "" || isDirectProxyMode(normalized) {
		return nil, nil
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, badRequest("Invalid upstream proxy URL.")
	}
	return parsed, nil
}

func normalizeProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if isDirectProxyMode(trimmed) {
		return "direct", nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", badRequest("proxy_url must be a valid absolute URL.")
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", badRequest("proxy_url has an unsupported URL scheme.")
	}
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateProxyURL(value string) (string, error) {
	return normalizeProxyURL(value)
}

func isDirectProxyMode(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "direct", "none":
		return true
	default:
		return false
	}
}

func socksProxyDialContext(proxyURL *url.URL) (func(context.Context, string, string) (net.Conn, error), error) {
	if proxyURL == nil {
		return nil, badRequest("Invalid upstream proxy URL.")
	}
	if proxyURL.Scheme != "socks5" && proxyURL.Scheme != "socks5h" {
		return nil, badRequest("Invalid upstream proxy URL.")
	}
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("unsupported socks network: %s", network)
		}
		return dialSOCKS5(ctx, proxyURL, network, addr)
	}, nil
}

func dialSOCKS5(ctx context.Context, proxyURL *url.URL, network string, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: upstreamDialTimeout, KeepAlive: upstreamKeepAlive}
	conn, err := dialer.DialContext(ctx, network, canonicalAddr(proxyURL))
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	if err := socks5Authenticate(conn, proxyURL); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := socks5Connect(conn, addr); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func socks5Authenticate(conn net.Conn, proxyURL *url.URL) error {
	if proxyURL.User == nil {
		if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			return err
		}
		response := []byte{0, 0}
		if _, err := io.ReadFull(conn, response); err != nil {
			return err
		}
		if response[0] != 0x05 || response[1] != 0x00 {
			return fmt.Errorf("socks5 auth failed")
		}
		return nil
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		return err
	}
	response := []byte{0, 0}
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] != 0x02 {
		return fmt.Errorf("socks5 username/password auth is not accepted")
	}
	username := proxyURL.User.Username()
	password, _ := proxyURL.User.Password()
	if len(username) > 255 || len(password) > 255 {
		return fmt.Errorf("socks5 credentials are too long")
	}
	authRequest := []byte{0x01, byte(len(username))}
	authRequest = append(authRequest, username...)
	authRequest = append(authRequest, byte(len(password)))
	authRequest = append(authRequest, password...)
	if _, err := conn.Write(authRequest); err != nil {
		return err
	}
	authResponse := []byte{0, 0}
	if _, err := io.ReadFull(conn, authResponse); err != nil {
		return err
	}
	if authResponse[0] != 0x01 || authResponse[1] != 0x00 {
		return fmt.Errorf("socks5 username/password auth failed")
	}
	return nil
}

func socks5Connect(conn net.Conn, addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid socks5 target port")
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("socks5 target host is too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return err
	}
	header := []byte{0, 0, 0, 0}
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return fmt.Errorf("invalid socks5 response")
	}
	if header[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed: %s", socks5ReplyText(header[1]))
	}
	switch header[3] {
	case 0x01:
		_, err = io.CopyN(io.Discard, conn, 4+2)
	case 0x03:
		length := []byte{0}
		if _, err = io.ReadFull(conn, length); err != nil {
			return err
		}
		_, err = io.CopyN(io.Discard, conn, int64(length[0])+2)
	case 0x04:
		_, err = io.CopyN(io.Discard, conn, 16+2)
	default:
		return fmt.Errorf("invalid socks5 address type")
	}
	return err
}

func socks5ReplyText(code byte) string {
	switch code {
	case 0x01:
		return "general failure"
	case 0x02:
		return "connection not allowed"
	case 0x03:
		return "network unreachable"
	case 0x04:
		return "host unreachable"
	case 0x05:
		return "connection refused"
	case 0x06:
		return "ttl expired"
	case 0x07:
		return "command not supported"
	case 0x08:
		return "address type not supported"
	default:
		return fmt.Sprintf("reply 0x%02x", code)
	}
}

func httpProxyDialTLSContext(proxyURL *url.URL, dialTLS func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		conn, err := dialHTTPProxyCONNECT(ctx, proxyURL, network, addr)
		if err != nil {
			return nil, err
		}
		return dialTLSOverConn(ctx, conn, addr, dialTLS)
	}
}

func dialHTTPProxyCONNECT(ctx context.Context, proxyURL *url.URL, network string, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: upstreamDialTimeout, KeepAlive: upstreamKeepAlive}
	proxyAddr := canonicalAddr(proxyURL)
	conn, err := dialer.DialContext(ctx, network, proxyAddr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	if proxyURL.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	connectHost := addr
	if !strings.Contains(connectHost, ":") {
		connectHost = net.JoinHostPort(connectHost, "443")
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: connectHost},
		Host:   connectHost,
		Header: make(http.Header),
	}
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	return conn, nil
}

func dialTLSOverConn(ctx context.Context, conn net.Conn, addr string, dialTLS func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if dialTLS == nil {
		return conn, nil
	}
	ctx = context.WithValue(ctx, tlsFingerprintConnContextKey{}, conn)
	tlsConn, err := dialTLS(ctx, "tcp", addr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func canonicalAddr(parsed *url.URL) string {
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}
