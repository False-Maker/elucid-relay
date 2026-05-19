package httpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

func (s *Server) listProxyRows(r *http.Request) ([]map[string]any, error) {
	query := r.URL.Query()
	search := "%" + strings.ToLower(strings.TrimSpace(query.Get("q"))) + "%"
	status := strings.TrimSpace(query.Get("status"))
	protocol := strings.TrimSpace(query.Get("protocol"))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT p.id::text, p.name, p.proxy_url, p.status, p.metadata_json::text, p.created_at, p.updated_at,
		       COUNT(DISTINCT a.id) AS account_count,
		       COUNT(DISTINCT c.id) AS channel_count,
		       ptr.status AS last_test_status,
		       ptr.latency_ms AS last_test_latency_ms,
		       ptr.error_message AS last_test_error,
		       ptr.metadata_json::text AS last_test_metadata,
		       ptr.tested_at AS last_tested_at,
		       qtr.metadata_json::text AS quality_metadata,
		       qtr.tested_at AS quality_tested_at
		FROM proxies p
		LEFT JOIN accounts a ON a.proxy_id = p.id
		LEFT JOIN channels c ON c.proxy_id = p.id
		LEFT JOIN LATERAL (
		  SELECT status, latency_ms, error_message, metadata_json, tested_at
		  FROM proxy_test_results
		  WHERE proxy_id = p.id
		  ORDER BY tested_at DESC
		  LIMIT 1
		) ptr ON true
		LEFT JOIN LATERAL (
		  SELECT metadata_json, tested_at
		  FROM proxy_test_results
		  WHERE proxy_id = p.id AND test_type = 'quality'
		  ORDER BY tested_at DESC
		  LIMIT 1
		) qtr ON true
		WHERE ($2 = '' OR p.status = $2)
		  AND ($3 = '' OR ($3 = 'direct' AND lower(p.proxy_url) IN ('direct', 'none')) OR p.proxy_url LIKE $3 || ':%')
		  AND ($4 = '%%' OR lower(p.name) LIKE $4 OR lower(p.proxy_url) LIKE $4)
		GROUP BY p.id, ptr.status, ptr.latency_ms, ptr.error_message, ptr.metadata_json, ptr.tested_at, qtr.metadata_json, qtr.tested_at
		ORDER BY p.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500), status, protocol, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, name, proxyURL, proxyStatus, metadata string
		var createdAt, updatedAt time.Time
		var accountCount, channelCount int
		var lastStatus, lastError sql.NullString
		var lastLatency sql.NullInt64
		var lastMetadata sql.NullString
		var lastTestedAt, qualityTestedAt sql.NullTime
		var qualityMetadata sql.NullString
		if err := rows.Scan(&id, &name, &proxyURL, &proxyStatus, &metadata, &createdAt, &updatedAt, &accountCount, &channelCount, &lastStatus, &lastLatency, &lastError, &lastMetadata, &lastTestedAt, &qualityMetadata, &qualityTestedAt); err != nil {
			return nil, err
		}
		lastMeta := decodeProxyMetadata(lastMetadata)
		qualityMeta := decodeProxyMetadata(qualityMetadata)
		exitInfo := proxyExitInfoFromMetadata(qualityMeta)
		if exitInfo.IP == "" {
			exitInfo = proxyExitInfoFromMetadata(lastMeta)
		}
		quality := proxyQualityFromMetadata(qualityMeta)
		items = append(items, map[string]any{
			"id":                   id,
			"name":                 name,
			"type_or_url":          proxyURL,
			"proxy_url":            proxyURL,
			"proxy_protocol":       proxyProtocolFromURL(proxyURL),
			"status":               proxyStatus,
			"metadata":             jsonRaw(metadata),
			"account_count":        accountCount,
			"channel_count":        channelCount,
			"last_test_status":     nullableSQLString(lastStatus),
			"last_test_latency_ms": nullableSQLInt(lastLatency),
			"last_test_error":      nullableSQLString(lastError),
			"last_upstream_status": nullableIntFromMetadata(lastMeta, "upstream_status"),
			"last_tested_at":       nullableSQLTime(lastTestedAt),
			"exit_ip":              exitInfo.IP,
			"country":              exitInfo.Country,
			"country_code":         exitInfo.CountryCode,
			"region":               exitInfo.Region,
			"city":                 exitInfo.City,
			"quality_status":       quality.Status,
			"quality_score":        nullableIntValue(quality.Score),
			"quality_grade":        quality.Grade,
			"quality_summary":      quality.Summary,
			"quality_problem":      quality.Problem,
			"quality_checked_at":   nullableSQLTime(qualityTestedAt),
			"quality_report":       quality.Report,
			"created_at":           createdAt.UTC().Format(time.RFC3339),
			"updated_at":           updatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) adminProxyTestResults(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{limitFromRequest(r, 100, 500)}
	where := ""
	if proxyID := strings.TrimSpace(r.URL.Query().Get("proxy_id")); proxyID != "" {
		args = append(args, proxyID)
		where = "WHERE proxy_id::text = $2"
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, proxy_id::text, test_type, target_url, status, latency_ms, error_message, metadata_json::text, tested_at
		FROM proxy_test_results
		`+where+`
		ORDER BY tested_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, proxyID, testType, targetURL, status, errorMessage, metadata string
		var latencyMS int
		var testedAt time.Time
		if err := rows.Scan(&id, &proxyID, &testType, &targetURL, &status, &latencyMS, &errorMessage, &metadata, &testedAt); err != nil {
			return err
		}
		meta := decodeProxyMetadata(sql.NullString{String: metadata, Valid: true})
		exitInfo := proxyExitInfoFromMetadata(meta)
		quality := proxyQualityFromMetadata(meta)
		items = append(items, map[string]any{
			"id":              id,
			"proxy_id":        proxyID,
			"test_type":       testType,
			"target_url":      targetURL,
			"status":          status,
			"latency_ms":      latencyMS,
			"upstream_status": nullableIntFromMetadata(meta, "upstream_status"),
			"error_message":   errorMessage,
			"exit_ip":         exitInfo.IP,
			"country":         exitInfo.Country,
			"city":            exitInfo.City,
			"quality_score":   nullableIntValue(quality.Score),
			"quality_grade":   quality.Grade,
			"quality_status":  quality.Status,
			"quality_problem": quality.Problem,
			"metadata":        jsonRaw(metadata),
			"tested_at":       testedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminTestProxy(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		TargetURL string `json:"target_url"`
		TestType  string `json:"test_type"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	result, err := s.runProxyCheck(r, r.PathValue("proxyId"), req.TargetURL, defaultString(req.TestType, "connectivity"))
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "proxy.test", "proxy", r.PathValue("proxyId"), r, result)
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) adminProxyQuality(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ProxyIDs  []string `json:"proxy_ids"`
		TargetURL string   `json:"target_url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ids := normalizeStringList(req.ProxyIDs)
	if len(ids) == 0 {
		rows, err := s.db.QueryContext(r.Context(), "SELECT id::text FROM proxies WHERE status = 'active' ORDER BY created_at DESC LIMIT 100")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	results := []map[string]any{}
	for _, id := range ids {
		result, err := s.runProxyCheck(r, id, req.TargetURL, "quality")
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "proxy.quality", "proxy", "", r, map[string]any{"count": len(results)})
	writeJSON(w, http.StatusOK, results, nil)
	return nil
}

func (s *Server) adminProxyBatch(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ProxyIDs []string `json:"proxy_ids"`
		Action   string   `json:"action"`
		Status   string   `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ids := normalizeStringList(req.ProxyIDs)
	if len(ids) == 0 {
		return badRequest("proxy_ids is required.")
	}
	nextStatus := strings.TrimSpace(req.Status)
	switch strings.TrimSpace(req.Action) {
	case "enable":
		nextStatus = "active"
	case "disable":
		nextStatus = "disabled"
	}
	status, err := defaultedStatus(nextStatus, "", "active", "disabled")
	if err != nil || status == "" {
		return badRequest("action must be enable or disable.")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated := 0
	for _, id := range ids {
		result, err := tx.ExecContext(r.Context(), "UPDATE proxies SET status = $2 WHERE id = $1", id, status)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows > 0 {
			updated++
		}
	}
	audit(r.Context(), tx, auth.UserID, "admin", "proxy.batch_update", "proxy", "", r, map[string]any{"count": updated, "status": status})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "status": status}, nil)
	return nil
}

func (s *Server) adminExportProxies(w http.ResponseWriter, r *http.Request, auth authContext) error {
	items, err := s.listProxyRows(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "exported_at": time.Now().UTC().Format(time.RFC3339)}, nil)
	return nil
}

func (s *Server) adminImportProxies(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Items []struct {
			Name     string         `json:"name"`
			ProxyURL string         `json:"proxy_url"`
			Status   string         `json:"status"`
			Metadata map[string]any `json:"metadata"`
		} `json:"items"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.Items) == 0 {
		return badRequest("items is required.")
	}
	if len(req.Items) > 200 {
		return badRequest("items cannot exceed 200.")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	imported := 0
	for _, item := range req.Items {
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.ProxyURL) == "" {
			return badRequest("name and proxy_url are required.")
		}
		status, err := defaultedStatus(item.Status, "active", "active", "disabled")
		if err != nil {
			return err
		}
		proxyURL, err := validateProxyURL(item.ProxyURL)
		if err != nil {
			return err
		}
		metadata, err := encodeJSON(item.Metadata)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO proxies (name, proxy_url, status, metadata_json)
			VALUES ($1, $2, $3, $4::jsonb)
		`, strings.TrimSpace(item.Name), proxyURL, status, metadata); err != nil {
			return err
		}
		imported++
	}
	audit(r.Context(), tx, auth.UserID, "admin", "proxy.import", "proxy", "", r, map[string]any{"count": imported})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"imported": imported}, nil)
	return nil
}

type proxyExitInfo struct {
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Region      string `json:"region,omitempty"`
	City        string `json:"city,omitempty"`
}

type proxyHTTPCheckResult struct {
	Status       string
	HTTPStatus   int
	LatencyMS    int
	ErrorMessage string
	ErrorKind    string
}

type proxyExitProbeResult struct {
	Info         proxyExitInfo
	LatencyMS    int
	ProbeURL     string
	ErrorMessage string
}

type proxyQualityTarget struct {
	Target          string
	URL             string
	Method          string
	AllowedStatuses map[int]bool
}

type proxyQualityItem struct {
	Target     string `json:"target"`
	Status     string `json:"status"`
	ErrorKind  string `json:"error_kind,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	LatencyMS  int    `json:"latency_ms,omitempty"`
	Message    string `json:"message,omitempty"`
	CFRay      string `json:"cf_ray,omitempty"`
}

type proxyQualityReport struct {
	Score          int                `json:"score"`
	Grade          string             `json:"grade"`
	Summary        string             `json:"summary"`
	QualityStatus  string             `json:"quality_status"`
	ExitIP         string             `json:"exit_ip,omitempty"`
	Country        string             `json:"country,omitempty"`
	CountryCode    string             `json:"country_code,omitempty"`
	Region         string             `json:"region,omitempty"`
	City           string             `json:"city,omitempty"`
	BaseLatencyMS  int                `json:"base_latency_ms,omitempty"`
	PassedCount    int                `json:"passed_count"`
	WarnCount      int                `json:"warn_count"`
	FailedCount    int                `json:"failed_count"`
	ChallengeCount int                `json:"challenge_count"`
	Items          []proxyQualityItem `json:"items"`
}

var proxyExitProbeTargets = []struct {
	URL    string
	Parser string
}{
	{URL: "http://ip-api.com/json/?lang=zh-CN", Parser: "ip-api"},
	{URL: "http://httpbin.org/ip", Parser: "httpbin"},
}

var proxyQualityBaseTargets = []proxyQualityTarget{
	{
		Target: "openai",
		URL:    "https://api.openai.com/v1/models",
		Method: http.MethodGet,
		AllowedStatuses: map[int]bool{
			http.StatusUnauthorized: true,
		},
	},
	{
		Target: "anthropic",
		URL:    "https://api.anthropic.com/v1/messages",
		Method: http.MethodGet,
		AllowedStatuses: map[int]bool{
			http.StatusUnauthorized:     true,
			http.StatusMethodNotAllowed: true,
			http.StatusNotFound:         true,
			http.StatusBadRequest:       true,
		},
	},
	{
		Target: "gemini",
		URL:    "https://generativelanguage.googleapis.com/$discovery/rest?version=v1beta",
		Method: http.MethodGet,
		AllowedStatuses: map[int]bool{
			http.StatusOK: true,
		},
	},
}

func (s *Server) runProxyCheck(r *http.Request, proxyID string, targetURL string, testType string) (map[string]any, error) {
	if testType != "connectivity" && testType != "quality" {
		testType = "connectivity"
	}
	customTargetURL := ""
	var err error
	if testType == "connectivity" {
		if strings.TrimSpace(targetURL) == "" {
			targetURL = "https://example.com"
		}
		targetURL, err = validateURL(targetURL, "target_url", "http", "https")
		if err != nil {
			return nil, err
		}
	} else if strings.TrimSpace(targetURL) != "" && strings.TrimSpace(targetURL) != "https://example.com" {
		customTargetURL, err = validateURL(targetURL, "target_url", "http", "https")
		if err != nil {
			return nil, err
		}
		targetURL = customTargetURL
	} else {
		targetURL = "multi-target"
	}
	var name, proxyURL string
	if err := s.db.QueryRowContext(r.Context(), "SELECT name, proxy_url FROM proxies WHERE id = $1", proxyID).Scan(&name, &proxyURL); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFound("Proxy was not found.")
		}
		return nil, err
	}
	if s.upstreamPool == nil {
		s.upstreamPool = newUpstreamClientPool()
	}
	baseClient, err := s.upstreamPool.client(routeInfo{ProxyURL: proxyURL}, true)
	if err != nil {
		return nil, err
	}
	client := withHTTPClientTimeout(baseClient, 15*time.Second)

	metadata := map[string]any{"proxy_name": name}
	status := "success"
	latencyMS := 0
	errorMessage := ""
	upstreamStatus := 0
	var qualityReport *proxyQualityReport

	if testType == "quality" {
		qualityReport = runProxyQualityReport(r.Context(), client, customTargetURL)
		if proxyQualityBaseConnectivityPass(qualityReport) {
			status = "success"
		} else {
			status = "failed"
			errorMessage = truncateForStorage(proxyQualityProblemMessage(qualityReport), 1000)
		}
		latencyMS = qualityReport.BaseLatencyMS
		metadata["error_kind"] = proxyQualityPrimaryErrorKind(qualityReport)
		metadata["quality_status"] = qualityReport.QualityStatus
		metadata["quality_score"] = qualityReport.Score
		metadata["quality_grade"] = qualityReport.Grade
		metadata["quality_summary"] = qualityReport.Summary
		metadata["quality_problem"] = proxyQualityIssueSummary(qualityReport)
		metadata["quality_items"] = qualityReport.Items
		metadata["quality_report"] = qualityReport
		if qualityReport.ExitIP != "" {
			metadata["exit_ip"] = qualityReport.ExitIP
			metadata["country"] = qualityReport.Country
			metadata["country_code"] = qualityReport.CountryCode
			metadata["region"] = qualityReport.Region
			metadata["city"] = qualityReport.City
		}
	} else {
		check := runProxyHTTPCheck(r.Context(), client, targetURL)
		status = check.Status
		latencyMS = check.LatencyMS
		errorMessage = truncateForStorage(check.ErrorMessage, 1000)
		upstreamStatus = check.HTTPStatus
		metadata["upstream_status"] = upstreamStatus
		metadata["error_kind"] = check.ErrorKind
		probe := runProxyExitProbe(r.Context(), client)
		if probe.Info.IP != "" {
			metadata["exit_ip"] = probe.Info.IP
			metadata["country"] = probe.Info.Country
			metadata["country_code"] = probe.Info.CountryCode
			metadata["region"] = probe.Info.Region
			metadata["city"] = probe.Info.City
			metadata["exit_probe_latency_ms"] = probe.LatencyMS
			metadata["exit_probe_url"] = probe.ProbeURL
		} else if probe.ErrorMessage != "" {
			metadata["exit_probe_error"] = truncateForStorage(probe.ErrorMessage, 500)
		}
	}
	metadataJSON, err := encodeJSON(metadata)
	if err != nil {
		return nil, err
	}
	var id string
	var testedAt time.Time
	if err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO proxy_test_results (proxy_id, test_type, target_url, status, latency_ms, error_message, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		RETURNING id::text, tested_at
	`, proxyID, testType, targetURL, status, latencyMS, errorMessage, metadataJSON).Scan(&id, &testedAt); err != nil {
		return nil, err
	}
	result := map[string]any{
		"id":              id,
		"proxy_id":        proxyID,
		"proxy_name":      name,
		"test_type":       testType,
		"target_url":      targetURL,
		"status":          status,
		"latency_ms":      latencyMS,
		"upstream_status": nullableInt(upstreamStatus),
		"error_message":   errorMessage,
		"tested_at":       testedAt.UTC().Format(time.RFC3339),
	}
	for key, value := range metadata {
		if key == "proxy_name" || key == "upstream_status" {
			continue
		}
		result[key] = value
	}
	if qualityReport != nil {
		result["score"] = qualityReport.Score
		result["grade"] = qualityReport.Grade
		result["summary"] = qualityReport.Summary
		result["problem"] = proxyQualityIssueSummary(qualityReport)
		result["items"] = qualityReport.Items
	}
	return result, nil
}

func runProxyHTTPCheck(ctx context.Context, client httpDoer, targetURL string) proxyHTTPCheckResult {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return proxyHTTPCheckResult{Status: "failed", ErrorMessage: "target_url must be a valid absolute URL.", ErrorKind: "invalid_target"}
	}
	resp, err := client.Do(req)
	latencyMS := int(time.Since(started).Milliseconds())
	if err != nil {
		return proxyHTTPCheckResult{Status: "failed", LatencyMS: latencyMS, ErrorMessage: err.Error(), ErrorKind: classifyNetworkError(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 500 {
		return proxyHTTPCheckResult{Status: "failed", HTTPStatus: resp.StatusCode, LatencyMS: latencyMS, ErrorMessage: resp.Status}
	}
	return proxyHTTPCheckResult{Status: "success", HTTPStatus: resp.StatusCode, LatencyMS: latencyMS}
}

func runProxyExitProbe(ctx context.Context, client httpDoer) proxyExitProbeResult {
	var lastErr string
	for _, target := range proxyExitProbeTargets {
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		resp, err := client.Do(req)
		latencyMS := int(time.Since(started).Milliseconds())
		if err != nil {
			lastErr = err.Error()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr.Error()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = resp.Status
			continue
		}
		info, err := parseProxyExitInfo(target.Parser, body)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		return proxyExitProbeResult{Info: info, LatencyMS: latencyMS, ProbeURL: target.URL}
	}
	return proxyExitProbeResult{ErrorMessage: lastErr}
}

func parseProxyExitInfo(parser string, body []byte) (proxyExitInfo, error) {
	switch parser {
	case "ip-api":
		var payload struct {
			Status      string `json:"status"`
			Message     string `json:"message"`
			Query       string `json:"query"`
			City        string `json:"city"`
			Region      string `json:"region"`
			RegionName  string `json:"regionName"`
			Country     string `json:"country"`
			CountryCode string `json:"countryCode"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return proxyExitInfo{}, err
		}
		if strings.ToLower(payload.Status) != "success" {
			return proxyExitInfo{}, fmt.Errorf("ip-api request failed: %s", defaultString(payload.Message, "unknown"))
		}
		region := payload.RegionName
		if region == "" {
			region = payload.Region
		}
		return proxyExitInfo{IP: payload.Query, Country: payload.Country, CountryCode: payload.CountryCode, Region: region, City: payload.City}, nil
	case "httpbin":
		var payload struct {
			Origin string `json:"origin"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return proxyExitInfo{}, err
		}
		if strings.TrimSpace(payload.Origin) == "" {
			return proxyExitInfo{}, fmt.Errorf("httpbin did not return origin")
		}
		return proxyExitInfo{IP: strings.TrimSpace(strings.Split(payload.Origin, ",")[0])}, nil
	default:
		return proxyExitInfo{}, fmt.Errorf("unsupported exit probe parser")
	}
}

func runProxyQualityReport(ctx context.Context, client httpDoer, customTargetURL string) *proxyQualityReport {
	report := &proxyQualityReport{Score: 100, Grade: "A", QualityStatus: "healthy", Items: []proxyQualityItem{}}
	probe := runProxyExitProbe(ctx, client)
	if probe.Info.IP == "" {
		fallbackURL := defaultString(customTargetURL, "https://example.com")
		check := runProxyHTTPCheck(ctx, client, fallbackURL)
		report.BaseLatencyMS = check.LatencyMS
		if check.Status != "success" {
			report.Items = append(report.Items, proxyQualityItem{Target: "base_connectivity", Status: "fail", ErrorKind: check.ErrorKind, HTTPStatus: check.HTTPStatus, LatencyMS: check.LatencyMS, Message: defaultString(check.ErrorMessage, defaultString(probe.ErrorMessage, "基础连通失败"))})
			report.FailedCount++
			finalizeProxyQualityReport(report)
			return report
		}
		report.Items = append(report.Items, proxyQualityItem{Target: "base_connectivity", Status: "warn", ErrorKind: classifyNetworkErrorText(probe.ErrorMessage), HTTPStatus: check.HTTPStatus, LatencyMS: check.LatencyMS, Message: "代理连通正常，出口 IP 探测失败：" + defaultString(probe.ErrorMessage, "未知错误")})
		report.WarnCount++
	} else {
		report.ExitIP = probe.Info.IP
		report.Country = probe.Info.Country
		report.CountryCode = probe.Info.CountryCode
		report.Region = probe.Info.Region
		report.City = probe.Info.City
		report.BaseLatencyMS = probe.LatencyMS
		report.Items = append(report.Items, proxyQualityItem{Target: "base_connectivity", Status: "pass", LatencyMS: probe.LatencyMS, Message: "代理出口连通正常"})
		report.PassedCount++
	}
	for _, target := range proxyQualityTargets(customTargetURL) {
		item := runProxyQualityTarget(ctx, client, target)
		report.Items = append(report.Items, item)
		switch item.Status {
		case "pass":
			report.PassedCount++
		case "warn":
			report.WarnCount++
		case "challenge":
			report.ChallengeCount++
		default:
			report.FailedCount++
		}
	}
	finalizeProxyQualityReport(report)
	return report
}

func proxyQualityTargets(customTargetURL string) []proxyQualityTarget {
	targets := append([]proxyQualityTarget{}, proxyQualityBaseTargets...)
	if strings.TrimSpace(customTargetURL) != "" {
		targets = append(targets, proxyQualityTarget{Target: "custom", URL: customTargetURL, Method: http.MethodGet})
	}
	return targets
}

func runProxyQualityTarget(ctx context.Context, client httpDoer, target proxyQualityTarget) proxyQualityItem {
	item := proxyQualityItem{Target: target.Target}
	req, err := http.NewRequestWithContext(ctx, target.Method, target.URL, nil)
	if err != nil {
		item.Status = "fail"
		item.ErrorKind = classifyNetworkError(err)
		item.Message = err.Error()
		return item
	}
	req.Header.Set("Accept", "application/json,text/html,*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	started := time.Now()
	resp, err := client.Do(req)
	item.LatencyMS = int(time.Since(started).Milliseconds())
	if err != nil {
		item.Status = "fail"
		item.Message = err.Error()
		return item
	}
	defer resp.Body.Close()
	item.HTTPStatus = resp.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if readErr != nil {
		item.Status = "fail"
		item.Message = readErr.Error()
		return item
	}
	if proxyLooksLikeCloudflareChallenge(resp, body) {
		item.Status = "challenge"
		item.CFRay = firstHeader(resp.Header, "Cf-Ray")
		item.Message = "命中 Cloudflare challenge"
		return item
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		item.Status = "pass"
		item.Message = "目标可达"
		return item
	}
	if target.AllowedStatuses[resp.StatusCode] {
		item.Status = "warn"
		item.Message = fmt.Sprintf("目标可达，返回 HTTP %d", resp.StatusCode)
		return item
	}
	item.Status = "fail"
	item.Message = fmt.Sprintf("非预期状态码: %d", resp.StatusCode)
	return item
}

func proxyLooksLikeCloudflareChallenge(resp *http.Response, body []byte) bool {
	if firstHeader(resp.Header, "Cf-Ray") != "" && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests) {
		return true
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, "cloudflare") {
		return false
	}
	return strings.Contains(text, "challenge") || strings.Contains(text, "cf-chl") || strings.Contains(text, "checking your browser")
}

func firstHeader(header http.Header, key string) string {
	values := header.Values(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func finalizeProxyQualityReport(report *proxyQualityReport) {
	score := 100 - report.WarnCount*10 - report.FailedCount*22 - report.ChallengeCount*30
	if score < 0 {
		score = 0
	}
	report.Score = score
	switch {
	case score >= 90:
		report.Grade = "A"
	case score >= 75:
		report.Grade = "B"
	case score >= 60:
		report.Grade = "C"
	case score >= 40:
		report.Grade = "D"
	default:
		report.Grade = "F"
	}
	switch {
	case report.ChallengeCount > 0:
		report.QualityStatus = "challenge"
	case report.FailedCount > 0:
		report.QualityStatus = "failed"
	case report.WarnCount > 0:
		report.QualityStatus = "warn"
	default:
		report.QualityStatus = "healthy"
	}
	report.Summary = fmt.Sprintf("通过 %d 项，告警 %d 项，失败 %d 项，挑战 %d 项", report.PassedCount, report.WarnCount, report.FailedCount, report.ChallengeCount)
}

func proxyQualityProblemMessage(report *proxyQualityReport) string {
	for _, item := range report.Items {
		if item.Target == "base_connectivity" && item.Status == "fail" {
			return proxyQualityItemIssue(item)
		}
	}
	for _, item := range report.Items {
		if item.Status == "fail" || item.Status == "challenge" {
			return proxyQualityItemIssue(item)
		}
	}
	return report.Summary
}

func proxyQualityIssueSummary(report *proxyQualityReport) string {
	if report == nil {
		return ""
	}
	issues := []string{}
	for _, item := range report.Items {
		if item.Status == "fail" || item.Status == "challenge" {
			issues = append(issues, proxyQualityItemIssue(item))
		}
	}
	if len(issues) == 0 {
		for _, item := range report.Items {
			if item.Status == "warn" {
				issues = append(issues, proxyQualityItemIssue(item))
			}
		}
	}
	if len(issues) == 0 {
		return ""
	}
	if len(issues) > 3 {
		issues = append(issues[:3], fmt.Sprintf("还有 %d 项异常", len(issues)-3))
	}
	return strings.Join(issues, "；")
}

func proxyQualityPrimaryErrorKind(report *proxyQualityReport) string {
	if report == nil {
		return ""
	}
	for _, item := range report.Items {
		if item.ErrorKind != "" && (item.Status == "fail" || item.Status == "challenge") {
			return item.ErrorKind
		}
	}
	for _, item := range report.Items {
		if item.ErrorKind != "" && item.Status == "warn" {
			return item.ErrorKind
		}
	}
	return ""
}

func proxyQualityItemIssue(item proxyQualityItem) string {
	target := proxyQualityTargetLabel(item.Target)
	status := proxyQualityStatusLabel(item.Status)
	message := strings.TrimSpace(item.Message)
	if message == "" {
		if item.HTTPStatus > 0 {
			message = fmt.Sprintf("HTTP %d", item.HTTPStatus)
		} else {
			message = "未返回具体原因"
		}
	}
	if item.HTTPStatus > 0 && !strings.Contains(message, strconv.Itoa(item.HTTPStatus)) {
		message = fmt.Sprintf("%s，HTTP %d", message, item.HTTPStatus)
	}
	return fmt.Sprintf("%s %s：%s", target, status, message)
}

func proxyQualityTargetLabel(target string) string {
	labels := map[string]string{
		"base_connectivity": "基础连通",
		"openai":            "OpenAI",
		"anthropic":         "Anthropic",
		"gemini":            "Gemini",
		"custom":            "自定义目标",
	}
	if label, ok := labels[target]; ok {
		return label
	}
	if target == "" {
		return "未知目标"
	}
	return target
}

func proxyQualityStatusLabel(status string) string {
	labels := map[string]string{
		"fail":      "失败",
		"challenge": "挑战",
		"warn":      "告警",
		"pass":      "通过",
	}
	if label, ok := labels[status]; ok {
		return label
	}
	if status == "" {
		return "异常"
	}
	return status
}

func proxyQualityBaseConnectivityPass(report *proxyQualityReport) bool {
	if report == nil {
		return false
	}
	for _, item := range report.Items {
		if item.Target == "base_connectivity" {
			return item.Status == "pass" || item.Status == "warn"
		}
	}
	return false
}

type proxyQualityMetadata struct {
	Status  string
	Score   *int
	Grade   string
	Summary string
	Problem string
	Report  any
}

func decodeProxyMetadata(value sql.NullString) map[string]any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return map[string]any{}
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(value.String), &metadata); err != nil {
		return map[string]any{}
	}
	return metadata
}

func proxyExitInfoFromMetadata(metadata map[string]any) proxyExitInfo {
	return proxyExitInfo{
		IP:          proxyMetadataString(metadata, "exit_ip"),
		Country:     proxyMetadataString(metadata, "country"),
		CountryCode: proxyMetadataString(metadata, "country_code"),
		Region:      proxyMetadataString(metadata, "region"),
		City:        proxyMetadataString(metadata, "city"),
	}
}

func proxyQualityFromMetadata(metadata map[string]any) proxyQualityMetadata {
	score, hasScore := proxyMetadataInt(metadata, "quality_score")
	var scorePtr *int
	if hasScore {
		scorePtr = &score
	}
	report := metadata["quality_report"]
	if report == nil && hasScore {
		report = map[string]any{
			"quality_status": proxyMetadataString(metadata, "quality_status"),
			"score":          score,
			"grade":          proxyMetadataString(metadata, "quality_grade"),
			"summary":        proxyMetadataString(metadata, "quality_summary"),
			"problem":        proxyMetadataString(metadata, "quality_problem"),
			"items":          metadata["quality_items"],
		}
	}
	return proxyQualityMetadata{
		Status:  proxyMetadataString(metadata, "quality_status"),
		Score:   scorePtr,
		Grade:   proxyMetadataString(metadata, "quality_grade"),
		Summary: proxyMetadataString(metadata, "quality_summary"),
		Problem: proxyMetadataString(metadata, "quality_problem"),
		Report:  report,
	}
}

func nullableIntFromMetadata(metadata map[string]any, key string) any {
	value, ok := proxyMetadataInt(metadata, key)
	if !ok || value == 0 {
		return nil
	}
	return value
}

func nullableIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func proxyMetadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func proxyMetadataInt(metadata map[string]any, key string) (int, bool) {
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (s *Server) adminChannelTests(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{limitFromRequest(r, 100, 500)}
	filters := []string{}
	if value := strings.TrimSpace(r.URL.Query().Get("channel_id")); value != "" {
		args = append(args, value)
		filters = append(filters, fmt.Sprintf("channel_id::text = $%d", len(args)))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("account_id")); value != "" {
		args = append(args, value)
		filters = append(filters, fmt.Sprintf("account_id::text = $%d", len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, COALESCE(provider_id::text, ''), COALESCE(channel_id::text, ''), COALESCE(account_id::text, ''),
		       test_type, model_name, endpoint, status, latency_ms, upstream_status, error_message, metadata_json::text, tested_at
		FROM channel_test_results
		`+where+`
		ORDER BY tested_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, providerID, channelID, accountID, testType, modelName, endpoint, status, errorMessage, metadata string
		var latencyMS int
		var upstreamStatus sql.NullInt64
		var testedAt time.Time
		if err := rows.Scan(&id, &providerID, &channelID, &accountID, &testType, &modelName, &endpoint, &status, &latencyMS, &upstreamStatus, &errorMessage, &metadata, &testedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":              id,
			"provider_id":     providerID,
			"channel_id":      channelID,
			"account_id":      accountID,
			"test_type":       testType,
			"model_name":      modelName,
			"endpoint":        endpoint,
			"status":          status,
			"latency_ms":      latencyMS,
			"upstream_status": nullableSQLInt(upstreamStatus),
			"error_message":   errorMessage,
			"metadata":        jsonRaw(metadata),
			"tested_at":       testedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateChannelTest(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ChannelID  string `json:"channel_id"`
		AccountID  string `json:"account_id"`
		ModelName  string `json:"model_name"`
		Endpoint   string `json:"endpoint"`
		TargetPath string `json:"target_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.AccountID) == "" {
		return badRequest("account_id is required for channel tests.")
	}
	result, err := s.runChannelCheck(r, req.ChannelID, req.AccountID, req.ModelName, req.Endpoint, req.TargetPath)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "channel.test", "channel", req.ChannelID, r, result)
	writeJSON(w, http.StatusCreated, result, nil)
	return nil
}

func (s *Server) runChannelCheck(r *http.Request, channelID string, accountID string, modelName string, endpoint string, targetPath string) (map[string]any, error) {
	return s.runChannelCheckContext(r.Context(), channelID, accountID, modelName, endpoint, targetPath)
}

func (s *Server) runChannelCheckContext(ctx context.Context, channelID string, accountID string, modelName string, endpoint string, targetPath string) (map[string]any, error) {
	if strings.TrimSpace(channelID) == "" && strings.TrimSpace(accountID) == "" {
		return nil, badRequest("channel_id or account_id is required.")
	}
	var route routeInfo
	var providerID, channelName string
	var ciphertext, nonce []byte
	query := `
		SELECT p.id::text, c.id::text, COALESCE(a.id::text, ''), c.base_url, p.provider_type,
		       c.timeout_seconds, COALESCE(ap.proxy_url, cp.proxy_url, ''),
		       COALESCE(aas.auth_mode, ''), COALESCE(aas.auth_status, ''), c.metadata_json::text, COALESCE(a.metadata_json::text, '{}'),
		       COALESCE(v.secret_ciphertext, '\x'::bytea), COALESCE(v.secret_nonce, '\x'::bytea), c.name
		FROM channels c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN accounts a ON a.channel_id = c.id AND ($2 = '' OR a.id::text = $2)
		LEFT JOIN credential_vault_records v ON v.id = a.credential_vault_record_id
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		LEFT JOIN proxies cp ON cp.id = c.proxy_id
		LEFT JOIN proxies ap ON ap.id = a.proxy_id
		WHERE ($1 = '' OR c.id::text = $1)
		  AND ($2 = '' OR a.id::text = $2)
		ORDER BY a.created_at NULLS LAST
		LIMIT 1
	`
	if err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(channelID), strings.TrimSpace(accountID)).Scan(
		&providerID, &route.ChannelID, &route.AccountID, &route.BaseURL, &route.ProviderType,
		&route.TimeoutSeconds, &route.ProxyURL, &route.AuthMode, &route.AuthStatus, &route.ChannelMeta, &route.AccountMeta,
		&ciphertext, &nonce, &channelName,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, notFound("Channel was not found.")
		}
		return nil, err
	}
	if len(ciphertext) > 0 {
		secret, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
		if err != nil {
			return nil, err
		}
		bundle, err := normalizedTokenBundleFromSecret(secret)
		if err != nil {
			return nil, err
		}
		route.APIKey = bundle.authSecret()
		route.TokenProvider = bundle.Provider
		route.AuthScheme = bundle.AuthScheme
		route.TokenSubject = bundle.Subject
		route.TokenMetadata = bundle.Metadata
	}
	modelName = strings.TrimSpace(modelName)
	endpoint = strings.TrimSpace(endpoint)
	if modelName != "" {
		route.UpstreamModel = modelName
	}
	path := strings.TrimSpace(targetPath)
	if path == "" {
		path = channelCheckProbePath(endpoint)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	baseClient, err := s.upstreamHTTPClient(route)
	if err != nil {
		return nil, err
	}
	client := withHTTPClientTimeout(baseClient, 15*time.Second)
	started := time.Now()
	status := "success"
	errorMessage := ""
	errorKind := ""
	upstreamStatus := 0
	targetURL := ""
	var req *http.Request
	var cancel context.CancelFunc = func() {}
	if route.APIKey != "" && endpoint != "" {
		probeBody := []byte(channelCheckProbeBody(route, modelName, endpoint))
		original, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(probeBody))
		if err != nil {
			return nil, badRequest("target_path produced an invalid URL.")
		}
		original.Header.Set("Content-Type", "application/json")
		req, cancel, err = s.newUpstreamRequest(original, route, probeBody)
		if err != nil {
			return nil, err
		}
		targetURL = req.URL.String()
	} else {
		targetURL = strings.TrimRight(route.BaseURL, "/") + path
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, badRequest("target_path produced an invalid URL.")
		}
		if route.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+route.APIKey)
		}
	}
	defer cancel()
	resp, err := client.Do(req)
	if err != nil {
		status = "failed"
		errorMessage = truncateForStorage(err.Error(), 1000)
		errorKind = classifyNetworkError(err)
	} else {
		upstreamStatus = resp.StatusCode
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		_ = resp.Body.Close()
		if route.APIKey != "" && endpoint != "" {
			if adapter, adapterErr := providerAdapterFor(route.ProviderType); adapterErr == nil {
				upstreamStatus, _, body = transformProviderResponse(adapter, route, endpoint, []byte(channelCheckProbeBody(route, modelName, endpoint)), upstreamStatus, resp.Header.Clone(), body)
			}
		}
		if route.APIKey != "" && endpoint != "" && upstreamStatus >= 400 {
			status = "failed"
			errorKind = upstreamStatusErrorCode(upstreamStatus, body)
			errorMessage = truncateForStorage(defaultString(string(bytes.TrimSpace(body)), resp.Status), 1000)
		} else if upstreamStatus >= 500 || upstreamStatus == http.StatusUnauthorized || upstreamStatus == http.StatusForbidden {
			status = "failed"
			errorKind = upstreamStatusErrorCode(upstreamStatus, body)
			errorMessage = truncateForStorage(defaultString(string(bytes.TrimSpace(body)), resp.Status), 1000)
		}
	}
	latencyMS := int(time.Since(started).Milliseconds())
	metadataJSON, err := encodeJSON(map[string]any{"channel_name": channelName, "target_url": targetURL, "error_kind": errorKind, "real_account_probe": route.APIKey != "" && endpoint != ""})
	if err != nil {
		return nil, err
	}
	var id string
	var testedAt time.Time
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO channel_test_results (
			provider_id, channel_id, account_id, test_type, model_name, endpoint, status, latency_ms, upstream_status, error_message, metadata_json
		)
		VALUES ($1, $2, NULLIF($3, '')::uuid, 'health', $4, $5, $6, $7, NULLIF($8, 0), $9, $10::jsonb)
		RETURNING id::text, tested_at
	`, providerID, route.ChannelID, route.AccountID, modelName, endpoint, status, latencyMS, upstreamStatus, errorMessage, metadataJSON).Scan(&id, &testedAt); err != nil {
		return nil, err
	}
	return map[string]any{
		"id":                 id,
		"provider_id":        providerID,
		"channel_id":         route.ChannelID,
		"account_id":         route.AccountID,
		"model_name":         modelName,
		"endpoint":           endpoint,
		"status":             status,
		"latency_ms":         latencyMS,
		"upstream_status":    nullableInt(upstreamStatus),
		"error_message":      errorMessage,
		"error_kind":         errorKind,
		"target_url":         targetURL,
		"real_account_probe": route.APIKey != "" && endpoint != "",
		"tested_at":          testedAt.UTC().Format(time.RFC3339),
	}, nil
}

func channelCheckProbeBody(route routeInfo, modelName string, endpoint string) string {
	model := firstNonEmpty(route.UpstreamModel, modelName, routeMetadataString(route, "model", "default_model"), "gpt-4o-mini")
	quotedModel := strconv.Quote(model)
	switch strings.TrimSpace(endpoint) {
	case "responses":
		return `{"model":` + quotedModel + `,"input":"ping","max_output_tokens":1,"stream":false}`
	case "messages":
		return `{"model":` + quotedModel + `,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`
	case "embeddings":
		return `{"model":` + quotedModel + `,"input":"ping"}`
	default:
		return `{"model":` + quotedModel + `,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`
	}
}

func channelCheckProbePath(endpoint string) string {
	switch strings.TrimSpace(endpoint) {
	case "chat":
		return "/v1/chat/completions"
	case "responses":
		return "/v1/responses"
	case "messages":
		return "/v1/messages"
	case "embeddings":
		return "/v1/embeddings"
	default:
		return "/models"
	}
}

func (s *Server) adminUsageSummary(w http.ResponseWriter, r *http.Request, auth authContext) error {
	whereSQL, args, err := usageWhereFromRequest(r, nil, nil)
	if err != nil {
		return err
	}
	var total, success, failed, rejected int64
	var cost, inputTokens, outputTokens, requestCount, avgDuration sql.NullString
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status = 'success'),
		       COUNT(*) FILTER (WHERE status = 'failed'),
		       COUNT(*) FILTER (WHERE status = 'rejected'),
		       COALESCE(SUM(actual_cost), 0)::text,
		       COALESCE(SUM(input_tokens), 0)::text,
		       COALESCE(SUM(output_tokens), 0)::text,
		       COALESCE(SUM(request_count), 0)::text,
		       COALESCE(ROUND(AVG(duration_ms))::text, '0')
		FROM usage_records
		`+whereSQL, args...).Scan(&total, &success, &failed, &rejected, &cost, &inputTokens, &outputTokens, &requestCount, &avgDuration); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":           total,
		"success":         success,
		"failed":          failed,
		"rejected":        rejected,
		"actual_cost":     nullStringValue(cost, "0"),
		"input_tokens":    nullStringValue(inputTokens, "0"),
		"output_tokens":   nullStringValue(outputTokens, "0"),
		"request_count":   nullStringValue(requestCount, "0"),
		"avg_duration_ms": nullStringValue(avgDuration, "0"),
	}, nil)
	return nil
}

func (s *Server) adminUsageExport(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 5000, 20000)
	whereSQL, args, err := usageWhereFromRequest(r, nil, nil)
	if err != nil {
		return err
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT request_id, requested_model, upstream_model, endpoint, input_tokens, output_tokens,
		       image_count, audio_seconds::text, request_count, actual_cost::text, status, error_code,
		       upstream_status, duration_ms, usage_source, created_at
		FROM usage_records
		`+whereSQL+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var body bytes.Buffer
	writer := csv.NewWriter(&body)
	_ = writer.Write([]string{"request_id", "requested_model", "upstream_model", "endpoint", "input_tokens", "output_tokens", "image_count", "audio_seconds", "request_count", "actual_cost", "status", "error_code", "upstream_status", "duration_ms", "usage_source", "created_at"})
	for rows.Next() {
		var requestID, requestedModel, upstreamModel, endpoint, audioSeconds, actualCost, status, errorCode, usageSource string
		var inputTokens, outputTokens, imageCount, requestCount, durationMS int
		var upstreamStatus sql.NullInt64
		var createdAt time.Time
		if err := rows.Scan(&requestID, &requestedModel, &upstreamModel, &endpoint, &inputTokens, &outputTokens, &imageCount, &audioSeconds, &requestCount, &actualCost, &status, &errorCode, &upstreamStatus, &durationMS, &usageSource, &createdAt); err != nil {
			return err
		}
		_ = writer.Write([]string{requestID, requestedModel, upstreamModel, endpoint, strconv.Itoa(inputTokens), strconv.Itoa(outputTokens), strconv.Itoa(imageCount), audioSeconds, strconv.Itoa(requestCount), actualCost, status, errorCode, sqlIntString(upstreamStatus), strconv.Itoa(durationMS), usageSource, createdAt.UTC().Format(time.RFC3339)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage-export.csv"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
	return nil
}

func (s *Server) adminUsageCleanup(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Before string `json:"before"`
		Status string `json:"status"`
		DryRun bool   `json:"dry_run"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	before, err := parseUsageTime(strings.TrimSpace(req.Before))
	if err != nil || before.IsZero() {
		return badRequest("before must be RFC3339 or YYYY-MM-DD.")
	}
	if !before.Before(time.Now().Add(-24 * time.Hour)) {
		return badRequest("before must be at least 24 hours in the past.")
	}
	status := strings.TrimSpace(req.Status)
	if status != "" && status != "success" && status != "failed" && status != "rejected" {
		return badRequest("Invalid usage status.")
	}
	args := []any{before}
	where := "created_at < $1"
	if status != "" {
		args = append(args, status)
		where += " AND status = $2"
	}
	var count int64
	if req.DryRun {
		if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM usage_records WHERE "+where, args...).Scan(&count); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"matched": count, "deleted": 0, "dry_run": true}, nil)
		return nil
	}
	result, err := s.db.ExecContext(r.Context(), "DELETE FROM usage_records WHERE "+where, args...)
	if err != nil {
		return err
	}
	count, _ = result.RowsAffected()
	audit(r.Context(), s.db, auth.UserID, "admin", "usage.cleanup", "usage_record", "", r, map[string]any{"deleted": count, "before": before.UTC().Format(time.RFC3339), "status": status})
	writeJSON(w, http.StatusOK, map[string]any{"matched": count, "deleted": count, "dry_run": false}, nil)
	return nil
}

func (s *Server) adminModelConflicts(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT 'alias_matches_model' AS conflict_type, ma.alias, ma.model_name, 'alias is also a model name' AS detail
		FROM model_aliases ma
		JOIN model_catalog mc ON mc.model_name = ma.alias
		UNION ALL
		SELECT 'disabled_model_has_active_channel', ca.model_name, ca.model_name, 'active channel ability points to disabled model'
		FROM channel_abilities ca
		JOIN model_catalog mc ON mc.model_name = ca.model_name
		JOIN channels c ON c.id = ca.channel_id
		WHERE mc.status <> 'active' AND c.status = 'active' AND ca.status = 'active'
		ORDER BY conflict_type, alias
		LIMIT 500
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var conflictType, alias, modelName, detail string
		if err := rows.Scan(&conflictType, &alias, &modelName, &detail); err != nil {
			return err
		}
		items = append(items, map[string]any{"conflict_type": conflictType, "alias": alias, "model_name": modelName, "detail": detail})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminMissingModels(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		WITH referenced AS (
		  SELECT COALESCE(NULLIF(ca.upstream_model, ''), ca.model_name) AS model_name,
		         ca.endpoint, ca.channel_id
		  FROM channel_abilities ca
		  WHERE ca.status = 'active'
		)
		SELECT r.model_name,
		       COALESCE(jsonb_agg(DISTINCT r.endpoint)::text, '[]') AS endpoints,
		       COUNT(DISTINCT r.channel_id)::int AS channel_count,
		       COUNT(DISTINCT a.id)::int AS account_count,
		       COALESCE(jsonb_agg(DISTINCT p.name)::text, '[]') AS providers
		FROM referenced r
		JOIN channels c ON c.id = r.channel_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN accounts a ON a.channel_id = c.id AND a.status = 'active'
		LEFT JOIN model_catalog mc ON mc.model_name = r.model_name
		WHERE r.model_name <> ''
		  AND mc.model_name IS NULL
		GROUP BY r.model_name
		ORDER BY r.model_name
		LIMIT 500
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var modelName, endpoints, providers string
		var channelCount, accountCount int
		if err := rows.Scan(&modelName, &endpoints, &channelCount, &accountCount, &providers); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"model_name":            modelName,
			"endpoint_capabilities": jsonArrayRaw(endpoints),
			"channel_count":         channelCount,
			"account_count":         accountCount,
			"providers":             jsonArrayRaw(providers),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminModelBatch(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ModelNames    []string `json:"model_names"`
		Status        *string  `json:"status"`
		PublicVisible *bool    `json:"public_visible"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	names := normalizeStringList(req.ModelNames)
	if len(names) == 0 {
		return badRequest("model_names is required.")
	}
	status := ""
	if req.Status != nil {
		next, err := defaultedStatus(*req.Status, "active", "active", "disabled")
		if err != nil {
			return err
		}
		status = next
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated := 0
	for _, name := range names {
		if status != "" {
			if _, err := tx.ExecContext(r.Context(), "UPDATE model_catalog SET status = $2 WHERE model_name = $1", name, status); err != nil {
				return err
			}
		}
		if req.PublicVisible != nil {
			if _, err := tx.ExecContext(r.Context(), "UPDATE model_catalog SET public_visible = $2 WHERE model_name = $1", name, *req.PublicVisible); err != nil {
				return err
			}
		}
		updated++
	}
	audit(r.Context(), tx, auth.UserID, "admin", "model.batch_update", "model", "", r, map[string]any{"count": updated})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated}, nil)
	return nil
}

func (s *Server) adminModelSyncFromChannels(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		WITH referenced AS (
		  SELECT COALESCE(NULLIF(ca.upstream_model, ''), ca.model_name) AS model_name,
		         ca.endpoint, ca.channel_id
		  FROM channel_abilities ca
		  WHERE ca.status = 'active'
		),
		missing AS (
		  SELECT r.model_name,
		         COALESCE(jsonb_agg(DISTINCT r.endpoint), '[]'::jsonb) AS endpoint_capabilities,
		         COALESCE(jsonb_agg(DISTINCT p.provider_type), '[]'::jsonb) AS provider_types,
		         COUNT(DISTINCT r.channel_id)::int AS channel_count
		  FROM referenced r
		  JOIN channels c ON c.id = r.channel_id
		  JOIN providers p ON p.id = c.provider_id
		  LEFT JOIN model_catalog mc ON mc.model_name = r.model_name
		  WHERE r.model_name <> ''
		    AND mc.model_name IS NULL
		  GROUP BY r.model_name
		)
		INSERT INTO model_catalog (model_name, display_name, provider_hint, endpoint_capabilities, public_visible, status, metadata_json)
		SELECT model_name, model_name, 'channel_sync', endpoint_capabilities, false, 'active',
		       jsonb_build_object('synced_from_channel_abilities', true, 'provider_types', provider_types, 'channel_count', channel_count)
		FROM missing
		RETURNING model_name
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	created := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		created = append(created, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "model.sync_from_channels", "model", "", r, map[string]any{"created": len(created)})
	writeJSON(w, http.StatusOK, map[string]any{"created": created}, nil)
	return nil
}

func (s *Server) adminTestNotificationChannel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	channelID := r.PathValue("channelId")
	if err := s.requireIDExists(r.Context(), "notification_channels", channelID, "Notification channel was not found."); err != nil {
		return err
	}
	err := s.emitNotification(r.Context(), notificationEventInput{
		EventType:  "notification.test",
		Severity:   "info",
		Title:      "Notification test",
		Message:    "Manual notification test from admin console.",
		TargetType: "notification_channel",
		TargetID:   channelID,
		Payload:    map[string]any{"operator_id": auth.UserID},
	})
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "notification_channel.test", "notification_channel", channelID, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"queued": true}, nil)
	return nil
}

func (s *Server) adminAnnouncements(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, title, body, audience, severity, starts_at, ends_at, status, metadata_json::text, created_at, updated_at
		FROM announcements
		ORDER BY created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items, err := scanAnnouncementRows(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateAnnouncement(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req announcementPayload
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id, err := s.upsertAnnouncement(r, auth, "", req, true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchAnnouncement(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req announcementPayload
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id, err := s.upsertAnnouncement(r, auth, r.PathValue("announcementId"), req, false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

type announcementPayload struct {
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Audience string         `json:"audience"`
	Severity string         `json:"severity"`
	StartsAt string         `json:"starts_at"`
	EndsAt   string         `json:"ends_at"`
	Status   string         `json:"status"`
	Metadata map[string]any `json:"metadata"`
}

func (s *Server) upsertAnnouncement(r *http.Request, auth authContext, id string, req announcementPayload, insert bool) (string, error) {
	if strings.TrimSpace(req.Title) == "" && insert {
		return "", badRequest("title is required.")
	}
	audience, err := defaultedEnum(req.Audience, "all", "all", "portal", "admin")
	if err != nil {
		return "", err
	}
	severity, err := defaultedEnum(req.Severity, "info", "info", "warning", "critical")
	if err != nil {
		return "", err
	}
	status, err := defaultedEnum(req.Status, "draft", "draft", "published", "archived")
	if err != nil {
		return "", err
	}
	startsAt, err := optionalTime(req.StartsAt, "starts_at")
	if err != nil {
		return "", err
	}
	endsAt, err := optionalTime(req.EndsAt, "ends_at")
	if err != nil {
		return "", err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return "", err
	}
	if insert {
		if err := s.db.QueryRowContext(r.Context(), `
			INSERT INTO announcements (title, body, audience, severity, starts_at, ends_at, status, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
			RETURNING id::text
		`, strings.TrimSpace(req.Title), req.Body, audience, severity, startsAt, endsAt, status, metadata).Scan(&id); err != nil {
			return "", err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "announcement.create", "announcement", id, r, nil)
		return id, nil
	}
	if err := s.requireIDExists(r.Context(), "announcements", id, "Announcement was not found."); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(r.Context(), `
		UPDATE announcements
		SET title = COALESCE(NULLIF($2, ''), title),
		    body = CASE WHEN $3::boolean THEN $4 ELSE body END,
		    audience = $5,
		    severity = $6,
		    starts_at = $7,
		    ends_at = $8,
		    status = $9,
		    metadata_json = $10::jsonb
		WHERE id = $1
	`, id, strings.TrimSpace(req.Title), true, req.Body, audience, severity, startsAt, endsAt, status, metadata); err != nil {
		return "", err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "announcement.update", "announcement", id, r, nil)
	return id, nil
}

func (s *Server) adminContentPages(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, slug, title, body, page_type, public_visible, status, metadata_json::text, created_at, updated_at
		FROM content_pages
		ORDER BY created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items, err := scanContentPageRows(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateContentPage(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req contentPagePayload
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id, err := s.upsertContentPage(r, auth, "", req, true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchContentPage(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req contentPagePayload
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	id, err := s.upsertContentPage(r, auth, r.PathValue("pageId"), req, false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

type contentPagePayload struct {
	Slug          string         `json:"slug"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	PageType      string         `json:"page_type"`
	PublicVisible *bool          `json:"public_visible"`
	Status        string         `json:"status"`
	Metadata      map[string]any `json:"metadata"`
}

func (s *Server) upsertContentPage(r *http.Request, auth authContext, id string, req contentPagePayload, insert bool) (string, error) {
	if insert && (strings.TrimSpace(req.Slug) == "" || strings.TrimSpace(req.Title) == "") {
		return "", badRequest("slug and title are required.")
	}
	pageType, err := defaultedEnum(req.PageType, "custom", "custom", "faq", "api_info", "legal", "about", "privacy", "terms")
	if err != nil {
		return "", err
	}
	status, err := defaultedEnum(req.Status, "draft", "draft", "published", "archived")
	if err != nil {
		return "", err
	}
	publicVisible := false
	if req.PublicVisible != nil {
		publicVisible = *req.PublicVisible
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return "", err
	}
	if insert {
		if err := s.db.QueryRowContext(r.Context(), `
			INSERT INTO content_pages (slug, title, body, page_type, public_visible, status, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
			RETURNING id::text
		`, strings.TrimSpace(req.Slug), strings.TrimSpace(req.Title), req.Body, pageType, publicVisible, status, metadata).Scan(&id); err != nil {
			return "", err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "content_page.create", "content_page", id, r, nil)
		return id, nil
	}
	if err := s.requireIDExists(r.Context(), "content_pages", id, "Content page was not found."); err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(r.Context(), `
		UPDATE content_pages
		SET slug = COALESCE(NULLIF($2, ''), slug),
		    title = COALESCE(NULLIF($3, ''), title),
		    body = $4,
		    page_type = $5,
		    public_visible = $6,
		    status = $7,
		    metadata_json = $8::jsonb
		WHERE id = $1
	`, id, strings.TrimSpace(req.Slug), strings.TrimSpace(req.Title), req.Body, pageType, publicVisible, status, metadata)
	if err != nil {
		return "", err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "content_page.update", "content_page", id, r, nil)
	return id, nil
}

func (s *Server) adminEmailVerificationSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	settings, err := s.emailVerificationSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, emailVerificationSettingsResponse(settings), nil)
	return nil
}

func (s *Server) adminPutEmailVerificationSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	current, err := s.emailVerificationSettings(r.Context())
	if err != nil {
		return err
	}
	var req struct {
		RegistrationVerificationEnabled bool   `json:"registration_verification_enabled"`
		Host                            string `json:"host"`
		Port                            int    `json:"port"`
		Username                        string `json:"username"`
		Password                        string `json:"password"`
		From                            string `json:"from"`
		TLSMode                         string `json:"tls_mode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	tlsMode := strings.ToLower(strings.TrimSpace(req.TLSMode))
	if err := validateSMTPSettingTLSMode(tlsMode); err != nil {
		return err
	}
	port := req.Port
	if port <= 0 {
		port = 587
	}
	settings := emailVerificationSettings{
		RegistrationVerificationEnabled: req.RegistrationVerificationEnabled,
		SMTP: smtpSettings{
			Host:               strings.TrimSpace(req.Host),
			Port:               port,
			Username:           strings.TrimSpace(req.Username),
			Password:           current.SMTP.Password,
			PasswordCiphertext: current.SMTP.PasswordCiphertext,
			PasswordNonce:      current.SMTP.PasswordNonce,
			From:               strings.TrimSpace(req.From),
			TLSMode:            tlsMode,
		},
	}
	if strings.TrimSpace(settings.SMTP.TLSMode) == "" {
		settings.SMTP.TLSMode = "starttls"
	}
	if strings.TrimSpace(req.Password) != "" {
		ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, req.Password)
		if err != nil {
			return err
		}
		settings.SMTP.Password = req.Password
		settings.SMTP.PasswordCiphertext = base64.StdEncoding.EncodeToString(ciphertext)
		settings.SMTP.PasswordNonce = base64.StdEncoding.EncodeToString(nonce)
	}
	value, err := encodeJSON(emailVerificationSettingsValue(settings))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO system_settings (setting_key, category, setting_value_json, is_public, updated_by)
		VALUES ($1, 'auth', $2::jsonb, false, $3)
		ON CONFLICT (setting_key) DO UPDATE SET
		  category = EXCLUDED.category,
		  setting_value_json = EXCLUDED.setting_value_json,
		  is_public = false,
		  updated_by = EXCLUDED.updated_by
	`, emailVerificationSettingKey, value, auth.UserID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "email_verification_settings.update", "system_setting", emailVerificationSettingKey, r, nil)
	writeJSON(w, http.StatusOK, emailVerificationSettingsResponse(settings), nil)
	return nil
}

func (s *Server) adminTestEmailVerificationSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	email := trimEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		return badRequest("A valid email is required.")
	}
	settings, err := s.emailVerificationSettings(r.Context())
	if err != nil {
		return err
	}
	if !settings.SMTP.enabled() {
		return upstreamUnavailable("smtp_not_configured", "SMTP is not configured.")
	}
	if err := s.sendEmailWithSMTP(r.Context(), settings.SMTP, email, "Elucid Relay email test", "This is a test email from Elucid Relay.\n"); err != nil {
		return upstreamUnavailable("email_delivery_failed", "Test email could not be sent.")
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "email_verification_settings.test", "system_setting", emailVerificationSettingKey, r, map[string]any{"email": email})
	writeJSON(w, http.StatusOK, map[string]any{"sent": true}, nil)
	return nil
}

func emailVerificationSettingsValue(settings emailVerificationSettings) map[string]any {
	return map[string]any{
		"registration_verification_enabled": settings.RegistrationVerificationEnabled,
		"smtp": map[string]any{
			"host":                strings.TrimSpace(settings.SMTP.Host),
			"port":                settings.SMTP.Port,
			"username":            strings.TrimSpace(settings.SMTP.Username),
			"password_ciphertext": settings.SMTP.PasswordCiphertext,
			"password_nonce":      settings.SMTP.PasswordNonce,
			"from":                strings.TrimSpace(settings.SMTP.From),
			"tls_mode":            strings.TrimSpace(settings.SMTP.TLSMode),
		},
	}
}

func emailVerificationSettingsResponse(settings emailVerificationSettings) map[string]any {
	return map[string]any{
		"registration_verification_enabled": settings.RegistrationVerificationEnabled,
		"smtp": map[string]any{
			"host":             settings.SMTP.Host,
			"port":             settings.SMTP.Port,
			"username":         settings.SMTP.Username,
			"password_present": settings.SMTP.Password != "" || settings.SMTP.PasswordCiphertext != "",
			"from":             settings.SMTP.From,
			"tls_mode":         settings.SMTP.TLSMode,
			"configured":       settings.SMTP.enabled(),
		},
	}
}

func validateSMTPSettingTLSMode(raw string) error {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" || mode == "starttls" || mode == "tls" || mode == "none" {
		return nil
	}
	return badRequest("SMTP TLS mode must be starttls, tls, or none.")
}

func (s *Server) adminGroups(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT g.id::text, g.name, g.description, g.status, g.priority, g.model_multiplier::text, g.rpm_limit,
		       COALESCE(g.monthly_usd_limit::text, ''), g.metadata_json::text, g.created_at, g.updated_at,
		       COALESCE((SELECT jsonb_agg(jsonb_build_object('user_id', m.user_id::text, 'role', m.role) ORDER BY m.created_at DESC)::text FROM user_group_memberships m WHERE m.group_id = g.id), '[]'),
		       COALESCE((SELECT jsonb_agg(jsonb_build_object('id', p.id::text, 'model_name', p.model_name, 'endpoint', p.endpoint, 'permission', p.permission, 'rpm_limit', p.rpm_limit, 'price_multiplier', p.price_multiplier::text) ORDER BY p.created_at DESC)::text FROM group_model_permissions p WHERE p.group_id = g.id), '[]')
		FROM groups g
		ORDER BY g.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, description, status, multiplier, monthlyLimit, metadata, members, permissions string
		var rpmLimit sql.NullInt64
		var priority int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &name, &description, &status, &priority, &multiplier, &rpmLimit, &monthlyLimit, &metadata, &createdAt, &updatedAt, &members, &permissions); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":                id,
			"name":              name,
			"description":       description,
			"status":            status,
			"priority":          priority,
			"model_multiplier":  multiplier,
			"rpm_limit":         nullableSQLInt(rpmLimit),
			"monthly_usd_limit": monthlyLimit,
			"metadata":          jsonRaw(metadata),
			"members":           jsonArrayRaw(members),
			"permissions":       jsonArrayRaw(permissions),
			"created_at":        createdAt.UTC().Format(time.RFC3339),
			"updated_at":        updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateGroup(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertGroup(r, auth, "", true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchGroup(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertGroup(r, auth, r.PathValue("groupId"), false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) upsertGroup(r *http.Request, auth authContext, id string, insert bool) (string, error) {
	var req struct {
		Name            string         `json:"name"`
		Description     string         `json:"description"`
		Status          string         `json:"status"`
		Priority        *int           `json:"priority"`
		ModelMultiplier string         `json:"model_multiplier"`
		RPMLimit        *int           `json:"rpm_limit"`
		MonthlyUSDLimit *string        `json:"monthly_usd_limit"`
		Metadata        map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	if insert && strings.TrimSpace(req.Name) == "" {
		return "", badRequest("name is required.")
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return "", err
	}
	multiplier := strings.TrimSpace(req.ModelMultiplier)
	if multiplier == "" {
		multiplier = "1"
	}
	if _, err := strconv.ParseFloat(multiplier, 64); err != nil {
		return "", badRequest("model_multiplier must be a decimal string.")
	}
	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
	}
	var monthly any
	if req.MonthlyUSDLimit != nil && strings.TrimSpace(*req.MonthlyUSDLimit) != "" {
		monthly = strings.TrimSpace(*req.MonthlyUSDLimit)
		if _, err := strconv.ParseFloat(monthly.(string), 64); err != nil {
			return "", badRequest("monthly_usd_limit must be a decimal string.")
		}
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return "", err
	}
	if insert {
		err = s.db.QueryRowContext(r.Context(), `
			INSERT INTO groups (name, description, status, priority, model_multiplier, rpm_limit, monthly_usd_limit, metadata_json)
			VALUES ($1, $2, $3, $4, $5::numeric, $6, $7::numeric, $8::jsonb)
			RETURNING id::text
		`, strings.TrimSpace(req.Name), req.Description, status, priority, multiplier, req.RPMLimit, monthly, metadata).Scan(&id)
	} else {
		if err := s.requireIDExists(r.Context(), "groups", id, "Group was not found."); err != nil {
			return "", err
		}
		_, err = s.db.ExecContext(r.Context(), `
			UPDATE groups
			SET name = COALESCE(NULLIF($2, ''), name),
			    description = $3,
			    status = $4,
			    priority = $5,
			    model_multiplier = $6::numeric,
			    rpm_limit = $7,
			    monthly_usd_limit = $8::numeric,
			    metadata_json = $9::jsonb
			WHERE id = $1
		`, id, strings.TrimSpace(req.Name), req.Description, status, priority, multiplier, req.RPMLimit, monthly, metadata)
	}
	if err != nil {
		return "", err
	}
	action := "group.update"
	if insert {
		action = "group.create"
	}
	audit(r.Context(), s.db, auth.UserID, "admin", action, "group", id, r, nil)
	return id, nil
}

func (s *Server) adminAddGroupMember(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.UserID) == "" {
		return badRequest("user_id is required.")
	}
	role := defaultString(req.Role, "member")
	groupID := r.PathValue("groupId")
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO user_group_memberships (group_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (group_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, groupID, strings.TrimSpace(req.UserID), strings.TrimSpace(role)); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "group.member_upsert", "group", groupID, r, map[string]any{"user_id": req.UserID})
	writeJSON(w, http.StatusOK, map[string]any{"group_id": groupID, "user_id": req.UserID, "updated": true}, nil)
	return nil
}

func (s *Server) adminDeleteGroupMember(w http.ResponseWriter, r *http.Request, auth authContext) error {
	groupID := r.PathValue("groupId")
	userID := r.PathValue("userId")
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM user_group_memberships WHERE group_id = $1 AND user_id = $2", groupID, userID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "group.member_delete", "group", groupID, r, map[string]any{"user_id": userID})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
	return nil
}

func (s *Server) adminAddGroupModelPermission(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ModelName       string         `json:"model_name"`
		Endpoint        string         `json:"endpoint"`
		Permission      string         `json:"permission"`
		RPMLimit        *int           `json:"rpm_limit"`
		PriceMultiplier string         `json:"price_multiplier"`
		Metadata        map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ModelName) == "" {
		return badRequest("model_name is required.")
	}
	permission, err := defaultedEnum(req.Permission, "allow", "allow", "deny")
	if err != nil {
		return err
	}
	priceMultiplier := strings.TrimSpace(req.PriceMultiplier)
	if priceMultiplier == "" {
		priceMultiplier = "1"
	}
	if parsed, err := strconv.ParseFloat(priceMultiplier, 64); err != nil || parsed < 0 {
		return badRequest("price_multiplier must be a non-negative decimal string.")
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return err
	}
	groupID := r.PathValue("groupId")
	var id string
	if err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO group_model_permissions (group_id, model_name, endpoint, permission, rpm_limit, price_multiplier, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::jsonb)
		ON CONFLICT (group_id, model_name, endpoint) DO UPDATE SET
		  permission = EXCLUDED.permission,
		  rpm_limit = EXCLUDED.rpm_limit,
		  price_multiplier = EXCLUDED.price_multiplier,
		  metadata_json = EXCLUDED.metadata_json
		RETURNING id::text
	`, groupID, strings.TrimSpace(req.ModelName), strings.TrimSpace(req.Endpoint), permission, req.RPMLimit, priceMultiplier, metadata).Scan(&id); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "group.model_permission_upsert", "group", groupID, r, map[string]any{"model_name": req.ModelName})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminRiskControls(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{limitFromRequest(r, 100, 500)}
	where := ""
	if ruleType := strings.TrimSpace(r.URL.Query().Get("rule_type")); ruleType != "" {
		args = append(args, ruleType)
		where = "WHERE rule_type = $2"
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, rule_type, name, pattern, action, severity, status, metadata_json::text, created_at, updated_at
		FROM risk_rules
		`+where+`
		ORDER BY created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, ruleType, name, pattern, action, severity, status, metadata string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &ruleType, &name, &pattern, &action, &severity, &status, &metadata, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "rule_type": ruleType, "name": name, "pattern": pattern, "action": action, "severity": severity, "status": status, "metadata": jsonRaw(metadata), "created_at": createdAt.UTC().Format(time.RFC3339), "updated_at": updatedAt.UTC().Format(time.RFC3339)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateRiskControl(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertRiskControl(r, auth, "", true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchRiskControl(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertRiskControl(r, auth, r.PathValue("ruleId"), false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) upsertRiskControl(r *http.Request, auth authContext, id string, insert bool) (string, error) {
	var req struct {
		RuleType string         `json:"rule_type"`
		Name     string         `json:"name"`
		Pattern  string         `json:"pattern"`
		Action   string         `json:"action"`
		Severity string         `json:"severity"`
		Status   string         `json:"status"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	if insert && strings.TrimSpace(req.Name) == "" {
		return "", badRequest("name is required.")
	}
	ruleType, err := defaultedEnum(req.RuleType, "sensitive_word", "sensitive_word", "ssrf_target", "request_limit", "bot_protection", "abuse_pattern")
	if err != nil {
		return "", err
	}
	action, err := defaultedEnum(req.Action, "flag", "flag", "block", "throttle")
	if err != nil {
		return "", err
	}
	severity, err := defaultedEnum(req.Severity, "warning", "info", "warning", "critical")
	if err != nil {
		return "", err
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return "", err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return "", err
	}
	if insert {
		err = s.db.QueryRowContext(r.Context(), `
			INSERT INTO risk_rules (rule_type, name, pattern, action, severity, status, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
			RETURNING id::text
		`, ruleType, strings.TrimSpace(req.Name), req.Pattern, action, severity, status, metadata).Scan(&id)
	} else {
		if err := s.requireIDExists(r.Context(), "risk_rules", id, "Risk rule was not found."); err != nil {
			return "", err
		}
		_, err = s.db.ExecContext(r.Context(), `
			UPDATE risk_rules
			SET rule_type = $2,
			    name = COALESCE(NULLIF($3, ''), name),
			    pattern = $4,
			    action = $5,
			    severity = $6,
			    status = $7,
			    metadata_json = $8::jsonb
			WHERE id = $1
		`, id, ruleType, strings.TrimSpace(req.Name), req.Pattern, action, severity, status, metadata)
	}
	if err != nil {
		return "", err
	}
	actionName := "risk_rule.update"
	if insert {
		actionName = "risk_rule.create"
	}
	audit(r.Context(), s.db, auth.UserID, "admin", actionName, "risk_rule", id, r, nil)
	return id, nil
}

func (s *Server) publicPricing(w http.ResponseWriter, r *http.Request) {
	models, err := s.listModels(r.Context(), true, "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models, nil)
}

func (s *Server) publicChannelStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT p.name, p.provider_type, c.name, c.status,
		       COUNT(a.id) FILTER (WHERE a.status = 'active') AS active_accounts,
		       COUNT(a.id) AS total_accounts,
		       ctr.status, ctr.latency_ms, ctr.tested_at,
		       msj.status, msj.error_message, msj.finished_at
		FROM channels c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN accounts a ON a.channel_id = c.id
		LEFT JOIN LATERAL (
		  SELECT status, latency_ms, tested_at
		  FROM channel_test_results
		  WHERE channel_id = c.id
		  ORDER BY tested_at DESC
		  LIMIT 1
		) ctr ON true
		LEFT JOIN LATERAL (
		  SELECT status, error_message, finished_at
		  FROM model_sync_jobs
		  WHERE channel_id = c.id
		  ORDER BY created_at DESC
		  LIMIT 1
		) msj ON true
		WHERE p.status = 'active'
		GROUP BY p.name, p.provider_type, c.id, ctr.status, ctr.latency_ms, ctr.tested_at, msj.status, msj.error_message, msj.finished_at
		ORDER BY p.name, c.name
		LIMIT 500
	`)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var providerName, providerType, channelName, channelStatus string
		var activeAccounts, totalAccounts int
		var testStatus sql.NullString
		var syncStatus, syncError sql.NullString
		var latency sql.NullInt64
		var testedAt, syncFinishedAt sql.NullTime
		if err := rows.Scan(&providerName, &providerType, &channelName, &channelStatus, &activeAccounts, &totalAccounts, &testStatus, &latency, &testedAt, &syncStatus, &syncError, &syncFinishedAt); err != nil {
			writeError(w, r, err)
			return
		}
		items = append(items, map[string]any{"provider_name": providerName, "provider_type": providerType, "channel_name": channelName, "status": channelStatus, "active_accounts": activeAccounts, "total_accounts": totalAccounts, "last_test_status": nullableSQLString(testStatus), "last_test_latency_ms": nullableSQLInt(latency), "last_tested_at": nullableSQLTime(testedAt), "last_model_sync_status": nullableSQLString(syncStatus), "last_model_sync_error": nullableSQLString(syncError), "last_model_sync_at": nullableSQLTime(syncFinishedAt)})
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items, nil)
}

func (s *Server) publicRankings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT requested_model, endpoint, COUNT(*) AS request_count, COALESCE(SUM(actual_cost), 0)::text
		FROM usage_records
		WHERE created_at >= now() - interval '7 days'
		  AND status = 'success'
		GROUP BY requested_model, endpoint
		ORDER BY request_count DESC
		LIMIT 50
	`)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var model, endpoint, cost string
		var count int64
		if err := rows.Scan(&model, &endpoint, &count, &cost); err != nil {
			writeError(w, r, err)
			return
		}
		items = append(items, map[string]any{"model": model, "endpoint": endpoint, "request_count": count, "actual_cost": cost})
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items, nil)
}

func (s *Server) publicAnnouncements(w http.ResponseWriter, r *http.Request) {
	audience := strings.TrimSpace(r.URL.Query().Get("audience"))
	if audience != "admin" && audience != "portal" {
		audience = "portal"
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, title, body, audience, severity, starts_at, ends_at, status, metadata_json::text, created_at, updated_at
		FROM announcements
		WHERE status = 'published'
		  AND audience IN ('all', $1)
		  AND (starts_at IS NULL OR starts_at <= now())
		  AND (ends_at IS NULL OR ends_at > now())
		ORDER BY created_at DESC
		LIMIT 20
	`, audience)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	items, err := scanAnnouncementRows(rows)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items, nil)
}

func (s *Server) publicContentPage(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, slug, title, body, page_type, public_visible, status, metadata_json::text, created_at, updated_at
		FROM content_pages
		WHERE slug = $1 AND public_visible = true AND status = 'published'
		LIMIT 1
	`, r.PathValue("slug"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	items, err := scanContentPageRows(rows)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(items) == 0 {
		writeError(w, r, notFound("Content page was not found."))
		return
	}
	writeJSON(w, http.StatusOK, items[0], nil)
}

func scanAnnouncementRows(rows *sql.Rows) ([]map[string]any, error) {
	items := []map[string]any{}
	for rows.Next() {
		var id, title, body, audience, severity, status, metadata string
		var startsAt, endsAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &title, &body, &audience, &severity, &startsAt, &endsAt, &status, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "title": title, "body": body, "audience": audience, "severity": severity, "starts_at": nullableSQLTime(startsAt), "ends_at": nullableSQLTime(endsAt), "status": status, "metadata": jsonRaw(metadata), "created_at": createdAt.UTC().Format(time.RFC3339), "updated_at": updatedAt.UTC().Format(time.RFC3339)})
	}
	return items, rows.Err()
}

func scanContentPageRows(rows *sql.Rows) ([]map[string]any, error) {
	items := []map[string]any{}
	for rows.Next() {
		var id, slug, title, body, pageType, status, metadata string
		var publicVisible bool
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &slug, &title, &body, &pageType, &publicVisible, &status, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "slug": slug, "title": title, "body": body, "page_type": pageType, "public_visible": publicVisible, "status": status, "metadata": jsonRaw(metadata), "created_at": createdAt.UTC().Format(time.RFC3339), "updated_at": updatedAt.UTC().Format(time.RFC3339)})
	}
	return items, rows.Err()
}

func proxyProtocolFromURL(value string) string {
	if isDirectProxyMode(value) {
		return "direct"
	}
	if index := strings.Index(value, "://"); index > 0 {
		return value[:index]
	}
	return ""
}

func nullStringValue(value sql.NullString, fallback string) string {
	if !value.Valid || value.String == "" {
		return fallback
	}
	return value.String
}

func sqlIntString(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return strconv.FormatInt(value.Int64, 10)
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func defaultMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func defaultedEnum(value string, fallback string, allowed ...string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback, nil
	}
	for _, candidate := range allowed {
		if trimmed == candidate {
			return trimmed, nil
		}
	}
	return "", badRequest("Invalid value.")
}

func optionalTime(value string, field string) (any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, badRequest(field + " must be RFC3339.")
	}
	return parsed, nil
}
