package httpserver

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
)

type apiKeyAuth struct {
	UserID          string
	UserStatus      string
	APIKeyID        string
	APIKeyStatus    string
	RoutingMode     string
	ExpiresAt       sql.NullTime
	IPAllowlistJSON string
	ModelScopeJSON  string
	WalletID        string
	Balance         string
	ReservedBalance string
}

type modelInfo struct {
	ModelName          string
	Aliases            []string
	InputUSDPer1K      float64
	OutputUSDPer1K     float64
	RequestUSD         float64
	MinChargeUSD       float64
	BillingMode        string
	BillingExpr        string
	CacheReadUSDPer1K  float64
	CacheWriteUSDPer1K float64
	ImageUSDPerUnit    float64
	AudioUSDPerSecond  float64
	RequestParams      map[string]any
}

type routeInfo struct {
	ChannelID      string         `json:"channel_id"`
	AccountID      string         `json:"account_id"`
	BaseURL        string         `json:"base_url"`
	ProviderType   string         `json:"provider_type"`
	UpstreamModel  string         `json:"upstream_model"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	ProxyURL       string         `json:"proxy_url,omitempty"`
	RoutingMode    string         `json:"routing_mode"`
	OwnerUserID    string         `json:"owner_user_id,omitempty"`
	AuthMode       string         `json:"auth_mode,omitempty"`
	AuthStatus     string         `json:"auth_status,omitempty"`
	AuthExpiresAt  string         `json:"auth_expires_at,omitempty"`
	RefreshDueAt   string         `json:"refresh_due_at,omitempty"`
	TokenProvider  string         `json:"token_provider,omitempty"`
	AuthScheme     string         `json:"auth_scheme,omitempty"`
	TokenSubject   string         `json:"-"`
	TokenMetadata  map[string]any `json:"-"`
	AbilityMeta    string         `json:"-"`
	ChannelMeta    string         `json:"-"`
	AccountMeta    string         `json:"-"`
	RuntimeMeta    string         `json:"-"`
	APIKey         string         `json:"-"`
}

type northboundRequestContext struct {
	RequestID              string
	Fingerprint            string
	Auth                   apiKeyAuth
	Model                  modelInfo
	Policy                 effectivePolicy
	Risk                   riskDecision
	Requested              string
	Endpoint               string
	Reserve                float64
	RouteAffinityKey       string
	RouteAffinityTTL       int
	RouteAffinityRuleName  string
	RouteAffinitySkipRetry bool
	RouteTags              []string
}

type northboundModelRecord struct {
	ModelName            string
	DisplayName          string
	ProviderHint         string
	EndpointCapabilities string
	Metadata             string
	Aliases              string
	Created              int64
}

func (s *Server) northboundModels(w http.ResponseWriter, r *http.Request) {
	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	records, err := s.listNorthboundModelRecords(r.Context(), auth)
	if err != nil {
		writeError(w, r, err)
		return
	}

	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, northboundModelItem(record))
	}

	writeRawJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}

func (s *Server) northboundModel(w http.ResponseWriter, r *http.Request) {
	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	model := strings.TrimSpace(r.PathValue("model"))
	if model == "" {
		writeError(w, r, badRequest("model is required."))
		return
	}

	record, err := s.getNorthboundModelRecord(r.Context(), auth, model)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, notFound("Model not found."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeRawJSON(w, http.StatusOK, northboundModelItem(record))
}

func (s *Server) listNorthboundModelRecords(ctx context.Context, auth apiKeyAuth) ([]northboundModelRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH scope AS (
			SELECT value
			FROM jsonb_array_elements_text($1::jsonb) AS t(value)
		)
		SELECT mc.model_name, mc.display_name, mc.provider_hint, mc.endpoint_capabilities::text,
		       mc.metadata_json::text, extract(epoch from mc.created_at)::bigint,
		       COALESCE((SELECT jsonb_agg(ma.alias ORDER BY ma.alias)::text FROM model_aliases ma WHERE ma.model_name = mc.model_name), '[]')
		FROM model_catalog mc
		WHERE mc.status = 'active'
		  AND mc.public_visible = true
		  AND (
		    $1::jsonb = '[]'::jsonb
		    OR EXISTS (
		      SELECT 1
		      FROM scope s
		      WHERE s.value = mc.model_name
		         OR EXISTS (
		           SELECT 1
		           FROM model_aliases ma
		           WHERE ma.model_name = mc.model_name
		             AND ma.alias = s.value
		         )
		    )
		  )
		ORDER BY mc.model_name
	`, auth.ModelScopeJSON)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []northboundModelRecord{}
	for rows.Next() {
		var record northboundModelRecord
		if err := rows.Scan(
			&record.ModelName, &record.DisplayName, &record.ProviderHint, &record.EndpointCapabilities,
			&record.Metadata, &record.Created, &record.Aliases,
		); err != nil {
			return nil, err
		}
		policy, err := s.resolveEffectivePolicy(ctx, auth.UserID, record.ModelName, "")
		if err != nil {
			return nil, err
		}
		if policyAllowsListedModel(policy) {
			records = append(records, record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Server) getNorthboundModelRecord(ctx context.Context, auth apiKeyAuth, requested string) (northboundModelRecord, error) {
	var record northboundModelRecord
	err := s.db.QueryRowContext(ctx, `
		WITH resolved AS (
			SELECT model_name FROM model_catalog WHERE model_name = $2
			UNION
			SELECT model_name FROM model_aliases WHERE alias = $2
			LIMIT 1
		),
		scope AS (
			SELECT value
			FROM jsonb_array_elements_text($1::jsonb) AS t(value)
		)
		SELECT mc.model_name, mc.display_name, mc.provider_hint, mc.endpoint_capabilities::text,
		       mc.metadata_json::text, extract(epoch from mc.created_at)::bigint,
		       COALESCE((SELECT jsonb_agg(ma.alias ORDER BY ma.alias)::text FROM model_aliases ma WHERE ma.model_name = mc.model_name), '[]')
		FROM model_catalog mc
		JOIN resolved r ON r.model_name = mc.model_name
		WHERE mc.status = 'active'
		  AND mc.public_visible = true
		  AND (
		    $1::jsonb = '[]'::jsonb
		    OR EXISTS (
		      SELECT 1
		      FROM scope s
		      WHERE s.value = mc.model_name
		         OR EXISTS (
		           SELECT 1
		           FROM model_aliases ma
		           WHERE ma.model_name = mc.model_name
		             AND ma.alias = s.value
		         )
		    )
		  )
	`, auth.ModelScopeJSON, requested).Scan(
		&record.ModelName, &record.DisplayName, &record.ProviderHint, &record.EndpointCapabilities,
		&record.Metadata, &record.Created, &record.Aliases,
	)
	if err != nil {
		return record, err
	}
	policy, err := s.resolveEffectivePolicy(ctx, auth.UserID, record.ModelName, "")
	if err != nil {
		return record, err
	}
	if !policyAllowsListedModel(policy) {
		return northboundModelRecord{}, sql.ErrNoRows
	}
	return record, nil
}

func northboundModelItem(record northboundModelRecord) map[string]any {
	item := map[string]any{
		"id":                    record.ModelName,
		"object":                "model",
		"created":               record.Created,
		"owned_by":              "elucid-relay",
		"display_name":          record.DisplayName,
		"provider_hint":         record.ProviderHint,
		"aliases":               jsonArrayRaw(record.Aliases),
		"endpoint_capabilities": jsonArrayRaw(record.EndpointCapabilities),
		"metadata":              jsonRaw(record.Metadata),
	}
	appendModelDisplayMetadata(item, record.Metadata)
	return item
}

func (s *Server) northboundProxy(w http.ResponseWriter, r *http.Request) {
	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	body, err := readNorthboundRequestBody(r.Body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	_ = r.Body.Close()
	body, err = decodeNorthboundRequestBody(body, r.Header.Get("Content-Encoding"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r.Header.Del("Content-Encoding")

	endpoint := endpointFromPath(r.URL.Path)
	body = s.applyNorthboundReplayGuard(r.Context(), endpoint, body, r.Header.Get("Content-Type"))
	requestedModel, err := modelFromBody(body, r.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if requestedModel == "" {
		writeError(w, r, badRequest("model is required."))
		return
	}
	if err := validateNorthboundClientRequest(endpoint, body, r.Header.Get("Content-Type")); err != nil {
		writeError(w, r, err)
		return
	}
	affinityMatch := s.routeAffinityMatchFromRequest(r.Context(), r, body, r.Header.Get("Content-Type"), requestedModel, endpoint)
	routeAffinityKey := affinityMatch.Key
	routeAffinityTTL := affinityMatch.TTLSeconds
	routeAffinityRuleName := affinityMatch.RuleName
	routeAffinitySkipRetry := affinityMatch.SkipRetryOnFailure
	if routeAffinityKey == "" {
		routeAffinityKey = routeAffinityKeyFromRequest(r, body, r.Header.Get("Content-Type"))
	}
	if routeAffinityKey == "" {
		settings := s.reverseProxySettingsOrDefault(r.Context())
		if settings.DigestAffinityEnabled {
			routeAffinityKey = routeDigestAffinityKeyFromRequest(r, endpoint, body, r.Header.Get("Content-Type"))
		}
	}
	routeTags := routeTagsFromRequest(r, body, r.Header.Get("Content-Type"))
	requestID := requestIDFromContext(r.Context())
	fingerprint := requestFingerprint(r.Method, r.URL.Path, body)
	if err := s.claimIdempotency(r.Context(), auth, requestID, fingerprint); err != nil {
		writeError(w, r, err)
		return
	}
	risk, err := s.evaluateRisk(r.Context(), auth, r, requestID, endpoint, body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := risk.Err(); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			Endpoint:           endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	model, err := s.resolveModel(r.Context(), requestedModel, endpoint)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			Endpoint:           endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	body = applyModelRequestParams(body, r.Header.Get("Content-Type"), model.RequestParams)
	if err := validateModelScope(auth.ModelScopeJSON, requestedModel, model.ModelName, model.Aliases...); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	policy, err := s.resolveEffectivePolicy(r.Context(), auth.UserID, model.ModelName, endpoint)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := policy.enforceModelAccess(); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	reserveAmount := applyBillingMultiplier(estimateReserve(model, body, r.Header.Get("Content-Type"), endpoint), policy)
	if auth.RoutingMode == "byo" {
		reserveAmount = 0
	}
	if err := s.enforceSpendLimits(r.Context(), auth, reserveAmount); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	if err := s.enforceEffectivePolicyLimits(r.Context(), auth, policy, reserveAmount, model.ModelName, endpoint); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	reservation, err := s.reserveWallet(r.Context(), auth.UserID, reserveAmount, requestID)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	reqCtx := northboundRequestContext{
		RequestID:              requestID,
		Fingerprint:            fingerprint,
		Auth:                   auth,
		Model:                  model,
		Policy:                 policy,
		Risk:                   risk,
		Requested:              requestedModel,
		Endpoint:               endpoint,
		Reserve:                reserveAmount,
		RouteAffinityKey:       routeAffinityKey,
		RouteAffinityTTL:       routeAffinityTTL,
		RouteAffinityRuleName:  routeAffinityRuleName,
		RouteAffinitySkipRetry: routeAffinitySkipRetry,
		RouteTags:              routeTags,
	}
	if isStreamingRequest(body, r.Header.Get("Content-Type"), r.Header.Get("Accept")) {
		route, err := s.checkoutRouteAttempt(r.Context(), model.ModelName, endpoint, auth.UserID, auth.APIKeyID, auth.RoutingMode, routeAffinityKey, routeAffinityTTL, routeAffinityRuleName, routeTags, 0)
		if err != nil {
			_ = s.releaseReservation(context.Background(), reservation, "no_available_account", "")
			_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
				RequestID:          requestID,
				RequestFingerprint: fingerprint,
				UserID:             auth.UserID,
				APIKeyID:           auth.APIKeyID,
				RequestedModel:     requestedModel,
				UpstreamModel:      model.ModelName,
				Endpoint:           endpoint,
				EstimatedCost:      reserveAmount,
				Pricing:            model,
				Status:             "rejected",
				ErrorCode:          errorCode(err),
				ErrorMessage:       err.Error(),
				EffectivePolicy:    policy,
				RiskDecision:       risk,
			})
			_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
			writeError(w, r, err)
			return
		}
		s.streamUpstream(w, r, route, body, reqCtx, reservation)
		return
	}

	route, upstreamStatus, upstreamHeader, upstreamBody, durationMS, err := s.callUpstreamWithRetry(r, model.ModelName, endpoint, body, auth.UserID, auth.APIKeyID, auth.RoutingMode, routeAffinityKey, routeAffinityTTL, routeAffinityRuleName, routeAffinitySkipRetry, routeTags)
	if err != nil {
		failureCode := upstreamFailureCode(err)
		_ = s.releaseReservation(context.Background(), reservation, failureCode, route.AccountID)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     requestedModel,
			UpstreamModel:      upstreamModelForRecord(route, model),
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "failed",
			ErrorCode:          failureCode,
			ErrorMessage:       err.Error(),
			DurationMS:         durationMS,
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "failed")
		writeError(w, r, upstreamUnavailable(failureCode, upstreamFailureMessage(err)))
		return
	}

	adapter, err := providerAdapterFor(route.ProviderType)
	if err != nil {
		_ = s.releaseReservation(context.Background(), reservation, "unsupported_provider", route.AccountID)
		writeError(w, r, err)
		return
	}
	upstreamStatus, upstreamHeader, upstreamBody = transformProviderResponse(adapter, route, endpoint, body, upstreamStatus, upstreamHeader, upstreamBody)
	upstreamHeader, upstreamBody = applyRouteResponseRewrite(r, route, upstreamHeader, upstreamBody)

	if upstreamStatus < 200 || upstreamStatus >= 300 {
		statusCode := upstreamStatusErrorCode(upstreamStatus, upstreamBody)
		_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     requestedModel,
			UpstreamModel:      upstreamModelForRecord(route, model),
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       string(bytes.TrimSpace(upstreamBody)),
			UpstreamStatus:     upstreamStatus,
			DurationMS:         durationMS,
			MeteringMetadata:   usageMetadataWithClientStatusMapping(route, upstreamStatus, nil),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "failed")
		copyRouteResponse(w, route, upstreamStatus, upstreamHeader, upstreamBody)
		return
	}

	metering := adapter.ParseUsage(endpoint, body, upstreamBody)
	actualCost := actualCostForRoutingMode(
		auth.RoutingMode,
		applyBillingMultiplier(calculateActualCost(model, metering.usageCounts(), metering.meteringMetrics()), policy),
	)
	if err := s.settleReservation(context.Background(), reservation, actualCost, route.AccountID, policy); err != nil {
		writeError(w, r, err)
		return
	}

	_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
		RequestID:          requestID,
		RequestFingerprint: fingerprint,
		UserID:             auth.UserID,
		APIKeyID:           auth.APIKeyID,
		ChannelID:          route.ChannelID,
		AccountID:          route.AccountID,
		RequestedModel:     requestedModel,
		UpstreamModel:      upstreamModelForRecord(route, model),
		Endpoint:           endpoint,
		InputTokens:        metering.InputTokens,
		OutputTokens:       metering.OutputTokens,
		ImageCount:         metering.ImageCount,
		AudioSeconds:       metering.AudioSeconds,
		RequestCount:       metering.RequestCount,
		EstimatedCost:      reserveAmount,
		ActualCost:         actualCost,
		Pricing:            model,
		Status:             "success",
		UpstreamStatus:     upstreamStatus,
		DurationMS:         durationMS,
		UsageSource:        metering.UsageSource,
		MeteringMetadata:   usageMetadataWithClientStatusMapping(route, upstreamStatus, metering.Metadata),
		EffectivePolicy:    policy,
		RiskDecision:       risk,
	})
	_, _ = s.db.ExecContext(context.Background(), "UPDATE api_keys SET last_used_at = now() WHERE id = $1", auth.APIKeyID)
	_ = s.completeIdempotency(context.Background(), auth, requestID, "success")

	copyRouteResponse(w, route, upstreamStatus, upstreamHeader, upstreamBody)
}

func (s *Server) northboundClaudeCodeProxy(w http.ResponseWriter, r *http.Request) {
	if !isClaudeCodeOfficialSidecarPath(r.URL.Path) {
		writeError(w, r, notFound("Claude Code endpoint was not found."))
		return
	}
	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !strings.EqualFold(auth.RoutingMode, "byo") {
		writeError(w, r, forbidden("Claude Code account endpoints require BYO routing."))
		return
	}
	body, err := readNorthboundRequestBody(r.Body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	_ = r.Body.Close()
	body, err = decodeNorthboundRequestBody(body, r.Header.Get("Content-Encoding"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r.Header.Del("Content-Encoding")

	requestID := requestIDFromContext(r.Context())
	fingerprint := requestFingerprint(r.Method, r.URL.RequestURI(), body)
	if err := s.claimIdempotency(r.Context(), auth, requestID, fingerprint); err != nil {
		writeError(w, r, err)
		return
	}

	route, upstreamStatus, upstreamHeader, upstreamBody, durationMS, err := s.callClaudeCodeSidecarWithRetry(r, body, auth)
	if err != nil {
		statusCode := upstreamFailureCode(err)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			ProviderType:       route.ProviderType,
			RequestedModel:     "claude-code",
			UpstreamModel:      "claude-code",
			Endpoint:           claudeCodeSidecarEndpoint(r.URL.Path),
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       err.Error(),
			DurationMS:         durationMS,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "failed")
		writeError(w, r, upstreamUnavailable(statusCode, upstreamFailureMessage(err)))
		return
	}
	upstreamHeader, upstreamBody = applyRouteResponseRewrite(r, route, upstreamHeader, upstreamBody)

	status := "success"
	errorCodeValue := ""
	errorMessage := ""
	if upstreamStatus < 200 || upstreamStatus >= 300 {
		status = "failed"
		errorCodeValue = upstreamStatusErrorCode(upstreamStatus, upstreamBody)
		errorMessage = string(bytes.TrimSpace(upstreamBody))
	}
	_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
		RequestID:          requestID,
		RequestFingerprint: fingerprint,
		UserID:             auth.UserID,
		APIKeyID:           auth.APIKeyID,
		ChannelID:          route.ChannelID,
		AccountID:          route.AccountID,
		ProviderType:       route.ProviderType,
		RequestedModel:     "claude-code",
		UpstreamModel:      "claude-code",
		Endpoint:           claudeCodeSidecarEndpoint(r.URL.Path),
		RequestCount:       1,
		Status:             status,
		ErrorCode:          errorCodeValue,
		ErrorMessage:       errorMessage,
		UpstreamStatus:     upstreamStatus,
		DurationMS:         durationMS,
		UsageSource:        "sidecar_passthrough",
		MeteringMetadata:   usageMetadataWithClientStatusMapping(route, upstreamStatus, map[string]any{"path": r.URL.Path}),
	})
	_, _ = s.db.ExecContext(context.Background(), "UPDATE api_keys SET last_used_at = now() WHERE id = $1", auth.APIKeyID)
	_ = s.completeIdempotency(context.Background(), auth, requestID, status)
	copyRouteResponse(w, route, upstreamStatus, upstreamHeader, upstreamBody)
}

func (s *Server) northboundClaudeCodeWebSocket(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		writeError(w, r, badRequest("WebSocket upgrade is required."))
		return
	}
	if !isClaudeSessionsSubscribeWebSocketPath(r.URL.Path) {
		writeError(w, r, notFound("Claude Code endpoint was not found."))
		return
	}
	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !strings.EqualFold(auth.RoutingMode, "byo") {
		writeError(w, r, forbidden("Claude Code account endpoints require BYO routing."))
		return
	}

	requestID := requestIDFromContext(r.Context())
	fingerprint := requestFingerprint(r.Method, r.URL.RequestURI(), nil)
	if err := s.claimIdempotency(r.Context(), auth, requestID, fingerprint); err != nil {
		writeError(w, r, err)
		return
	}

	reqCtx := northboundRequestContext{
		RequestID:   requestID,
		Fingerprint: fingerprint,
		Auth:        auth,
		Requested:   "claude-code",
		Endpoint:    claudeCodeSidecarEndpoint(r.URL.Path),
		Reserve:     0,
	}
	route, err := s.checkoutClaudeCodeSidecarRoute(r.Context(), auth)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     "claude-code",
			UpstreamModel:      "claude-code",
			Endpoint:           reqCtx.Endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	s.proxyClaudeCodeWebSocket(w, r, route, reqCtx)
}

func (s *Server) northboundRealtimeWebSocket(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		writeError(w, r, badRequest("WebSocket upgrade is required."))
		return
	}

	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	endpoint := "realtime"
	requestedModel := strings.TrimSpace(r.URL.Query().Get("model"))
	if requestedModel == "" {
		writeError(w, r, badRequest("model is required."))
		return
	}

	affinityMatch := s.routeAffinityMatchFromRequest(r.Context(), r, nil, "", requestedModel, endpoint)
	routeAffinityKey := affinityMatch.Key
	routeAffinityTTL := affinityMatch.TTLSeconds
	routeAffinityRuleName := affinityMatch.RuleName
	routeAffinitySkipRetry := affinityMatch.SkipRetryOnFailure
	if routeAffinityKey == "" {
		routeAffinityKey = routeAffinityKeyFromRequest(r, nil, "")
	}
	routeTags := routeTagsFromRequest(r, nil, "")
	requestID := requestIDFromContext(r.Context())
	fingerprint := requestFingerprint(r.Method, r.URL.RequestURI(), nil)
	if err := s.claimIdempotency(r.Context(), auth, requestID, fingerprint); err != nil {
		writeError(w, r, err)
		return
	}
	risk, err := s.evaluateRisk(r.Context(), auth, r, requestID, endpoint, nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := risk.Err(); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			Endpoint:           endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	model, err := s.resolveModel(r.Context(), requestedModel, endpoint)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			Endpoint:           endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	if err := validateModelScope(auth.ModelScopeJSON, requestedModel, model.ModelName, model.Aliases...); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	policy, err := s.resolveEffectivePolicy(r.Context(), auth.UserID, model.ModelName, endpoint)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := policy.enforceModelAccess(); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	reserveAmount := applyBillingMultiplier(estimateReserve(model, nil, "", endpoint), policy)
	if auth.RoutingMode == "byo" {
		reserveAmount = 0
	}
	if err := s.enforceSpendLimits(r.Context(), auth, reserveAmount); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	if err := s.enforceEffectivePolicyLimits(r.Context(), auth, policy, reserveAmount, model.ModelName, endpoint); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}
	reservation, err := s.reserveWallet(r.Context(), auth.UserID, reserveAmount, requestID)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	reqCtx := northboundRequestContext{
		RequestID:              requestID,
		Fingerprint:            fingerprint,
		Auth:                   auth,
		Model:                  model,
		Policy:                 policy,
		Risk:                   risk,
		Requested:              requestedModel,
		Endpoint:               endpoint,
		Reserve:                reserveAmount,
		RouteAffinityKey:       routeAffinityKey,
		RouteAffinityTTL:       routeAffinityTTL,
		RouteAffinityRuleName:  routeAffinityRuleName,
		RouteAffinitySkipRetry: routeAffinitySkipRetry,
		RouteTags:              routeTags,
	}
	route, err := s.checkoutRouteAttempt(r.Context(), model.ModelName, endpoint, auth.UserID, auth.APIKeyID, auth.RoutingMode, routeAffinityKey, routeAffinityTTL, routeAffinityRuleName, routeTags, 0)
	if err != nil {
		_ = s.releaseReservation(context.Background(), reservation, "no_available_account", "")
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		writeError(w, r, err)
		return
	}

	s.proxyRealtimeWebSocket(w, r, route, reqCtx, reservation)
}

func (s *Server) northboundResponsesWebSocket(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		writeError(w, r, badRequest("WebSocket upgrade is required."))
		return
	}

	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	endpoint := "responses"
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(_ *http.Request) bool { return true },
		EnableCompression: true,
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer clientConn.Close()

	messageType, firstPayload, err := clientConn.ReadMessage()
	if err != nil {
		return
	}
	if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
		_ = closeWebSocketWithError(clientConn, websocket.CloseUnsupportedData, "response.create message is required.")
		return
	}

	firstFrame := map[string]any{}
	if err := json.Unmarshal(firstPayload, &firstFrame); err != nil {
		_ = closeWebSocketWithError(clientConn, websocket.CloseInvalidFramePayloadData, "Invalid response.create payload.")
		return
	}
	if metadataText(firstFrame["type"]) != "response.create" {
		_ = closeWebSocketWithError(clientConn, websocket.CloseUnsupportedData, "First websocket frame must be response.create.")
		return
	}
	requestedModel := strings.TrimSpace(metadataText(firstFrame["model"]))
	if requestedModel == "" {
		_ = closeWebSocketWithError(clientConn, websocket.ClosePolicyViolation, "model is required.")
		return
	}

	affinityMatch := s.routeAffinityMatchFromRequest(r.Context(), r, firstPayload, "application/json", requestedModel, endpoint)
	routeAffinityKey := affinityMatch.Key
	routeAffinityTTL := affinityMatch.TTLSeconds
	routeAffinityRuleName := affinityMatch.RuleName
	routeAffinitySkipRetry := affinityMatch.SkipRetryOnFailure
	if routeAffinityKey == "" {
		routeAffinityKey = routeAffinityKeyFromRequest(r, firstPayload, "application/json")
	}
	routeTags := routeTagsFromRequest(r, firstPayload, "application/json")
	requestID := requestIDFromContext(r.Context())
	fingerprint := requestFingerprint(r.Method, r.URL.RequestURI(), firstPayload)
	if err := s.claimIdempotency(r.Context(), auth, requestID, fingerprint); err != nil {
		_ = closeWebSocketWithError(clientConn, websocket.ClosePolicyViolation, err.Error())
		return
	}
	risk, err := s.evaluateRisk(r.Context(), auth, r, requestID, endpoint, firstPayload)
	if err != nil {
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}
	if err := risk.Err(); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			Endpoint:           endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}

	model, err := s.resolveModel(r.Context(), requestedModel, endpoint)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			Endpoint:           endpoint,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.ClosePolicyViolation, err.Error())
		return
	}
	firstPayload = applyModelRequestParams(firstPayload, "application/json", model.RequestParams)
	if err := validateModelScope(auth.ModelScopeJSON, requestedModel, model.ModelName, model.Aliases...); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.ClosePolicyViolation, err.Error())
		return
	}
	policy, err := s.resolveEffectivePolicy(r.Context(), auth.UserID, model.ModelName, endpoint)
	if err != nil {
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}
	if err := policy.enforceModelAccess(); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.ClosePolicyViolation, err.Error())
		return
	}

	reserveAmount := applyBillingMultiplier(estimateReserve(model, firstPayload, "application/json", endpoint), policy)
	if auth.RoutingMode == "byo" {
		reserveAmount = 0
	}
	if err := s.enforceSpendLimits(r.Context(), auth, reserveAmount); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}
	if err := s.enforceEffectivePolicyLimits(r.Context(), auth, policy, reserveAmount, model.ModelName, endpoint); err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
			EffectivePolicy:    policy,
			RiskDecision:       risk,
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}
	reservation, err := s.reserveWallet(r.Context(), auth.UserID, reserveAmount, requestID)
	if err != nil {
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}

	reqCtx := northboundRequestContext{
		RequestID:              requestID,
		Fingerprint:            fingerprint,
		Auth:                   auth,
		Model:                  model,
		Policy:                 policy,
		Risk:                   risk,
		Requested:              requestedModel,
		Endpoint:               endpoint,
		Reserve:                reserveAmount,
		RouteAffinityKey:       routeAffinityKey,
		RouteAffinityTTL:       routeAffinityTTL,
		RouteAffinityRuleName:  routeAffinityRuleName,
		RouteAffinitySkipRetry: routeAffinitySkipRetry,
		RouteTags:              routeTags,
	}
	route, err := s.checkoutRouteAttempt(r.Context(), model.ModelName, endpoint, auth.UserID, auth.APIKeyID, auth.RoutingMode, routeAffinityKey, routeAffinityTTL, routeAffinityRuleName, routeTags, 0)
	if err != nil {
		_ = s.releaseReservation(context.Background(), reservation, "no_available_account", "")
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          requestID,
			RequestFingerprint: fingerprint,
			UserID:             auth.UserID,
			APIKeyID:           auth.APIKeyID,
			RequestedModel:     requestedModel,
			UpstreamModel:      model.ModelName,
			Endpoint:           endpoint,
			EstimatedCost:      reserveAmount,
			Pricing:            model,
			Status:             "rejected",
			ErrorCode:          errorCode(err),
			ErrorMessage:       err.Error(),
		})
		_ = s.completeIdempotency(context.Background(), auth, requestID, "rejected")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}

	attempt, err := s.openWebSocketWithRetry(r, route, reqCtx.Model.ModelName, reqCtx.Endpoint, reqCtx.Auth.UserID, reqCtx.Auth.APIKeyID, reqCtx.Auth.RoutingMode, reqCtx.RouteAffinityKey, reqCtx.RouteAffinityTTL, reqCtx.RouteAffinityRuleName, reqCtx.RouteAffinitySkipRetry, reqCtx.RouteTags)
	route = attempt.Route
	if err != nil {
		statusCode := errorCode(err)
		if statusCode == "internal_error" {
			statusCode = upstreamFailureCode(err)
		}
		_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			EstimatedCost:      reqCtx.Reserve,
			Pricing:            reqCtx.Model,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       err.Error(),
			DurationMS:         attempt.DurationMS,
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, "Upstream WebSocket request failed.")
		return
	}

	upstreamConn := attempt.UpstreamConn
	if upstreamConn == nil {
		respBody := attempt.Body
		if len(respBody) == 0 {
			respBody = []byte(`{"error":{"message":"upstream rejected websocket request"}}`)
		}
		statusCode := upstreamStatusErrorCode(attempt.Status, respBody)
		_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
		_ = s.completeRoute(context.Background(), route, !isRetryableUpstreamStatusForRoute(route, attempt.Status), attempt.Status, string(bytes.TrimSpace(respBody)))
		s.applyUpstreamHeaderSignals(context.Background(), route, attempt.Status, attempt.Header)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			EstimatedCost:      reqCtx.Reserve,
			Pricing:            reqCtx.Model,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       string(bytes.TrimSpace(respBody)),
			UpstreamStatus:     attempt.Status,
			DurationMS:         attempt.DurationMS,
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, string(bytes.TrimSpace(respBody)))
		return
	}
	defer upstreamConn.Close()

	adapter, adapterErr := providerAdapterFor(route.ProviderType)
	if adapterErr != nil {
		adapter = openaiCompatibleAdapter{}
	}
	wsStats := &webSocketMeteringStats{}
	firstPayload = rewriteCodexOfficialWebSocketFrame(r, route, firstPayload)
	wsStats.recordFrame(firstPayload, adapter, reqCtx.Endpoint, false)
	if err := upstreamConn.WriteMessage(messageType, firstPayload); err != nil {
		_ = s.releaseReservation(context.Background(), reservation, "upstream_error", route.AccountID)
		_ = s.completeRoute(context.Background(), route, false, http.StatusSwitchingProtocols, err.Error())
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			EstimatedCost:      reqCtx.Reserve,
			Pricing:            reqCtx.Model,
			Status:             "failed",
			ErrorCode:          "upstream_websocket_send_failed",
			ErrorMessage:       err.Error(),
			DurationMS:         attempt.DurationMS,
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, err.Error())
		return
	}

	startedAt := time.Now()
	_ = s.completeRoute(context.Background(), route, true, http.StatusSwitchingProtocols, "")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = copyWebSocketMessages(clientConn, upstreamConn, adapter, reqCtx.Endpoint, wsStats, false, r, route)
		_ = upstreamConn.Close()
	}()
	go func() {
		defer wg.Done()
		if err := copyWebSocketMessages(upstreamConn, clientConn, adapter, reqCtx.Endpoint, wsStats, true, r, route); shouldCloseWebSocketWithUpstreamError(err) {
			_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, "Upstream WebSocket interrupted.")
		}
		_ = clientConn.Close()
	}()
	wg.Wait()

	fallbackMetrics := meteringMetrics{RequestCount: 1}
	wsMetering := wsStats.meteringResult(fallbackMetrics)
	actualCost := applyBillingMultiplier(calculateActualCost(reqCtx.Model, wsMetering.usageCounts(), wsMetering.meteringMetrics()), reqCtx.Policy)
	if !wsStats.hasProviderUsage() || actualCost <= 0 {
		actualCost = reqCtx.Reserve
		if actualCost <= 0 {
			actualCost = applyBillingMultiplier(calculateActualCost(reqCtx.Model, usageCounts{}, fallbackMetrics), reqCtx.Policy)
		}
	}
	actualCost = actualCostForRoutingMode(reqCtx.Auth.RoutingMode, actualCost)
	if settleErr := s.settleReservation(context.Background(), reservation, actualCost, route.AccountID, reqCtx.Policy); settleErr != nil {
		slog.Error("websocket settlement failed", "error", settleErr, "request_id", reqCtx.RequestID)
	}
	_ = s.completeRoute(context.Background(), route, true, http.StatusSwitchingProtocols, "")
	s.applyUpstreamHeaderSignals(context.Background(), route, http.StatusSwitchingProtocols, attempt.Header)
	durationMS := int(time.Since(startedAt).Milliseconds())
	_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
		RequestID:           reqCtx.RequestID,
		RequestFingerprint:  reqCtx.Fingerprint,
		UserID:              reqCtx.Auth.UserID,
		APIKeyID:            reqCtx.Auth.APIKeyID,
		ChannelID:           route.ChannelID,
		AccountID:           route.AccountID,
		RequestedModel:      reqCtx.Requested,
		UpstreamModel:       upstreamModelForRecord(route, reqCtx.Model),
		Endpoint:            reqCtx.Endpoint,
		InputTokens:         wsMetering.InputTokens,
		OutputTokens:        wsMetering.OutputTokens,
		RequestCount:        wsMetering.RequestCount,
		EstimatedCost:       reqCtx.Reserve,
		ActualCost:          actualCost,
		Pricing:             reqCtx.Model,
		Status:              "success",
		UpstreamStatus:      http.StatusSwitchingProtocols,
		DurationMS:          durationMS,
		UsageSource:         wsMetering.UsageSource,
		WebSocketFrameCount: wsMetering.WebSocketFrameCount,
		MeteringMetadata:    wsMetering.Metadata,
		EffectivePolicy:     reqCtx.Policy,
		RiskDecision:        reqCtx.Risk,
	})
	_, _ = s.db.ExecContext(context.Background(), "UPDATE api_keys SET last_used_at = now() WHERE id = $1", reqCtx.Auth.APIKeyID)
	_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "success")
}

func requestFingerprint(method string, path string, body []byte) string {
	sum := sha256.Sum256(append([]byte(method+" "+path+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

const maxNorthboundBodyBytes int64 = 64 << 20

func readNorthboundRequestBody(reader io.Reader) ([]byte, error) {
	return readLimitedRequestBody(reader, maxNorthboundBodyBytes)
}

func readLimitedRequestBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = maxNorthboundBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, badRequest("Could not read request body.")
	}
	if int64(len(body)) > maxBytes {
		return nil, requestEntityTooLarge("Request body is too large.")
	}
	return body, nil
}

func decodeNorthboundRequestBody(body []byte, contentEncoding string) ([]byte, error) {
	encodings := contentEncodingTokens(contentEncoding)
	if len(encodings) == 0 {
		return body, nil
	}
	reader, closer, err := contentEncodedReader(bytes.NewReader(body), encodings, "request")
	if err != nil {
		if json.Valid(bytes.TrimSpace(body)) {
			return body, nil
		}
		return nil, err
	}
	if closer != nil {
		defer closer.Close()
	}
	decoded, err := readDecodedNorthboundBody(reader)
	if err != nil && json.Valid(bytes.TrimSpace(body)) {
		return body, nil
	}
	return decoded, err
}

func contentEncodingTokens(contentEncoding string) []string {
	parts := strings.Split(strings.ToLower(contentEncoding), ",")
	encodings := make([]string, 0, len(parts))
	for _, part := range parts {
		encoding := strings.TrimSpace(part)
		if encoding == "" || encoding == "identity" {
			continue
		}
		encodings = append(encodings, encoding)
	}
	return encodings
}

func readDecodedNorthboundBody(reader io.Reader) ([]byte, error) {
	decoded, err := io.ReadAll(io.LimitReader(reader, maxNorthboundBodyBytes+1))
	if err != nil {
		return nil, badRequest("Could not decode request body.")
	}
	if int64(len(decoded)) > maxNorthboundBodyBytes {
		return nil, requestEntityTooLarge("Request body is too large.")
	}
	return decoded, nil
}

var routeAffinityHeaderCandidates = []string{
	"X-Elucid-Relay-Session",
	"X-Relay-Session",
	"X-Client-Request-Id",
	"X-Session-ID",
	"Session_id",
	"X-Conversation-ID",
	"X-Amp-Thread-Id",
	"X-Codex-Window-ID",
	"X-Codex-Turn-State",
	"X-Codex-Parent-Thread-ID",
	"X-Codex-Session-ID",
	"X-Claude-Session-ID",
	"X-Claude-Code-Session-Id",
	"X-Gemini-Session-ID",
	"X-Gemini-User-Prompt-Id",
	"X-Antigravity-Session-Id",
	"X-Antigravity-Request-Id",
	"X-Kiro-Conversation-Id",
	"X-Codeium-Session-Id",
	"X-Windsurf-Session-Id",
	"X-GitHub-Copilot-Session-Id",
	"X-Interaction-Id",
	"X-Agent-Task-Id",
	"OpenAI-Conversation-ID",
	"Anthropic-Conversation-ID",
	"Google-Conversation-ID",
}

var routeAffinityJSONFields = map[string]struct{}{
	"session_id":           {},
	"conversation_id":      {},
	"thread_id":            {},
	"chat_id":              {},
	"prompt_cache_key":     {},
	"previous_response_id": {},
	"user_prompt_id":       {},
	"prompt_id":            {},
}

func routeAffinityKeyFromRequest(r *http.Request, body []byte, contentType string) string {
	for _, header := range routeAffinityHeaderCandidates {
		if value := normalizeRouteAffinityKey(r.Header.Get(header)); value != "" {
			return value
		}
	}
	for _, key := range []string{"session_id", "conversation_id", "thread_id"} {
		if value := normalizeRouteAffinityKey(r.URL.Query().Get(key)); value != "" {
			return value
		}
	}
	return routeAffinityKeyFromJSON(body, contentType)
}

func routeAffinityKeyFromJSON(body []byte, contentType string) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return ""
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if key := findRouteAffinityJSONKey(payload, 0); key != "" {
		return key
	}
	return responseRouteAffinityDigest(payload)
}

func findRouteAffinityJSONKey(value any, depth int) string {
	if depth > 16 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, ok := routeAffinityJSONFields[strings.ToLower(key)]; ok {
				if text, ok := child.(string); ok {
					if normalized := normalizeRouteAffinityKey(text); normalized != "" {
						return normalized
					}
				}
			}
		}
		for _, child := range typed {
			if key := findRouteAffinityJSONKey(child, depth+1); key != "" {
				return key
			}
		}
	case []any:
		for _, child := range typed {
			if key := findRouteAffinityJSONKey(child, depth+1); key != "" {
				return key
			}
		}
	}
	return ""
}

func responseRouteAffinityDigest(value any) string {
	payload, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	parts := make([]any, 0, 2)
	if instructions, ok := payload["instructions"]; ok {
		parts = append(parts, map[string]any{"instructions": instructions})
	}
	if input, ok := payload["input"]; ok {
		parts = append(parts, map[string]any{"input": input})
	}
	if len(parts) == 0 {
		return ""
	}
	canonical, err := json.Marshal(parts)
	if err != nil || len(canonical) == 0 || bytes.Equal(bytes.TrimSpace(canonical), []byte("null")) {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "responses:" + hex.EncodeToString(sum[:16])
}

func normalizeRouteAffinityKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return ""
	}
	for _, ch := range value {
		if ch < 32 || ch == 127 {
			return ""
		}
	}
	return value
}

func routeAffinityHash(value string) string {
	value = normalizeRouteAffinityKey(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var routeTagHeaderCandidates = []string{
	"X-Elucid-Relay-Route-Tag",
	"X-Elucid-Relay-Route-Tags",
	"X-Relay-Route-Tag",
	"X-Relay-Route-Tags",
	"X-Elucid-Relay-Route-Profile",
	"X-Relay-Route-Profile",
}

var routeTagJSONFields = map[string]struct{}{
	"route_tag":       {},
	"route_tags":      {},
	"routing_tag":     {},
	"routing_tags":    {},
	"route_profile":   {},
	"route_profiles":  {},
	"routing_profile": {},
}

func routeTagsFromRequest(r *http.Request, body []byte, contentType string) []string {
	tags := []string{}
	for _, header := range routeTagHeaderCandidates {
		tags = appendRouteTags(tags, r.Header.Get(header))
	}
	for _, key := range []string{"route_tag", "route_tags", "route_profile", "route_profiles"} {
		tags = appendRouteTags(tags, r.URL.Query().Get(key))
	}
	tags = appendRouteTagsFromJSON(tags, body, contentType)
	return tags
}

func appendRouteTagsFromJSON(tags []string, body []byte, contentType string) []string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return tags
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return tags
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return tags
	}
	tags = appendRouteTagsFromObject(tags, payload)
	if metadata, ok := objectValue(payload["metadata"]); ok {
		tags = appendRouteTagsFromObject(tags, metadata)
	}
	return tags
}

func appendRouteTagsFromObject(tags []string, payload map[string]any) []string {
	for key, value := range payload {
		if _, ok := routeTagJSONFields[strings.ToLower(key)]; ok {
			tags = appendRouteTags(tags, value)
		}
	}
	return tags
}

func appendRouteTags(tags []string, value any) []string {
	if len(tags) >= 4 {
		return tags
	}
	switch typed := value.(type) {
	case string:
		for _, part := range strings.Split(typed, ",") {
			if tag := normalizeRouteTag(part); tag != "" && !stringInSlice(tags, tag) {
				tags = append(tags, tag)
				if len(tags) >= 4 {
					return tags
				}
			}
		}
	case []any:
		for _, item := range typed {
			tags = appendRouteTags(tags, item)
			if len(tags) >= 4 {
				return tags
			}
		}
	case []string:
		for _, item := range typed {
			tags = appendRouteTags(tags, item)
			if len(tags) >= 4 {
				return tags
			}
		}
	}
	return tags
}

func normalizeRouteTag(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return ""
	}
	return value
}

func stringInSlice(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func endpointFromPath(path string) string {
	switch {
	case path == "/v1/chat/completions":
		return "chat"
	case path == "/v1/responses":
		return "responses"
	case path == "/v1/messages":
		return "messages"
	case path == "/v1/messages/count_tokens":
		return "messages"
	case path == "/v1/embeddings":
		return "embeddings"
	case strings.HasPrefix(path, "/v1/images/"):
		return "images"
	case strings.HasPrefix(path, "/v1/audio/"):
		return "audio"
	case path == "/v1/realtime/sessions":
		return "realtime"
	case path == "/v1/rerank":
		return "rerank"
	default:
		return "unknown"
	}
}

func claudeCodeSidecarEndpoint(path string) string {
	switch {
	case isClaudeFilesPath(path):
		return "claude_files"
	case isClaudeMCPServersPath(path):
		return "claude_mcp_servers"
	case isClaudeSessionsSubscribeWebSocketPath(path):
		return "claude_sessions_ws"
	case isClaudeSessionsPath(path):
		return "claude_sessions"
	case isClaudeCodeSessionsPath(path):
		return "claude_code_sessions"
	case isClaudeSessionIngressPath(path):
		return "claude_session_ingress"
	case isClaudeEnvironmentsPath(path):
		return "claude_environments"
	case isClaudeEnvironmentProvidersPath(path):
		return "claude_environment_providers"
	case isClaudeOAuthAPIPath(path):
		return "claude_oauth"
	default:
		return "claude_sidecar"
	}
}

func modelFromBody(body []byte, contentType string) (string, error) {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") {
		return modelFromMultipart(body, params["boundary"])
	}
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || mediaType == "" {
		payload := map[string]any{}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &payload); err != nil {
				return "", badRequest("Invalid JSON request body.")
			}
		}
		return modelFromPayload(payload), nil
	}
	return "", badRequest("Unsupported request content type.")
}

func modelFromPayload(payload map[string]any) string {
	if value, ok := payload["model"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func modelFromMultipart(body []byte, boundary string) (string, error) {
	if boundary == "" {
		return "", badRequest("Multipart boundary is required.")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", badRequest("Invalid multipart request body.")
		}
		if part.FormName() != "model" {
			_ = part.Close()
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, 1024))
		_ = part.Close()
		if err != nil {
			return "", badRequest("Invalid multipart model field.")
		}
		return strings.TrimSpace(string(value)), nil
	}
	return "", nil
}

func isStreamingRequest(body []byte, contentType string, accept string) bool {
	if strings.Contains(strings.ToLower(accept), "text/event-stream") {
		return true
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	stream, _ := payload["stream"].(bool)
	return stream
}

var (
	openAIResponseIDPattern = regexp.MustCompile(`^resp_[A-Za-z0-9_-]{1,256}$`)
	openAIMessageIDPattern  = regexp.MustCompile(`^(msg|message|item|chatcmpl)_[A-Za-z0-9_-]{1,256}$`)
)

func validateNorthboundClientRequest(endpoint string, body []byte, contentType string) error {
	payload, ok, err := decodeJSONPayloadForClientValidation(body, contentType)
	if err != nil || !ok {
		return err
	}
	if streamValue, exists := payload["stream"]; exists {
		if _, ok := streamValue.(bool); !ok {
			return badRequest("stream must be a boolean.")
		}
	}
	if endpoint == "responses" {
		return validateResponsesClientRequest(payload)
	}
	return nil
}

func decodeJSONPayloadForClientValidation(body []byte, contentType string) (map[string]any, bool, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return nil, false, nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, true, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	payload := map[string]any{}
	if err := decoder.Decode(&payload); err != nil {
		return nil, true, badRequest("Invalid JSON request body.")
	}
	return payload, true, nil
}

func validateResponsesClientRequest(payload map[string]any) error {
	if payload == nil {
		return nil
	}
	hasPreviousResponseID := false
	if previous, exists := payload["previous_response_id"]; exists {
		if err := validateOpenAIPreviousResponseID(previous); err != nil {
			return err
		}
		hasPreviousResponseID = strings.TrimSpace(metadataText(previous)) != ""
	}
	if input, exists := payload["input"]; exists {
		if err := validateResponsesFunctionCallOutputs(input, hasPreviousResponseID); err != nil {
			return err
		}
	}
	return nil
}

func validateOpenAIPreviousResponseID(value any) error {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return badRequest("previous_response_id must be a string.")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return badRequest("previous_response_id cannot be empty.")
	}
	lower := strings.ToLower(text)
	if openAIMessageIDPattern.MatchString(lower) {
		return badRequest("previous_response_id must be a response id, not a message id.")
	}
	if strings.HasPrefix(lower, "resp_") && !openAIResponseIDPattern.MatchString(text) {
		return badRequest("previous_response_id is invalid.")
	}
	return nil
}

func validateResponsesFunctionCallOutputs(value any, hasPreviousResponseID bool) error {
	context := responsesFunctionCallOutputContext{
		callIDs:       map[string]struct{}{},
		itemReference: map[string]struct{}{},
	}
	collectResponsesFunctionCallOutputContext(value, &context)
	if !context.hasFunctionCallOutput {
		return nil
	}
	if context.missingCallID {
		return badRequest("function_call_output items require call_id.")
	}
	if context.missingOutput {
		return badRequest("function_call_output items require output.")
	}
	if hasPreviousResponseID || context.hasToolCallContext {
		return nil
	}
	for callID := range context.callIDs {
		if _, ok := context.itemReference[callID]; !ok {
			return badRequest("function_call_output items require matching item_reference when previous_response_id is absent.")
		}
	}
	return nil
}

type responsesFunctionCallOutputContext struct {
	hasFunctionCallOutput bool
	missingCallID         bool
	missingOutput         bool
	hasToolCallContext    bool
	callIDs               map[string]struct{}
	itemReference         map[string]struct{}
}

func collectResponsesFunctionCallOutputContext(value any, context *responsesFunctionCallOutputContext) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectResponsesFunctionCallOutputContext(item, context)
		}
	case map[string]any:
		itemType := strings.ToLower(strings.TrimSpace(metadataText(typed["type"])))
		switch itemType {
		case "function_call_output":
			context.hasFunctionCallOutput = true
			callID := strings.TrimSpace(metadataText(typed["call_id"]))
			if callID == "" {
				context.missingCallID = true
			} else {
				context.callIDs[callID] = struct{}{}
			}
			if _, exists := typed["output"]; !exists {
				context.missingOutput = true
			}
			return
		case "item_reference":
			if id := strings.TrimSpace(metadataText(typed["id"])); id != "" {
				context.itemReference[id] = struct{}{}
			}
		case "function_call", "tool_call":
			if id := strings.TrimSpace(firstNonEmpty(metadataText(typed["call_id"]), metadataText(typed["id"]))); id != "" {
				context.hasToolCallContext = true
			}
		}
		for _, child := range typed {
			collectResponsesFunctionCallOutputContext(child, context)
		}
	}
}

func (s *Server) resolveModel(ctx context.Context, requested string, endpoint string) (modelInfo, error) {
	info, err := s.resolveModelExact(ctx, requested, endpoint)
	if err == nil {
		return info, nil
	}
	baseModel, params := modelRequestParamsFromSuffix(requested)
	if len(params) > 0 && baseModel != requested {
		fallback, fallbackErr := s.resolveModelExact(ctx, baseModel, endpoint)
		if fallbackErr == nil {
			fallback.RequestParams = params
			fallback.Aliases = append(fallback.Aliases, requested)
			return fallback, nil
		}
	}
	return info, err
}

func (s *Server) resolveModelExact(ctx context.Context, requested string, endpoint string) (modelInfo, error) {
	var info modelInfo
	var inputPrice, outputPrice, requestUSD, minCharge, cacheRead, cacheWrite, imagePrice, audioPrice, aliases string
	err := s.db.QueryRowContext(ctx, `
		WITH resolved AS (
			SELECT model_name FROM model_catalog WHERE model_name = $1
			UNION
			SELECT model_name FROM model_aliases WHERE alias = $1
			LIMIT 1
		)
		SELECT m.model_name, m.input_usd_per_1k::text, m.output_usd_per_1k::text, m.request_usd::text, m.min_charge_usd::text,
		       COALESCE(mpo.billing_mode, 'standard'), COALESCE(mpo.billing_expr, ''),
		       COALESCE(mpo.cache_read_usd_per_1k::text, '0'), COALESCE(mpo.cache_write_usd_per_1k::text, '0'),
		       COALESCE(mpo.image_usd_per_unit::text, '0'), COALESCE(mpo.audio_usd_per_second::text, '0'),
		       COALESCE((SELECT jsonb_agg(alias ORDER BY alias)::text FROM model_aliases WHERE model_name = m.model_name), '[]')
		FROM model_catalog m
		JOIN resolved r ON r.model_name = m.model_name
		LEFT JOIN model_pricing_overrides mpo ON mpo.model_name = m.model_name
		WHERE m.status = 'active'
		  AND m.endpoint_capabilities ? $2
		`, requested, endpoint).Scan(
		&info.ModelName, &inputPrice, &outputPrice, &requestUSD, &minCharge,
		&info.BillingMode, &info.BillingExpr, &cacheRead, &cacheWrite, &imagePrice, &audioPrice, &aliases,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return modelInfo{}, appError{status: http.StatusBadRequest, code: "unsupported_model_capability", message: "Model does not support this endpoint.", typ: "invalid_request_error"}
	}
	if err != nil {
		return modelInfo{}, err
	}
	if err := json.Unmarshal([]byte(aliases), &info.Aliases); err != nil {
		return modelInfo{}, err
	}

	info.InputUSDPer1K, _ = strconv.ParseFloat(inputPrice, 64)
	info.OutputUSDPer1K, _ = strconv.ParseFloat(outputPrice, 64)
	info.RequestUSD, _ = strconv.ParseFloat(requestUSD, 64)
	info.MinChargeUSD, _ = strconv.ParseFloat(minCharge, 64)
	info.CacheReadUSDPer1K, _ = strconv.ParseFloat(cacheRead, 64)
	info.CacheWriteUSDPer1K, _ = strconv.ParseFloat(cacheWrite, 64)
	info.ImageUSDPerUnit, _ = strconv.ParseFloat(imagePrice, 64)
	info.AudioUSDPerSecond, _ = strconv.ParseFloat(audioPrice, 64)
	info.BillingMode = strings.TrimSpace(info.BillingMode)
	if info.BillingMode == "" {
		info.BillingMode = "standard"
	}
	return info, nil
}

func modelRequestParamsFromSuffix(model string) (string, map[string]any) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return trimmed, nil
	}
	suffixes := []struct {
		Suffix string
		Params map[string]any
	}{
		{Suffix: "-thinking", Params: map[string]any{"thinking": true}},
		{Suffix: "-think", Params: map[string]any{"thinking": true}},
		{Suffix: "-reasoning-high", Params: map[string]any{"reasoning_effort": "high"}},
		{Suffix: "-reasoning-medium", Params: map[string]any{"reasoning_effort": "medium"}},
		{Suffix: "-reasoning-low", Params: map[string]any{"reasoning_effort": "low"}},
		{Suffix: "-high", Params: map[string]any{"reasoning_effort": "high"}},
		{Suffix: "-medium", Params: map[string]any{"reasoning_effort": "medium"}},
		{Suffix: "-low", Params: map[string]any{"reasoning_effort": "low"}},
	}
	lower := strings.ToLower(trimmed)
	for _, suffix := range suffixes {
		if strings.HasSuffix(lower, suffix.Suffix) {
			base := strings.TrimSpace(trimmed[:len(trimmed)-len(suffix.Suffix)])
			if base == "" {
				return trimmed, nil
			}
			params := map[string]any{}
			for key, value := range suffix.Params {
				params[key] = value
			}
			return base, params
		}
	}
	return trimmed, nil
}

func applyModelRequestParams(body []byte, contentType string, params map[string]any) []byte {
	if len(params) == 0 {
		return body
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := false
	for key, value := range params {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = value
		changed = true
	}
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return rewritten
}

func estimateReserve(model modelInfo, body []byte, contentType string, endpoint string) float64 {
	requests := 1
	inputTokens := 0
	outputTokens := 0

	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || mediaType == "" {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if n := positiveIntField(payload, "n"); n > 0 {
				requests = n
			}
			outputTokens = firstPositiveIntField(payload, "max_tokens", "max_completion_tokens", "max_output_tokens")
		}
	}

	if endpoint == "chat" || endpoint == "responses" || endpoint == "messages" || endpoint == "embeddings" || endpoint == "rerank" {
		inputTokens = len(body) / 4
		if inputTokens < 1 && len(bytes.TrimSpace(body)) > 0 {
			inputTokens = 1
		}
		if outputTokens == 0 && endpoint != "embeddings" && endpoint != "rerank" {
			outputTokens = 1024
		}
	}

	metrics := meteringMetrics{InputTokens: inputTokens, OutputTokens: outputTokens, RequestCount: requests}
	if endpoint == "images" {
		metrics.ImageCount = requests
	}
	return calculateActualCost(model, usageCounts{InputTokens: inputTokens, OutputTokens: outputTokens}, metrics)
}

func positiveIntField(payload map[string]any, name string) int {
	switch value := payload[name].(type) {
	case float64:
		if value > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstPositiveIntField(payload map[string]any, names ...string) int {
	for _, name := range names {
		if value := positiveIntField(payload, name); value > 0 {
			return value
		}
	}
	return 0
}

type reservationInfo struct {
	WalletID        string
	Amount          float64
	BalanceAfter    string
	ReservedAfter   string
	ReserveLedgerID string
	RequestID       string
}

func (s *Server) reserveWallet(ctx context.Context, userID string, amount float64, requestID string) (reservationInfo, error) {
	if amount <= 0 {
		return reservationInfo{Amount: 0, RequestID: requestID}, nil
	}
	amountText := strconv.FormatFloat(amount, 'f', 10, 64)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return reservationInfo{}, err
	}
	defer tx.Rollback()

	var walletID, balanceAfter, reservedAfter string
	err = tx.QueryRowContext(ctx, `
		UPDATE wallet_accounts
		SET reserved_balance = reserved_balance + $2::numeric
		WHERE user_id = $1
		  AND status = 'active'
		  AND balance - reserved_balance >= $2::numeric
		RETURNING id::text, balance::text, reserved_balance::text
	`, userID, amountText).Scan(&walletID, &balanceAfter, &reservedAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationInfo{}, billingError("insufficient_balance", "Wallet balance is insufficient.")
	}
	if err != nil {
		return reservationInfo{}, err
	}

	var ledgerID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id)
		VALUES ($1, 'reserve', $2::numeric, $3::numeric, $4::numeric, 'northbound_request', $5)
		RETURNING id::text
	`, walletID, amountText, balanceAfter, reservedAfter, requestID).Scan(&ledgerID); err != nil {
		return reservationInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return reservationInfo{}, err
	}
	return reservationInfo{
		WalletID:        walletID,
		Amount:          amount,
		BalanceAfter:    balanceAfter,
		ReservedAfter:   reservedAfter,
		ReserveLedgerID: ledgerID,
		RequestID:       requestID,
	}, nil
}

func (s *Server) releaseReservation(ctx context.Context, reservation reservationInfo, reason string, accountID string) error {
	if reservation.Amount <= 0 || reservation.WalletID == "" {
		return nil
	}
	amountText := strconv.FormatFloat(reservation.Amount, 'f', 10, 64)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var balanceAfter, reservedAfter string
	if err := tx.QueryRowContext(ctx, `
		UPDATE wallet_accounts
		SET reserved_balance = GREATEST(reserved_balance - $2::numeric, 0)
		WHERE id = $1
		RETURNING balance::text, reserved_balance::text
	`, reservation.WalletID, amountText).Scan(&balanceAfter, &reservedAfter); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id, metadata_json)
		VALUES ($1, 'release', $2::numeric, $3::numeric, $4::numeric, 'northbound_request', $5, $6::jsonb)
	`, reservation.WalletID, amountText, balanceAfter, reservedAfter, reservation.RequestID, `{"reason":"`+reason+`","account_id":"`+accountID+`"}`)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Server) settleReservation(ctx context.Context, reservation reservationInfo, actualCost float64, accountID string, policy effectivePolicy) error {
	if reservation.WalletID == "" {
		return nil
	}
	reservedText := strconv.FormatFloat(reservation.Amount, 'f', 10, 64)
	actualText := strconv.FormatFloat(actualCost, 'f', 10, 64)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var balanceAfter, reservedAfter string
	if err := tx.QueryRowContext(ctx, `
		UPDATE wallet_accounts
		SET balance = balance - $3::numeric,
		    reserved_balance = GREATEST(reserved_balance - $2::numeric, 0)
		WHERE id = $1
		  AND balance - $3::numeric >= GREATEST(reserved_balance - $2::numeric, 0)
		RETURNING balance::text, reserved_balance::text
	`, reservation.WalletID, reservedText, actualText).Scan(&balanceAfter, &reservedAfter); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billingError("insufficient_balance", "Wallet balance became insufficient before settlement.")
		}
		return err
	}
	if actualCost > 0 {
		metadata := map[string]any{
			"account_id":          accountID,
			"billing_multiplier":  policy.BillingMultiplier,
			"effective_policy":    effectivePolicySnapshot(policy),
			"reserve_ledger_id":   reservation.ReserveLedgerID,
			"reserved_amount_usd": reservation.Amount,
			"settled_amount_usd":  actualCost,
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id, metadata_json)
			VALUES ($1, 'debit', $2::numeric, $3::numeric, $4::numeric, 'northbound_request', $5, $6::jsonb)
		`, reservation.WalletID, actualText, balanceAfter, reservedAfter, reservation.RequestID, mustEncodeJSON(metadata)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if actualCost > 0 {
		if err := s.emitLowBalanceNotification(ctx, reservation.WalletID, balanceAfter); err != nil {
			slog.WarnContext(ctx, "low balance notification failed", "wallet_id", reservation.WalletID, "error", err)
		}
	}
	return nil
}

func (s *Server) selectRoute(ctx context.Context, model string, endpoint string) (routeInfo, error) {
	return s.checkoutAnyRoute(ctx, model, endpoint, "", "pool", routeSelectionSeed(ctx, model, endpoint, "pool", "", 0), nil, nil, 0)
}

func (s *Server) checkoutRoute(ctx context.Context, model string, endpoint string, userID string, apiKeyID string, routingMode string, affinityKey string) (routeInfo, error) {
	return s.checkoutRouteAttempt(ctx, model, endpoint, userID, apiKeyID, routingMode, affinityKey, 0, "", nil, 0)
}

func (s *Server) checkoutRouteAttempt(ctx context.Context, model string, endpoint string, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, routeTags []string, attempt int) (routeInfo, error) {
	return s.checkoutRouteAttemptExcluding(ctx, model, endpoint, userID, apiKeyID, routingMode, affinityKey, affinityTTLSeconds, affinityRuleName, routeTags, nil, attempt)
}

func (s *Server) checkoutRouteAttemptExcluding(ctx context.Context, model string, endpoint string, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, routeTags []string, excludedAccountIDs []string, attempt int) (routeInfo, error) {
	routingMode, err := routingModeValue(routingMode)
	if err != nil {
		return routeInfo{}, err
	}
	if len(routeTags) == 0 && len(excludedAccountIDs) == 0 && attempt <= 0 && strings.TrimSpace(affinityKey) != "" && userID != "" && apiKeyID != "" {
		route, ok, err := s.checkoutAffinityRoute(ctx, model, endpoint, userID, apiKeyID, routingMode, affinityKey, affinityTTLSeconds, affinityRuleName)
		if err != nil {
			return routeInfo{}, err
		}
		if ok {
			return s.enrichCodexRoute(ctx, route)
		}
		s.observeRouteAffinityMiss(context.Background(), userID, apiKeyID, model, endpoint, affinityKey, affinityRuleName)
	}

	route, err := s.checkoutAnyRoute(ctx, model, endpoint, userID, routingMode, routeSelectionSeed(ctx, model, endpoint, routingMode, affinityKey, attempt), routeTags, excludedAccountIDs, attempt)
	if err != nil {
		return routeInfo{}, err
	}
	if len(routeTags) == 0 && len(excludedAccountIDs) == 0 && attempt <= 0 && strings.TrimSpace(affinityKey) != "" && userID != "" && apiKeyID != "" {
		if err := s.upsertRouteAffinity(context.Background(), userID, apiKeyID, model, endpoint, affinityKey, affinityTTLSeconds, affinityRuleName, route); err != nil {
			slog.Warn("route affinity upsert failed", "error", err, "account_id", route.AccountID)
		}
	}
	return s.enrichCodexRoute(ctx, route)
}

func routeSelectionSeed(ctx context.Context, model string, endpoint string, routingMode string, affinityKey string, attempt int) string {
	if seed := strings.TrimSpace(affinityKey); seed != "" {
		return seed + ":" + strconv.Itoa(attempt)
	}
	if seed := strings.TrimSpace(requestIDFromContext(ctx)); seed != "" {
		return seed + ":" + strconv.Itoa(attempt)
	}
	return strings.Join([]string{model, endpoint, routingMode, strconv.Itoa(attempt)}, ":")
}

func (s *Server) enrichCodexRoute(ctx context.Context, route routeInfo) (routeInfo, error) {
	if !isCodexOfficialRoute(route) {
		return route, nil
	}
	if routeMetadataBool(route, "codex", "disable_models_sync", "disable_model_metadata_sync") {
		return route, nil
	}
	baseClient, err := s.upstreamHTTPClient(route)
	if err != nil {
		return route, nil
	}
	client := withHTTPClientTimeout(baseClient, 5*time.Second)
	enriched, err := s.codexModels.merge(route, codexClientVersion(route), client)
	if err != nil {
		slog.WarnContext(ctx, "codex models metadata sync failed", "error", err, "account_id", route.AccountID, "model", route.UpstreamModel)
		return route, nil
	}
	return enriched, nil
}

func (s *Server) checkoutAnyRoute(ctx context.Context, model string, endpoint string, userID string, routingMode string, selectionSeed string, routeTags []string, excludedAccountIDs []string, attempt int) (routeInfo, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return routeInfo{}, err
	}
	defer tx.Rollback()

	var route routeInfo
	var ciphertext, nonce []byte
	var routeAuthExpiresAt, routeRefreshDueAt sql.NullTime
	routeTagsJSON := "[]"
	if len(routeTags) > 0 {
		routeTagsJSON = mustEncodeJSON(routeTags)
	}
	excludedAccountIDsJSON := "[]"
	if len(excludedAccountIDs) > 0 {
		excludedAccountIDsJSON = mustEncodeJSON(excludedAccountIDs)
	}
	err = tx.QueryRowContext(ctx, `
		SELECT c.id::text, a.id::text, c.base_url, p.provider_type,
		       COALESCE(NULLIF(ca.upstream_model, ''), ca.model_name), c.timeout_seconds,
		       COALESCE(ap.proxy_url, cp.proxy_url, ''),
		       a.routing_mode, COALESCE(a.owner_user_id::text, ''),
		       COALESCE(aas.auth_mode, ''), COALESCE(aas.auth_status, ''), aas.expires_at, aas.refresh_due_at,
		       ca.transform_capability_json::text, c.metadata_json::text, a.metadata_json::text, ars.metadata_json::text,
		       v.secret_ciphertext, v.secret_nonce
		FROM channel_abilities ca
		JOIN channels c ON c.id = ca.channel_id
		JOIN providers p ON p.id = c.provider_id
		JOIN accounts a ON a.channel_id = c.id AND a.status = 'active'
		JOIN credential_vault_records v ON v.id = a.credential_vault_record_id
		JOIN account_runtime_states ars ON ars.account_id = a.id
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		LEFT JOIN proxies cp ON cp.id = c.proxy_id AND cp.status = 'active'
		LEFT JOIN proxies ap ON ap.id = a.proxy_id AND ap.status = 'active'
		WHERE ca.model_name = $1
		  AND ca.endpoint = $2
		  AND ca.status = 'active'
		  AND c.status = 'active'
		  AND p.status = 'active'
		  AND a.routing_mode = $4
		  AND NOT EXISTS (
		    SELECT 1
		    FROM jsonb_array_elements_text($8::jsonb) excluded_account(id)
		    WHERE excluded_account.id = a.id::text
		  )
		  AND (
		    ($4 = 'pool' AND a.owner_user_id IS NULL)
		    OR ($4 = 'byo' AND a.owner_user_id::text = $3)
		  )
		  AND (
		    $7::jsonb = '[]'::jsonb
		    OR EXISTS (
		      SELECT 1
		      FROM jsonb_array_elements_text($7::jsonb) requested_tag(value)
		      WHERE requested_tag.value IN (
		        SELECT lower(channel_tag.value)
		        FROM jsonb_array_elements_text(
		          CASE
		            WHEN jsonb_typeof(c.metadata_json->'route_tags') = 'array' THEN c.metadata_json->'route_tags'
		            WHEN jsonb_typeof(c.metadata_json->'routing_profiles') = 'array' THEN c.metadata_json->'routing_profiles'
		            WHEN jsonb_typeof(c.metadata_json->'profiles') = 'array' THEN c.metadata_json->'profiles'
		            WHEN jsonb_typeof(c.metadata_json->'route_profile') = 'string' THEN jsonb_build_array(c.metadata_json->>'route_profile')
		            ELSE '[]'::jsonb
		          END
		        ) channel_tag(value)
		        UNION
		        SELECT lower(account_tag.value)
		        FROM jsonb_array_elements_text(
		          CASE
		            WHEN jsonb_typeof(a.metadata_json->'route_tags') = 'array' THEN a.metadata_json->'route_tags'
		            WHEN jsonb_typeof(a.metadata_json->'routing_profiles') = 'array' THEN a.metadata_json->'routing_profiles'
		            WHEN jsonb_typeof(a.metadata_json->'profiles') = 'array' THEN a.metadata_json->'profiles'
		            WHEN jsonb_typeof(a.metadata_json->'route_profile') = 'string' THEN jsonb_build_array(a.metadata_json->>'route_profile')
		            ELSE '[]'::jsonb
		          END
		        ) account_tag(value)
		      )
		    )
		  )
		  AND ars.active_requests < a.max_concurrency
		  AND (ars.cooldown_until IS NULL OR ars.cooldown_until <= now())
		  AND (
		    COALESCE(ars.circuit_state, 'closed') = 'closed'
		    OR (COALESCE(ars.circuit_state, 'closed') = 'half_open' AND ars.active_requests = 0)
		    OR (
		      COALESCE(ars.circuit_state, 'closed') = 'open'
		      AND ars.circuit_half_open_after IS NOT NULL
		      AND ars.circuit_half_open_after <= now()
		      AND ars.active_requests = 0
		    )
		  )
		  AND (aas.account_id IS NULL OR aas.auth_status IN ('active', 'refresh_due'))
		  AND (aas.expires_at IS NULL OR aas.expires_at > now())
		  AND NOT EXISTS (
		    SELECT 1 FROM account_quota_windows qw
		    WHERE qw.account_id = a.id
		      AND (qw.reset_at IS NULL OR qw.reset_at > now())
		      AND qw.remaining IS NOT NULL
		      AND qw.remaining <= 0
		  )
		ORDER BY COALESCE((
		           SELECT MAX(
		             CASE
		               WHEN (qw.metadata_json->>'limit') ~ '^[0-9]+(\.[0-9]+)?$'
		                 AND NULLIF(qw.metadata_json->>'limit', '')::numeric > 0
		                 THEN qw.remaining / NULLIF(qw.metadata_json->>'limit', '')::numeric
		               ELSE LEAST(qw.remaining, 1000) / 1000
		             END
		           )
		           FROM account_quota_windows qw
		           WHERE qw.account_id = a.id
		             AND (qw.reset_at IS NULL OR qw.reset_at > now())
		             AND qw.remaining IS NOT NULL
		             AND qw.remaining > 0
		         ), 1) DESC,
		         c.priority ASC,
		         ca.priority ASC,
		         a.priority ASC,
		         CASE
		           WHEN ars.last_failure_at IS NOT NULL AND ars.last_failure_at > now() - interval '10 minutes'
		             THEN 1 + (ars.failure_count::numeric / GREATEST(ars.success_count + ars.failure_count, 1))
		           ELSE (ars.failure_count::numeric / GREATEST(ars.success_count + ars.failure_count, 1)) * 0.25
		         END ASC,
		         CASE WHEN $6::int > 0 THEN ca.retry_priority ELSE 100 END ASC,
		         (
		           SELECT MIN(qw.reset_at)
		           FROM account_quota_windows qw
		           WHERE qw.account_id = a.id
		             AND qw.reset_at IS NOT NULL
		             AND qw.reset_at > now()
		             AND qw.remaining IS NOT NULL
		             AND qw.remaining > 0
		         ) ASC NULLS LAST,
		         (
		           (('x' || substr(md5($5 || ':' || c.id::text || ':' || a.id::text || ':' || ca.id::text), 1, 15))::bit(60)::bigint)::numeric
		           / GREATEST(c.weight::numeric * ca.weight::numeric, 1)
		         ) ASC,
		         ars.active_requests ASC,
		         a.created_at ASC
		FOR UPDATE OF ars SKIP LOCKED
		LIMIT 1
	`, model, endpoint, userID, routingMode, selectionSeed, attempt, routeTagsJSON, excludedAccountIDsJSON).Scan(
		&route.ChannelID, &route.AccountID, &route.BaseURL, &route.ProviderType, &route.UpstreamModel, &route.TimeoutSeconds, &route.ProxyURL,
		&route.RoutingMode, &route.OwnerUserID, &route.AuthMode, &route.AuthStatus, &routeAuthExpiresAt, &routeRefreshDueAt,
		&route.AbilityMeta, &route.ChannelMeta, &route.AccountMeta, &route.RuntimeMeta, &ciphertext, &nonce,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return routeInfo{}, appError{status: http.StatusServiceUnavailable, code: "no_available_account", message: "No valid upstream account is available.", typ: "upstream_error"}
	}
	if err != nil {
		return routeInfo{}, err
	}

	apiKey, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
	if err != nil {
		return routeInfo{}, err
	}
	bundle, err := normalizedTokenBundleFromSecret(apiKey)
	if err != nil {
		return routeInfo{}, err
	}
	route.APIKey = bundle.authSecret()
	route.TokenProvider = bundle.Provider
	route.AuthScheme = bundle.AuthScheme
	route.TokenSubject = bundle.Subject
	route.TokenMetadata = bundle.Metadata
	route.AuthExpiresAt = nullableTimeString(&routeAuthExpiresAt.Time)
	route.RefreshDueAt = nullableTimeString(&routeRefreshDueAt.Time)
	if !authStatusAllowsRouting(route.AuthStatus, routeAuthExpiresAt, routeRefreshDueAt, time.Now().UTC()) {
		return routeInfo{}, appError{status: http.StatusServiceUnavailable, code: "no_available_account", message: "No valid upstream account is available.", typ: "upstream_error"}
	}
	if err := s.ensureRuntimeFingerprint(ctx, tx, &route); err != nil {
		return routeInfo{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET active_requests = active_requests + 1,
		    circuit_state = CASE
		      WHEN circuit_state = 'open'
		        AND circuit_half_open_after IS NOT NULL
		        AND circuit_half_open_after <= now()
		        THEN 'half_open'
		      ELSE circuit_state
		    END,
		    cooldown_until = CASE
		      WHEN circuit_state = 'open'
		        AND circuit_half_open_after IS NOT NULL
		        AND circuit_half_open_after <= now()
		        THEN NULL
		      ELSE cooldown_until
		    END,
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID); err != nil {
		return routeInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return routeInfo{}, err
	}
	return route, nil
}

func (s *Server) checkoutAffinityRoute(ctx context.Context, model string, endpoint string, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string) (routeInfo, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return routeInfo{}, false, err
	}
	defer tx.Rollback()

	var route routeInfo
	var ciphertext, nonce []byte
	var routeAuthExpiresAt, routeRefreshDueAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT c.id::text, a.id::text, c.base_url, p.provider_type,
		       COALESCE(NULLIF(ca.upstream_model, ''), ca.model_name), c.timeout_seconds,
		       COALESCE(ap.proxy_url, cp.proxy_url, ''),
		       a.routing_mode, COALESCE(a.owner_user_id::text, ''),
		       COALESCE(aas.auth_mode, ''), COALESCE(aas.auth_status, ''), aas.expires_at, aas.refresh_due_at,
		       ca.transform_capability_json::text, c.metadata_json::text, a.metadata_json::text, ars.metadata_json::text,
		       v.secret_ciphertext, v.secret_nonce
		FROM northbound_route_affinities ra
		JOIN accounts a ON a.id = ra.account_id AND a.status = 'active'
		JOIN channels c ON c.id = a.channel_id AND c.status = 'active'
		JOIN providers p ON p.id = c.provider_id AND p.status = 'active'
		JOIN channel_abilities ca ON ca.channel_id = c.id
		JOIN credential_vault_records v ON v.id = a.credential_vault_record_id
		JOIN account_runtime_states ars ON ars.account_id = a.id
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		LEFT JOIN proxies cp ON cp.id = c.proxy_id AND cp.status = 'active'
		LEFT JOIN proxies ap ON ap.id = a.proxy_id AND ap.status = 'active'
		WHERE ra.user_id = $1::uuid
		  AND ra.api_key_id = $2::uuid
		  AND ra.model_name = $3
		  AND ra.endpoint = $4
		  AND ra.session_key_hash = $5
		  AND ra.expires_at > now()
		  AND ca.model_name = $3
		  AND ca.endpoint = $4
		  AND ca.status = 'active'
		  AND a.routing_mode = $6
		  AND (
		    ($6 = 'pool' AND a.owner_user_id IS NULL)
		    OR ($6 = 'byo' AND a.owner_user_id::text = $1::text)
		  )
		  AND ars.active_requests < a.max_concurrency
		  AND (ars.cooldown_until IS NULL OR ars.cooldown_until <= now())
		  AND (
		    COALESCE(ars.circuit_state, 'closed') = 'closed'
		    OR (COALESCE(ars.circuit_state, 'closed') = 'half_open' AND ars.active_requests = 0)
		    OR (
		      COALESCE(ars.circuit_state, 'closed') = 'open'
		      AND ars.circuit_half_open_after IS NOT NULL
		      AND ars.circuit_half_open_after <= now()
		      AND ars.active_requests = 0
		    )
		  )
		  AND (aas.account_id IS NULL OR aas.auth_status IN ('active', 'refresh_due'))
		  AND (aas.expires_at IS NULL OR aas.expires_at > now())
		  AND NOT EXISTS (
		    SELECT 1 FROM account_quota_windows qw
		    WHERE qw.account_id = a.id
		      AND (qw.reset_at IS NULL OR qw.reset_at > now())
		      AND qw.remaining IS NOT NULL
		      AND qw.remaining <= 0
		  )
		FOR UPDATE OF ars SKIP LOCKED
		LIMIT 1
	`, userID, apiKeyID, model, endpoint, routeAffinityHash(affinityKey), routingMode).Scan(
		&route.ChannelID, &route.AccountID, &route.BaseURL, &route.ProviderType, &route.UpstreamModel, &route.TimeoutSeconds, &route.ProxyURL,
		&route.RoutingMode, &route.OwnerUserID, &route.AuthMode, &route.AuthStatus, &routeAuthExpiresAt, &routeRefreshDueAt,
		&route.AbilityMeta, &route.ChannelMeta, &route.AccountMeta, &route.RuntimeMeta, &ciphertext, &nonce,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return routeInfo{}, false, nil
	}
	if err != nil {
		return routeInfo{}, false, err
	}

	secret, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
	if err != nil {
		return routeInfo{}, false, err
	}
	bundle, err := normalizedTokenBundleFromSecret(secret)
	if err != nil {
		return routeInfo{}, false, err
	}
	route.APIKey = bundle.authSecret()
	route.TokenProvider = bundle.Provider
	route.AuthScheme = bundle.AuthScheme
	route.TokenSubject = bundle.Subject
	route.TokenMetadata = bundle.Metadata
	route.AuthExpiresAt = nullableTimeString(&routeAuthExpiresAt.Time)
	route.RefreshDueAt = nullableTimeString(&routeRefreshDueAt.Time)
	if !authStatusAllowsRouting(route.AuthStatus, routeAuthExpiresAt, routeRefreshDueAt, time.Now().UTC()) {
		return routeInfo{}, false, nil
	}
	if err := s.ensureRuntimeFingerprint(ctx, tx, &route); err != nil {
		return routeInfo{}, false, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET active_requests = active_requests + 1,
		    circuit_state = CASE
		      WHEN circuit_state = 'open'
		        AND circuit_half_open_after IS NOT NULL
		        AND circuit_half_open_after <= now()
		        THEN 'half_open'
		      ELSE circuit_state
		    END,
		    cooldown_until = CASE
		      WHEN circuit_state = 'open'
		        AND circuit_half_open_after IS NOT NULL
		        AND circuit_half_open_after <= now()
		        THEN NULL
		      ELSE cooldown_until
		    END,
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID); err != nil {
		return routeInfo{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE northbound_route_affinities
		SET channel_id = $6,
		    account_id = $7,
		    rule_name = COALESCE(NULLIF($9, ''), rule_name),
		    hit_count = hit_count + 1,
		    last_hit_at = now(),
		    last_seen_at = now(),
		    expires_at = now() + CASE WHEN $8::int > 0 THEN $8::int * interval '1 second' ELSE interval '7 days' END
		WHERE user_id = $1::uuid
		  AND api_key_id = $2::uuid
		  AND model_name = $3
		  AND endpoint = $4
		  AND session_key_hash = $5
	`, userID, apiKeyID, model, endpoint, routeAffinityHash(affinityKey), route.ChannelID, route.AccountID, affinityTTLSeconds, strings.TrimSpace(affinityRuleName)); err != nil {
		return routeInfo{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return routeInfo{}, false, err
	}
	return route, true, nil
}

func (s *Server) checkoutClaudeCodeSidecarRoute(ctx context.Context, auth apiKeyAuth) (routeInfo, error) {
	return s.checkoutClaudeCodeSidecarRouteExcluding(ctx, auth, nil)
}

func (s *Server) checkoutClaudeCodeSidecarRouteExcluding(ctx context.Context, auth apiKeyAuth, excludedAccountIDs []string) (routeInfo, error) {
	routingMode, err := routingModeValue(auth.RoutingMode)
	if err != nil {
		return routeInfo{}, err
	}
	if routingMode != "byo" {
		return routeInfo{}, forbidden("Claude Code account endpoints require BYO routing.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return routeInfo{}, err
	}
	defer tx.Rollback()

	var route routeInfo
	var ciphertext, nonce []byte
	var routeAuthExpiresAt, routeRefreshDueAt sql.NullTime
	excludedAccountIDsJSON := "[]"
	if len(excludedAccountIDs) > 0 {
		excludedAccountIDsJSON = mustEncodeJSON(excludedAccountIDs)
	}
	err = tx.QueryRowContext(ctx, `
		SELECT c.id::text, a.id::text, c.base_url, p.provider_type,
		       c.timeout_seconds,
		       COALESCE(ap.proxy_url, cp.proxy_url, ''),
		       a.routing_mode, COALESCE(a.owner_user_id::text, ''),
		       COALESCE(aas.auth_mode, ''), COALESCE(aas.auth_status, ''), aas.expires_at, aas.refresh_due_at,
		       c.metadata_json::text, a.metadata_json::text, ars.metadata_json::text,
		       v.secret_ciphertext, v.secret_nonce
		FROM accounts a
		JOIN channels c ON c.id = a.channel_id AND c.status = 'active'
		JOIN providers p ON p.id = c.provider_id AND p.status = 'active'
		JOIN credential_vault_records v ON v.id = a.credential_vault_record_id
		JOIN account_runtime_states ars ON ars.account_id = a.id
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		LEFT JOIN proxies cp ON cp.id = c.proxy_id AND cp.status = 'active'
		LEFT JOIN proxies ap ON ap.id = a.proxy_id AND ap.status = 'active'
		WHERE a.status = 'active'
		  AND a.routing_mode = $2
		  AND a.owner_user_id::text = $1
		  AND NOT EXISTS (
		    SELECT 1
		    FROM jsonb_array_elements_text($3::jsonb) excluded_account(id)
		    WHERE excluded_account.id = a.id::text
		  )
		  AND aas.auth_mode = 'claude_cli'
		  AND (aas.auth_status IN ('active', 'refresh_due'))
		  AND (aas.expires_at IS NULL OR aas.expires_at > now())
		  AND ars.active_requests < a.max_concurrency
		  AND (ars.cooldown_until IS NULL OR ars.cooldown_until <= now())
		  AND (
		    COALESCE(ars.circuit_state, 'closed') = 'closed'
		    OR (COALESCE(ars.circuit_state, 'closed') = 'half_open' AND ars.active_requests = 0)
		    OR (
		      COALESCE(ars.circuit_state, 'closed') = 'open'
		      AND ars.circuit_half_open_after IS NOT NULL
		      AND ars.circuit_half_open_after <= now()
		      AND ars.active_requests = 0
		    )
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM account_quota_windows qw
		    WHERE qw.account_id = a.id
		      AND (qw.reset_at IS NULL OR qw.reset_at > now())
		      AND qw.remaining IS NOT NULL
		      AND qw.remaining <= 0
		  )
		ORDER BY c.priority ASC,
		         a.priority ASC,
		         CASE
		           WHEN ars.last_failure_at IS NOT NULL AND ars.last_failure_at > now() - interval '10 minutes'
		             THEN 1 + (ars.failure_count::numeric / GREATEST(ars.success_count + ars.failure_count, 1))
		           ELSE (ars.failure_count::numeric / GREATEST(ars.success_count + ars.failure_count, 1)) * 0.25
		         END ASC,
		         ars.active_requests ASC,
		         a.created_at ASC
		FOR UPDATE OF ars SKIP LOCKED
		LIMIT 1
	`, auth.UserID, routingMode, excludedAccountIDsJSON).Scan(
		&route.ChannelID, &route.AccountID, &route.BaseURL, &route.ProviderType, &route.TimeoutSeconds, &route.ProxyURL,
		&route.RoutingMode, &route.OwnerUserID, &route.AuthMode, &route.AuthStatus, &routeAuthExpiresAt, &routeRefreshDueAt,
		&route.ChannelMeta, &route.AccountMeta, &route.RuntimeMeta, &ciphertext, &nonce,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return routeInfo{}, appError{status: http.StatusServiceUnavailable, code: "no_available_account", message: "No valid Claude Code OAuth account is available.", typ: "upstream_error"}
	}
	if err != nil {
		return routeInfo{}, err
	}

	secret, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
	if err != nil {
		return routeInfo{}, err
	}
	bundle, err := normalizedTokenBundleFromSecret(secret)
	if err != nil {
		return routeInfo{}, err
	}
	route.APIKey = bundle.authSecret()
	route.TokenProvider = bundle.Provider
	route.AuthScheme = bundle.AuthScheme
	route.TokenSubject = bundle.Subject
	route.TokenMetadata = bundle.Metadata
	route.AuthExpiresAt = nullableTimeString(&routeAuthExpiresAt.Time)
	route.RefreshDueAt = nullableTimeString(&routeRefreshDueAt.Time)
	if !isClaudeCodeRoute(route) {
		return routeInfo{}, appError{status: http.StatusServiceUnavailable, code: "no_available_account", message: "No valid Claude Code OAuth account is available.", typ: "upstream_error"}
	}
	if !authStatusAllowsRouting(route.AuthStatus, routeAuthExpiresAt, routeRefreshDueAt, time.Now().UTC()) {
		return routeInfo{}, appError{status: http.StatusServiceUnavailable, code: "no_available_account", message: "No valid Claude Code OAuth account is available.", typ: "upstream_error"}
	}
	if err := s.ensureRuntimeFingerprint(ctx, tx, &route); err != nil {
		return routeInfo{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET active_requests = active_requests + 1,
		    circuit_state = CASE
		      WHEN circuit_state = 'open'
		        AND circuit_half_open_after IS NOT NULL
		        AND circuit_half_open_after <= now()
		        THEN 'half_open'
		      ELSE circuit_state
		    END,
		    cooldown_until = CASE
		      WHEN circuit_state = 'open'
		        AND circuit_half_open_after IS NOT NULL
		        AND circuit_half_open_after <= now()
		        THEN NULL
		      ELSE cooldown_until
		    END,
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID); err != nil {
		return routeInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return routeInfo{}, err
	}
	return route, nil
}

func (s *Server) ensureRuntimeFingerprint(ctx context.Context, tx *sql.Tx, route *routeInfo) error {
	if route == nil || tx == nil || route.AccountID == "" {
		return nil
	}
	metadata := metadataMapFromJSON(route.RuntimeMeta)
	if metadata == nil {
		metadata = map[string]any{}
	}
	now := time.Now().UTC()
	changed := false
	if isClaudeCodeRoute(*route) {
		claudeMetadata, claudeChanged := mergeClaudeRuntimeFingerprintMetadata(*route, now)
		for key, value := range claudeMetadata {
			metadata[key] = value
		}
		if claudeChanged {
			changed = true
		}
	}
	if mergeGenericRuntimeFingerprintMetadata(*route, metadata, now) {
		changed = true
	}
	if !changed {
		return nil
	}
	encoded := mustEncodeJSON(metadata)
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET metadata_json = $2::jsonb,
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID, encoded); err != nil {
		return err
	}
	route.RuntimeMeta = encoded
	return nil
}

func mergeGenericRuntimeFingerprintMetadata(route routeInfo, metadata map[string]any, now time.Time) bool {
	namespace := runtimeFingerprintNamespace(route)
	if namespace == "" {
		return false
	}
	changed := false

	official, ok := objectValue(metadata["official_client"])
	if !ok {
		official = map[string]any{}
		metadata["official_client"] = official
		changed = true
	}
	fingerprint, ok := objectValue(official["fingerprint"])
	if !ok {
		fingerprint = map[string]any{}
		official["fingerprint"] = fingerprint
		changed = true
	}
	namespaced, ok := objectValue(metadata[namespace])
	if !ok {
		namespaced = map[string]any{}
		metadata[namespace] = namespaced
		changed = true
	}

	deviceID := firstNonEmpty(metadataText(namespaced["device_id"]), metadataText(fingerprint["device_id"]))
	if deviceID == "" {
		deviceID = newRuntimeDeviceID(route, namespace)
	}
	if setRuntimeMetadataIfEmpty(namespaced, "device_id", deviceID) {
		changed = true
	}
	if setRuntimeMetadataIfEmpty(fingerprint, "device_id", deviceID) {
		changed = true
	}

	clientID := firstNonEmpty(metadataText(namespaced["client_id"]), metadataText(fingerprint["client_id"]))
	if clientID == "" {
		clientID = newRuntimeClientID(route, namespace)
	}
	if setRuntimeMetadataIfEmpty(namespaced, "client_id", clientID) {
		changed = true
	}
	if setRuntimeMetadataIfEmpty(fingerprint, "client_id", clientID) {
		changed = true
	}

	userAgent := firstNonEmpty(metadataText(namespaced["user_agent"]), metadataText(fingerprint["user_agent"]), runtimeFingerprintUserAgent(route, namespace))
	if setRuntimeMetadataIfEmpty(namespaced, "user_agent", userAgent) {
		changed = true
	}
	if setRuntimeMetadataIfEmpty(fingerprint, "user_agent", userAgent) {
		changed = true
	}

	if namespace == "kiro" {
		if setRuntimeMetadataIfEmpty(namespaced, "fingerprint", deviceID) {
			changed = true
		}
	}
	if profile := routeTLSFingerprintProfile(route); profile != "" {
		if setRuntimeMetadataIfEmpty(fingerprint, "tls_profile", profile) {
			changed = true
		}
		if setRuntimeMetadataIfEmpty(namespaced, "tls_profile", profile) {
			changed = true
		}
		if hash := tlsFingerprintJA3Hash(profile); hash != "" {
			if setRuntimeMetadataIfEmpty(fingerprint, "ja3_hash", hash) {
				changed = true
			}
		}
	}
	if setRuntimeMetadataIfEmpty(fingerprint, "provider", namespace) {
		changed = true
	}
	if setRuntimeMetadataIfEmpty(fingerprint, "source", "account_runtime") {
		changed = true
	}
	if setRuntimeMetadataIfEmpty(fingerprint, "created_at", now.UTC().Format(time.RFC3339)) {
		changed = true
	}
	return changed
}

func runtimeFingerprintNamespace(route routeInfo) string {
	switch {
	case isCodexOfficialRoute(route):
		return "codex"
	case isClaudeCodeRoute(route):
		return "claude"
	case isGeminiOfficialTLSRoute(route):
		return "gemini"
	case isGitHubCopilotRoute(route):
		return "github"
	case strings.Contains(strings.ToLower(route.ProviderType), "antigravity"):
		return "antigravity"
	case strings.Contains(strings.ToLower(route.ProviderType), "kiro"):
		return "kiro"
	case strings.Contains(strings.ToLower(route.ProviderType), "windsurf") || strings.Contains(strings.ToLower(route.ProviderType), "codeium"):
		return "windsurf"
	default:
		return ""
	}
}

func runtimeFingerprintUserAgent(route routeInfo, namespace string) string {
	switch namespace {
	case "codex":
		originator := firstNonEmpty(routeMetadataString(route, "codex", "originator"), "codex_exec")
		return codexUserAgent(route, originator, codexClientVersion(route))
	case "claude":
		return claudeCodeUserAgent(route)
	case "gemini":
		return geminiUserAgent(route, firstNonEmpty(route.UpstreamModel, "gemini-2.5-pro"))
	case "github":
		return githubCopilotUserAgent(route)
	case "antigravity":
		return antigravityUserAgent(route)
	case "kiro":
		fingerprint := firstNonEmpty(routeMetadataString(route, "kiro", "fingerprint", "machine_fingerprint"), sha256Hex(firstNonEmpty(route.AccountID, route.TokenSubject, "elucid-relay-kiro")))
		version := firstNonEmpty(routeMetadataString(route, "kiro", "client_version", "kiro_version"), "0.7.45")
		nodeVersion := firstNonEmpty(routeMetadataString(route, "kiro", "node_version"), "22.21.1")
		osText := firstNonEmpty(routeMetadataString(route, "kiro", "os"), "win32#10.0.19044")
		return "aws-sdk-js/1.0.27 ua/2.1 os/" + osText + " lang/js md/nodejs#" + nodeVersion + " api/codewhispererstreaming#1.0.27 m/E KiroIDE-" + version + "-" + fingerprint
	case "windsurf":
		ideVersion := firstNonEmpty(routeMetadataString(route, "windsurf", "ide_version", "client_version"), "1.20.9")
		extensionVersion := firstNonEmpty(routeMetadataString(route, "windsurf", "extension_version", "language_server_version"), ideVersion)
		return "Windsurf/" + ideVersion + " Codeium/" + extensionVersion
	default:
		return ""
	}
}

func tlsFingerprintJA3Hash(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "codex_rustls", "rustls", "codex":
		return codexRustlsJA3Hash
	case "node24", "node", "nodejs", "claude", "claude_code", "chrome", "chromium":
		return node24JA3Hash
	default:
		return ""
	}
}

func setRuntimeMetadataIfEmpty(metadata map[string]any, key string, value string) bool {
	if metadata == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return false
	}
	if metadataText(metadata[key]) != "" {
		return false
	}
	metadata[key] = value
	return true
}

func newRuntimeDeviceID(route routeInfo, namespace string) string {
	if value := randomHexString(32); value != "" {
		return value
	}
	return sha256Hex("runtime-device:" + namespace + ":" + runtimeFingerprintSeed(route))
}

func newRuntimeClientID(route routeInfo, namespace string) string {
	if value := randomUUIDString(); value != "" {
		return value
	}
	sum := sha256.Sum256([]byte("runtime-client:" + namespace + ":" + runtimeFingerprintSeed(route)))
	return uuidStringFromBytes(sum[:16])
}

func runtimeFingerprintSeed(route routeInfo) string {
	parts := []string{
		strings.TrimSpace(route.ProviderType),
		strings.TrimSpace(route.TokenProvider),
		strings.TrimSpace(route.AuthMode),
		strings.TrimSpace(route.TokenSubject),
		strings.TrimSpace(route.AccountID),
		strings.TrimSpace(route.OwnerUserID),
		strings.TrimSpace(route.ChannelID),
		strings.TrimSpace(route.BaseURL),
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return "elucid-relay-runtime"
	}
	return strings.Join(filtered, "|")
}

func mergeClaudeRuntimeFingerprintMetadata(route routeInfo, now time.Time) (map[string]any, bool) {
	metadata := metadataMapFromJSON(route.RuntimeMeta)
	if metadata == nil {
		metadata = map[string]any{}
	}
	changed := false
	claude, ok := objectValue(metadata["claude"])
	if !ok {
		claude = map[string]any{}
		metadata["claude"] = claude
		changed = true
	}
	fingerprint, ok := objectValue(claude["fingerprint"])
	if !ok {
		fingerprint = map[string]any{}
		claude["fingerprint"] = fingerprint
		changed = true
	}

	deviceID := firstNonEmpty(
		metadataText(claude["device_id"]),
		metadataText(fingerprint["device_id"]),
	)
	if deviceID == "" {
		deviceID = newClaudeRuntimeDeviceID(route)
		claude["device_id"] = deviceID
		fingerprint["device_id"] = deviceID
		changed = true
	} else {
		if metadataText(claude["device_id"]) == "" {
			claude["device_id"] = deviceID
			changed = true
		}
		if metadataText(fingerprint["device_id"]) == "" {
			fingerprint["device_id"] = deviceID
			changed = true
		}
	}

	clientID := firstNonEmpty(
		metadataText(claude["client_id"]),
		metadataText(fingerprint["client_id"]),
	)
	if clientID == "" {
		clientID = newClaudeRuntimeClientID(route)
		claude["client_id"] = clientID
		fingerprint["client_id"] = clientID
		changed = true
	} else {
		if metadataText(claude["client_id"]) == "" {
			claude["client_id"] = clientID
			changed = true
		}
		if metadataText(fingerprint["client_id"]) == "" {
			fingerprint["client_id"] = clientID
			changed = true
		}
	}

	userAgent := firstNonEmpty(
		metadataText(claude["user_agent"]),
		metadataText(fingerprint["user_agent"]),
	)
	if userAgent == "" {
		userAgent = claudeCodeUserAgent(route)
		claude["user_agent"] = userAgent
		fingerprint["user_agent"] = userAgent
		changed = true
	} else {
		if metadataText(claude["user_agent"]) == "" {
			claude["user_agent"] = userAgent
			changed = true
		}
		if metadataText(fingerprint["user_agent"]) == "" {
			fingerprint["user_agent"] = userAgent
			changed = true
		}
	}

	if metadataText(claude["fingerprint_source"]) == "" {
		claude["fingerprint_source"] = "account_runtime"
		changed = true
	}
	if metadataText(fingerprint["source"]) == "" {
		fingerprint["source"] = "account_runtime"
		changed = true
	}
	createdAt := firstNonEmpty(
		metadataText(claude["fingerprint_created_at"]),
		metadataText(fingerprint["created_at"]),
	)
	if createdAt == "" {
		createdAt = now.UTC().Format(time.RFC3339)
		claude["fingerprint_created_at"] = createdAt
		fingerprint["created_at"] = createdAt
		changed = true
	} else {
		if metadataText(claude["fingerprint_created_at"]) == "" {
			claude["fingerprint_created_at"] = createdAt
			changed = true
		}
		if metadataText(fingerprint["created_at"]) == "" {
			fingerprint["created_at"] = createdAt
			changed = true
		}
	}

	return metadata, changed
}

func newClaudeRuntimeDeviceID(route routeInfo) string {
	if value := randomHexString(32); value != "" {
		return value
	}
	return sha256Hex("claude-code-runtime-device:" + claudeCodeFingerprintSeed(route))
}

func newClaudeRuntimeClientID(route routeInfo) string {
	if value := randomUUIDString(); value != "" {
		return value
	}
	sum := sha256.Sum256([]byte("claude-code-runtime-client:" + claudeCodeFingerprintSeed(route)))
	return uuidStringFromBytes(sum[:16])
}

func randomHexString(byteCount int) string {
	if byteCount <= 0 {
		return ""
	}
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

func randomUUIDString() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return uuidStringFromBytes(buf)
}

func uuidStringFromBytes(buf []byte) string {
	if len(buf) < 16 {
		return ""
	}
	value := make([]byte, 16)
	copy(value, buf[:16])
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func (s *Server) upsertRouteAffinity(ctx context.Context, userID string, apiKeyID string, model string, endpoint string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, route routeInfo) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO northbound_route_affinities (user_id, api_key_id, model_name, endpoint, session_key_hash, channel_id, account_id, rule_name, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid,
		        $9,
		        now() + CASE WHEN $8::int > 0 THEN $8::int * interval '1 second' ELSE interval '7 days' END)
		ON CONFLICT (api_key_id, model_name, endpoint, session_key_hash)
		DO UPDATE SET channel_id = EXCLUDED.channel_id,
		              account_id = EXCLUDED.account_id,
		              rule_name = COALESCE(NULLIF(EXCLUDED.rule_name, ''), northbound_route_affinities.rule_name),
		              last_seen_at = now(),
		              expires_at = now() + CASE WHEN $8::int > 0 THEN $8::int * interval '1 second' ELSE interval '7 days' END
	`, userID, apiKeyID, model, endpoint, routeAffinityHash(affinityKey), route.ChannelID, route.AccountID, affinityTTLSeconds, strings.TrimSpace(affinityRuleName))
	return err
}

func (s *Server) observeRouteAffinityMiss(ctx context.Context, userID string, apiKeyID string, model string, endpoint string, affinityKey string, affinityRuleName string) {
	if s == nil || s.db == nil || strings.TrimSpace(affinityKey) == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE northbound_route_affinities
		SET rule_name = COALESCE(NULLIF($6, ''), rule_name),
		    miss_count = miss_count + 1,
		    last_miss_at = now()
		WHERE user_id = $1::uuid
		  AND api_key_id = $2::uuid
		  AND model_name = $3
		  AND endpoint = $4
		  AND session_key_hash = $5
	`, userID, apiKeyID, model, endpoint, routeAffinityHash(affinityKey), strings.TrimSpace(affinityRuleName))
}

func (s *Server) completeRoute(ctx context.Context, route routeInfo, success bool, upstreamStatus int, message string) error {
	if route.AccountID == "" {
		return nil
	}

	if success {
		_, err := s.db.ExecContext(ctx, `
			UPDATE account_runtime_states
			SET active_requests = GREATEST(active_requests - 1, 0),
			    cooldown_until = NULL,
			    last_error = '',
			    success_count = success_count + 1,
			    last_success_at = now(),
			    circuit_state = 'closed',
			    circuit_failure_count = 0,
			    circuit_opened_at = NULL,
			    circuit_half_open_after = NULL,
			    updated_at = now()
			WHERE account_id = $1
		`, route.AccountID)
		if err != nil {
			return err
		}
		return s.decrementRequestQuota(ctx, route.AccountID)
	}

	cooldownUntil := time.Now().UTC().Add(circuitBreakerOpenDuration(route, upstreamStatus))
	failureThreshold := circuitBreakerFailureThreshold(route)
	var circuitState string
	var circuitFailureCount int
	err := s.db.QueryRowContext(ctx, `
		UPDATE account_runtime_states
		SET active_requests = GREATEST(active_requests - 1, 0),
		    cooldown_until = $2,
		    last_error = $3,
		    failure_count = failure_count + 1,
		    last_failure_at = now(),
		    circuit_failure_count = circuit_failure_count + 1,
		    circuit_state = CASE
		      WHEN circuit_state = 'half_open' OR circuit_failure_count + 1 >= $4 THEN 'open'
		      ELSE 'closed'
		    END,
		    circuit_opened_at = CASE
		      WHEN circuit_state = 'half_open' OR circuit_failure_count + 1 >= $4 THEN now()
		      ELSE NULL
		    END,
		    circuit_half_open_after = CASE
		      WHEN circuit_state = 'half_open' OR circuit_failure_count + 1 >= $4 THEN $2
		      ELSE NULL
		    END,
		    updated_at = now()
		WHERE account_id = $1
		RETURNING circuit_state, circuit_failure_count
	`, route.AccountID, cooldownUntil, truncateForStorage(message, 500), failureThreshold).Scan(&circuitState, &circuitFailureCount)
	if err != nil {
		return err
	}
	if circuitState == "open" {
		_ = s.emitNotification(ctx, notificationEventInput{
			EventType:  "account_circuit_open",
			Severity:   "warning",
			Title:      "Upstream account circuit opened",
			Message:    "An upstream account was removed from routing until the half-open probe window.",
			TargetType: "account",
			TargetID:   route.AccountID,
			Payload: map[string]any{
				"provider_type":         route.ProviderType,
				"channel_id":            route.ChannelID,
				"account_id":            route.AccountID,
				"upstream_status":       upstreamStatus,
				"circuit_failure_count": circuitFailureCount,
				"half_open_after":       cooldownUntil.UTC().Format(time.RFC3339),
			},
		})
	}
	return s.decrementRequestQuota(ctx, route.AccountID)
}

func (s *Server) releaseRouteCheckout(ctx context.Context, route routeInfo) error {
	if s == nil || s.db == nil || route.AccountID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET active_requests = GREATEST(active_requests - 1, 0),
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID)
	return err
}

func isRoutePreparationError(err error) bool {
	var appErr appError
	return errors.As(err, &appErr)
}

func (s *Server) decrementRequestQuota(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_quota_windows
		SET remaining = GREATEST(remaining - 1, 0),
		    updated_at = now()
		WHERE account_id = $1
		  AND window_type IN ('request', 'requests')
		  AND remaining IS NOT NULL
		  AND (reset_at IS NULL OR reset_at > now())
	`, accountID)
	return err
}

type upstreamQuotaSignal struct {
	WindowType string
	Remaining  string
	Limit      string
	ResetAt    *time.Time
	Metadata   map[string]any
}

func (s *Server) applyUpstreamHeaderSignals(ctx context.Context, route routeInfo, upstreamStatus int, header http.Header) {
	if s == nil || s.db == nil || strings.TrimSpace(route.AccountID) == "" || header == nil {
		return
	}
	now := time.Now().UTC()
	if retryAfter := upstreamRetryAfterTime(header, now); retryAfter != nil && (upstreamStatus == http.StatusTooManyRequests || upstreamStatus == http.StatusServiceUnavailable) {
		if err := s.applyAccountRetryAfterCooldown(ctx, route, *retryAfter, upstreamStatus); err != nil {
			slog.WarnContext(ctx, "upstream retry-after cooldown sync failed", "error", err, "account_id", route.AccountID, "upstream_status", upstreamStatus)
		}
	}
	if err := s.recordUpstreamHeaderSignalSnapshot(ctx, route, upstreamStatus, header, now); err != nil {
		slog.WarnContext(ctx, "upstream header snapshot sync failed", "error", err, "account_id", route.AccountID, "upstream_status", upstreamStatus)
	}
	if !s.reverseProxySettingsOrDefault(ctx).QuotaHeaderSyncEnabled {
		return
	}
	for _, signal := range upstreamQuotaSignalsFromHeaders(header, upstreamStatus, now) {
		if err := s.upsertAccountQuotaWindowFromSignal(ctx, route.AccountID, signal); err != nil {
			slog.WarnContext(ctx, "upstream quota header sync failed", "error", err, "account_id", route.AccountID, "window_type", signal.WindowType)
		}
	}
}

func (s *Server) recordUpstreamHeaderSignalSnapshot(ctx context.Context, route routeInfo, upstreamStatus int, header http.Header, now time.Time) error {
	snapshot := upstreamHeaderSignalSnapshot(header, upstreamStatus, now)
	if len(snapshot) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET metadata_json = metadata_json || $2::jsonb,
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID, mustEncodeJSON(map[string]any{"last_upstream_headers": snapshot}))
	return err
}

func (s *Server) applyAccountRetryAfterCooldown(ctx context.Context, route routeInfo, retryAfter time.Time, upstreamStatus int) error {
	if !retryAfter.After(time.Now().UTC()) {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET cooldown_until = $2,
		    circuit_half_open_after = CASE
		      WHEN COALESCE(circuit_state, 'closed') = 'open' THEN $2
		      ELSE circuit_half_open_after
		    END,
		    last_error = $3,
		    updated_at = now()
		WHERE account_id = $1
	`, route.AccountID, retryAfter.UTC(), truncateForStorage("upstream retry-after status "+strconv.Itoa(upstreamStatus), 500))
	return err
}

func (s *Server) upsertAccountQuotaWindowFromSignal(ctx context.Context, accountID string, signal upstreamQuotaSignal) error {
	windowType := strings.TrimSpace(signal.WindowType)
	if windowType == "" {
		windowType = "requests"
	}
	metadata := defaultMap(signal.Metadata)
	metadata["source"] = "upstream_headers"
	metadata["limit"] = signal.Limit
	metadata["last_header_sync_at"] = time.Now().UTC().Format(time.RFC3339)
	var resetAt any
	if signal.ResetAt != nil {
		resetAt = signal.ResetAt.UTC()
		metadata["reset_at"] = signal.ResetAt.UTC().Format(time.RFC3339)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE account_quota_windows
		SET remaining = COALESCE(NULLIF($3, '')::numeric, remaining),
		    reset_at = COALESCE($4::timestamptz, reset_at),
		    metadata_json = metadata_json || $5::jsonb
		WHERE id = (
		  SELECT id FROM account_quota_windows
		  WHERE account_id = $1 AND window_type = $2
		  ORDER BY created_at DESC
		  LIMIT 1
		)
	`, accountID, windowType, signal.Remaining, resetAt, mustEncodeJSON(metadata))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO account_quota_windows (account_id, window_type, reset_at, remaining, metadata_json)
		VALUES ($1, $2, $3, NULLIF($4, '')::numeric, $5::jsonb)
	`, accountID, windowType, resetAt, signal.Remaining, mustEncodeJSON(metadata))
	return err
}

func upstreamQuotaSignalsFromHeaders(header http.Header, upstreamStatus int, now time.Time) []upstreamQuotaSignal {
	if header == nil {
		return nil
	}
	retryAfter := upstreamRetryAfterTime(header, now)
	signals := make([]upstreamQuotaSignal, 0, 2)
	if signal, ok := upstreamQuotaSignalFromHeaderSet(header, "requests", now,
		[]string{"X-RateLimit-Remaining-Requests", "X-RateLimit-Remaining", "RateLimit-Remaining", "X-Rate-Limit-Remaining", "Anthropic-RateLimit-Requests-Remaining", "X-Request-Limit-Remaining"},
		[]string{"X-RateLimit-Limit-Requests", "X-RateLimit-Limit", "RateLimit-Limit", "X-Rate-Limit-Limit", "Anthropic-RateLimit-Requests-Limit", "X-Request-Limit-Limit"},
		[]string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset", "RateLimit-Reset", "X-Rate-Limit-Reset", "Anthropic-RateLimit-Requests-Reset", "X-Request-Limit-Reset"},
	); ok {
		if upstreamStatus == http.StatusTooManyRequests && signal.Remaining == "" {
			signal.Remaining = "0"
		}
		if signal.ResetAt == nil && retryAfter != nil {
			signal.ResetAt = retryAfter
		}
		signals = append(signals, signal)
	} else if upstreamStatus == http.StatusTooManyRequests && retryAfter != nil {
		signals = append(signals, upstreamQuotaSignal{
			WindowType: "requests",
			Remaining:  "0",
			ResetAt:    retryAfter,
			Metadata: map[string]any{
				"upstream_status": upstreamStatus,
				"retry_after":     retryAfter.UTC().Format(time.RFC3339),
			},
		})
	}
	if signal, ok := upstreamQuotaSignalFromHeaderSet(header, "tokens", now,
		[]string{"X-RateLimit-Remaining-Tokens", "X-Rate-Limit-Remaining-Tokens", "Anthropic-RateLimit-Tokens-Remaining", "X-Token-Limit-Remaining"},
		[]string{"X-RateLimit-Limit-Tokens", "X-Rate-Limit-Limit-Tokens", "Anthropic-RateLimit-Tokens-Limit", "X-Token-Limit-Limit"},
		[]string{"X-RateLimit-Reset-Tokens", "X-Rate-Limit-Reset-Tokens", "Anthropic-RateLimit-Tokens-Reset", "X-Token-Limit-Reset"},
	); ok {
		signals = append(signals, signal)
	}
	if signal, ok := upstreamQuotaSignalFromHeaderSet(header, "input_tokens", now,
		[]string{"X-RateLimit-Remaining-Input-Tokens", "X-Rate-Limit-Remaining-Input-Tokens", "Anthropic-RateLimit-Input-Tokens-Remaining"},
		[]string{"X-RateLimit-Limit-Input-Tokens", "X-Rate-Limit-Limit-Input-Tokens", "Anthropic-RateLimit-Input-Tokens-Limit"},
		[]string{"X-RateLimit-Reset-Input-Tokens", "X-Rate-Limit-Reset-Input-Tokens", "Anthropic-RateLimit-Input-Tokens-Reset"},
	); ok {
		signals = append(signals, signal)
	}
	if signal, ok := upstreamQuotaSignalFromHeaderSet(header, "output_tokens", now,
		[]string{"X-RateLimit-Remaining-Output-Tokens", "X-Rate-Limit-Remaining-Output-Tokens", "Anthropic-RateLimit-Output-Tokens-Remaining"},
		[]string{"X-RateLimit-Limit-Output-Tokens", "X-Rate-Limit-Limit-Output-Tokens", "Anthropic-RateLimit-Output-Tokens-Limit"},
		[]string{"X-RateLimit-Reset-Output-Tokens", "X-Rate-Limit-Reset-Output-Tokens", "Anthropic-RateLimit-Output-Tokens-Reset"},
	); ok {
		signals = append(signals, signal)
	}
	return signals
}

func upstreamHeaderSignalSnapshot(header http.Header, upstreamStatus int, now time.Time) map[string]any {
	if header == nil {
		return nil
	}
	snapshot := map[string]any{
		"status":         upstreamStatus,
		"synced_at":      now.UTC().Format(time.RFC3339),
		"rate_limits":    map[string]any{},
		"retry_after_at": "",
	}
	if retryAfter := upstreamRetryAfterTime(header, now); retryAfter != nil {
		snapshot["retry_after_at"] = retryAfter.UTC().Format(time.RFC3339)
	}
	rateLimits := map[string]any{}
	for _, signal := range upstreamQuotaSignalsFromHeaders(header, upstreamStatus, now) {
		entry := map[string]any{
			"remaining": signal.Remaining,
			"limit":     signal.Limit,
		}
		if signal.ResetAt != nil {
			entry["reset_at"] = signal.ResetAt.UTC().Format(time.RFC3339)
		}
		rateLimits[signal.WindowType] = entry
	}
	if len(rateLimits) == 0 && snapshot["retry_after_at"] == "" {
		return nil
	}
	snapshot["rate_limits"] = rateLimits
	return snapshot
}

func upstreamQuotaSignalFromHeaderSet(header http.Header, windowType string, now time.Time, remainingKeys []string, limitKeys []string, resetKeys []string) (upstreamQuotaSignal, bool) {
	remainingRaw := firstHeaderValue(header, remainingKeys...)
	limitRaw := firstHeaderValue(header, limitKeys...)
	resetRaw := firstHeaderValue(header, resetKeys...)
	remaining := normalizedDecimalHeaderValue(remainingRaw)
	limit := normalizedDecimalHeaderValue(limitRaw)
	resetAt := upstreamRateLimitResetTime(resetRaw, now)
	if remaining == "" && limit == "" && resetAt == nil {
		return upstreamQuotaSignal{}, false
	}
	metadata := map[string]any{
		"remaining_header": remainingRaw,
		"limit_header":     limitRaw,
		"reset_header":     resetRaw,
	}
	return upstreamQuotaSignal{
		WindowType: windowType,
		Remaining:  remaining,
		Limit:      limit,
		ResetAt:    resetAt,
		Metadata:   metadata,
	}, true
}

func normalizedDecimalHeaderValue(value string) string {
	text, ok := decimalStringFromAny(strings.TrimSpace(value))
	if !ok {
		return ""
	}
	return text
}

func upstreamRetryAfterTime(header http.Header, now time.Time) *time.Time {
	value := strings.TrimSpace(firstHeaderValue(header, "Retry-After"))
	if value == "" {
		return nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		resetAt := now.Add(time.Duration(seconds * float64(time.Second))).UTC()
		return &resetAt
	}
	if parsed, err := http.ParseTime(value); err == nil {
		parsed = parsed.UTC()
		return &parsed
	}
	return nil
}

func upstreamRateLimitResetTime(value string, now time.Time) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		parsed = parsed.UTC()
		return &parsed
	}
	if duration, err := time.ParseDuration(value); err == nil && duration >= 0 {
		resetAt := now.Add(duration).UTC()
		return &resetAt
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		if seconds > 1_000_000_000 {
			resetAt := time.Unix(int64(seconds), 0).UTC()
			return &resetAt
		}
		resetAt := now.Add(time.Duration(seconds * float64(time.Second))).UTC()
		return &resetAt
	}
	return nil
}

func firstHeaderValue(header http.Header, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

const (
	defaultUpstreamMaxAttempts      = 2
	maxConfiguredUpstreamAttempts   = 4
	defaultSameAccountRetries       = 1
	maxConfiguredSameAccountRetries = 3
	defaultSameAccountRetryDelay    = 500 * time.Millisecond
	minSameAccountRetryDelay        = 50 * time.Millisecond
	maxSameAccountRetryDelay        = 5 * time.Second

	defaultCircuitBreakerFailureThreshold = 3
	maxCircuitBreakerFailureThreshold     = 20
	minCircuitBreakerOpenSeconds          = 5
	maxCircuitBreakerOpenSeconds          = 3600
)

func upstreamMaxAttemptsFromRoute(route routeInfo) int {
	attempts, ok := routeMetadataInt(route, "retry", "max_attempts", "upstream_max_attempts", "attempts")
	if !ok {
		return defaultUpstreamMaxAttempts
	}
	if attempts < 1 {
		return 1
	}
	if attempts > maxConfiguredUpstreamAttempts {
		return maxConfiguredUpstreamAttempts
	}
	return attempts
}

func hasRemainingUpstreamAttempts(attempt int, maxAttempts int) bool {
	return attempt+1 < maxAttempts
}

func circuitBreakerFailureThreshold(route routeInfo) int {
	threshold, ok := routeMetadataInt(route, "circuit_breaker", "failure_threshold", "failures", "threshold")
	if !ok {
		return defaultCircuitBreakerFailureThreshold
	}
	if threshold < 1 {
		return 1
	}
	if threshold > maxCircuitBreakerFailureThreshold {
		return maxCircuitBreakerFailureThreshold
	}
	return threshold
}

func circuitBreakerOpenDuration(route routeInfo, upstreamStatus int) time.Duration {
	if seconds, ok := routeCircuitStatusOpenSeconds(route, upstreamStatus); ok {
		if seconds < minCircuitBreakerOpenSeconds {
			seconds = minCircuitBreakerOpenSeconds
		}
		if seconds > maxCircuitBreakerOpenSeconds {
			seconds = maxCircuitBreakerOpenSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	if seconds, ok := routeMetadataInt(route, "circuit_breaker", "open_seconds", "cooldown_seconds", "half_open_after_seconds"); ok {
		if seconds < minCircuitBreakerOpenSeconds {
			seconds = minCircuitBreakerOpenSeconds
		}
		if seconds > maxCircuitBreakerOpenSeconds {
			seconds = maxCircuitBreakerOpenSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	if upstreamStatus == http.StatusTooManyRequests {
		return 10 * time.Minute
	}
	if upstreamStatus >= 500 {
		return 2 * time.Minute
	}
	return 30 * time.Second
}

func sameAccountMaxRetriesFromRoute(route routeInfo) int {
	retries, ok := routeMetadataInt(route, "retry", "same_account_retries", "same_account_max_retries", "same_account_retry_count")
	if !ok {
		return defaultSameAccountRetries
	}
	if retries < 0 {
		return 0
	}
	if retries > maxConfiguredSameAccountRetries {
		return maxConfiguredSameAccountRetries
	}
	return retries
}

func sameAccountRetryDelay(route routeInfo) time.Duration {
	delayMS, ok := routeMetadataInt(route, "retry", "same_account_delay_ms", "same_account_retry_delay_ms")
	if !ok {
		return defaultSameAccountRetryDelay
	}
	delay := time.Duration(delayMS) * time.Millisecond
	if delay < minSameAccountRetryDelay {
		return minSameAccountRetryDelay
	}
	if delay > maxSameAccountRetryDelay {
		return maxSameAccountRetryDelay
	}
	return delay
}

func shouldRetrySameAccount(route routeInfo, err error, upstreamStatus int, header http.Header) bool {
	if sameAccountMaxRetriesFromRoute(route) <= 0 {
		return false
	}
	now := time.Now().UTC()
	if retryAfter := upstreamRetryAfterTime(header, now); retryAfter != nil && retryAfter.After(now) {
		return false
	}
	if err != nil {
		return isSameAccountRetryableError(err)
	}
	if upstreamStatus == http.StatusTooManyRequests {
		return false
	}
	return upstreamStatus >= http.StatusInternalServerError &&
		upstreamStatus != http.StatusNotImplemented &&
		upstreamStatus != http.StatusHTTPVersionNotSupported
}

func isSameAccountRetryableError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection reset") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "server closed") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "broken pipe")
}

func sleepBeforeSameAccountRetry(ctx context.Context, route routeInfo) bool {
	delay := sameAccountRetryDelay(route)
	if delay <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

func (s *Server) observeUpstreamRetry(r *http.Request, route routeInfo, endpoint string, attempt int, maxAttempts int, upstreamStatus int, reason string, message string, durationMS int) {
	if reason == "" {
		reason = "upstream_retry"
	}
	slog.WarnContext(r.Context(), "upstream retrying",
		"request_id", requestIDFromContext(r.Context()),
		"provider", route.ProviderType,
		"channel_id", route.ChannelID,
		"account_id", route.AccountID,
		"endpoint", endpoint,
		"attempt", attempt+1,
		"max_attempts", maxAttempts,
		"upstream_status", upstreamStatus,
		"duration_ms", durationMS,
		"reason", reason,
		"message", truncateForStorage(message, 200),
	)
}

func appendFailedRouteAccountID(ids []string, route routeInfo) []string {
	accountID := strings.TrimSpace(route.AccountID)
	if accountID == "" || stringInSlice(ids, accountID) {
		return ids
	}
	return append(ids, accountID)
}

func (s *Server) callUpstreamWithRetry(r *http.Request, model string, endpoint string, body []byte, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, affinitySkipRetry bool, routeTags []string) (routeInfo, int, http.Header, []byte, int, error) {
	var lastRoute routeInfo
	var lastDuration int
	var lastErr error
	excludedAccountIDs := []string{}

	maxAttempts := defaultUpstreamMaxAttempts
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && affinitySkipRetry && strings.TrimSpace(affinityKey) != "" {
			if lastErr != nil {
				return lastRoute, 0, nil, nil, lastDuration, lastErr
			}
			return lastRoute, 0, nil, nil, lastDuration, upstreamUnavailable("affinity_retry_disabled", "Affinity rule disabled cross-account retry.")
		}
		route, err := s.checkoutRouteAttemptExcluding(r.Context(), model, endpoint, userID, apiKeyID, routingMode, affinityKey, affinityTTLSeconds, affinityRuleName, routeTags, excludedAccountIDs, attempt)
		if err != nil {
			if lastErr != nil {
				return lastRoute, 0, nil, nil, lastDuration, lastErr
			}
			return routeInfo{}, 0, nil, nil, 0, err
		}
		if attempt == 0 {
			maxAttempts = upstreamMaxAttemptsFromRoute(route)
		}
		lastRoute = route

		sameAccountRetries := sameAccountMaxRetriesFromRoute(route)
		attemptBody := body
		signatureRepairTried := false
		for sameAttempt := 0; ; sameAttempt++ {
			startedAt := time.Now()
			status, header, responseBody, err := s.callUpstream(r, route, attemptBody)
			durationMS := int(time.Since(startedAt).Milliseconds())
			lastDuration = durationMS

			if err != nil {
				lastErr = err
				if isRoutePreparationError(err) {
					_ = s.releaseRouteCheckout(context.Background(), route)
					return lastRoute, 0, nil, nil, lastDuration, lastErr
				}
				if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, err, 0, nil) {
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "same_account_error", err.Error(), durationMS)
					if !sleepBeforeSameAccountRetry(r.Context(), route) {
						return lastRoute, 0, nil, nil, lastDuration, r.Context().Err()
					}
					continue
				}
				message := upstreamFailureMessage(err) + " " + err.Error()
				_ = s.completeRoute(context.Background(), route, false, 0, message)
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, upstreamFailureCode(err), err.Error(), durationMS)
					break
				}
				return lastRoute, 0, nil, nil, lastDuration, lastErr
			}

			if !signatureRepairTried && s.shouldRetryClaudeSignatureRepair(r.Context(), route, status, responseBody) {
				repairedBody, repaired := repairClaudeThinkingBlocksForRetry(attemptBody)
				if repaired {
					signatureRepairTried = true
					attemptBody = repairedBody
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "anthropic_signature_repair", string(bytes.TrimSpace(responseBody)), durationMS)
					continue
				}
			}

			if isRetryableUpstreamStatusForRoute(route, status) {
				message := string(bytes.TrimSpace(responseBody))
				if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, nil, status, header) {
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "same_account_status", message, durationMS)
					if !sleepBeforeSameAccountRetry(r.Context(), route) {
						return lastRoute, 0, nil, nil, lastDuration, r.Context().Err()
					}
					continue
				}
				_ = s.completeRoute(context.Background(), route, false, status, message)
				s.applyUpstreamHeaderSignals(context.Background(), route, status, header)
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					lastErr = upstreamUnavailable("upstream_retryable", "Upstream returned a retryable error.")
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "retryable_status", message, durationMS)
					break
				}
				return route, status, header, responseBody, durationMS, nil
			}

			_ = s.completeRoute(context.Background(), route, true, status, "")
			s.applyUpstreamHeaderSignals(context.Background(), route, status, header)
			return route, status, header, responseBody, durationMS, nil
		}
	}

	if lastErr != nil {
		return lastRoute, 0, nil, nil, lastDuration, lastErr
	}
	return routeInfo{}, 0, nil, nil, 0, upstreamUnavailable("no_available_account", "No valid upstream account is available.")
}

func (s *Server) callUpstream(r *http.Request, route routeInfo, body []byte) (int, http.Header, []byte, error) {
	req, cancel, err := s.newUpstreamRequest(r, route, body)
	if err != nil {
		return 0, nil, nil, err
	}
	defer cancel()

	client, err := s.upstreamHTTPClient(route)
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := readUpstreamHTTPResponseBody(resp)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header.Clone(), respBody, nil
}

func (s *Server) callClaudeCodeSidecarWithRetry(r *http.Request, body []byte, auth apiKeyAuth) (routeInfo, int, http.Header, []byte, int, error) {
	var lastRoute routeInfo
	var lastDuration int
	var lastErr error
	excludedAccountIDs := []string{}

	maxAttempts := defaultUpstreamMaxAttempts
	for attempt := 0; attempt < maxAttempts; attempt++ {
		route, err := s.checkoutClaudeCodeSidecarRouteExcluding(r.Context(), auth, excludedAccountIDs)
		if err != nil {
			if lastErr != nil {
				return lastRoute, 0, nil, nil, lastDuration, lastErr
			}
			return routeInfo{}, 0, nil, nil, 0, err
		}
		if attempt == 0 {
			maxAttempts = upstreamMaxAttemptsFromRoute(route)
		}
		lastRoute = route

		sameAccountRetries := sameAccountMaxRetriesFromRoute(route)
		for sameAttempt := 0; ; sameAttempt++ {
			startedAt := time.Now()
			status, header, responseBody, err := s.callUpstream(r, route, body)
			durationMS := int(time.Since(startedAt).Milliseconds())
			lastDuration = durationMS
			endpoint := claudeCodeSidecarEndpoint(r.URL.Path)

			if err != nil {
				lastErr = err
				if isRoutePreparationError(err) {
					_ = s.releaseRouteCheckout(context.Background(), route)
					return lastRoute, 0, nil, nil, lastDuration, lastErr
				}
				if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, err, 0, nil) {
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "same_account_error", err.Error(), durationMS)
					if !sleepBeforeSameAccountRetry(r.Context(), route) {
						return lastRoute, 0, nil, nil, lastDuration, r.Context().Err()
					}
					continue
				}
				message := upstreamFailureMessage(err) + " " + err.Error()
				_ = s.completeRoute(context.Background(), route, false, 0, message)
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, upstreamFailureCode(err), err.Error(), durationMS)
					break
				}
				return lastRoute, 0, nil, nil, lastDuration, lastErr
			}

			if isRetryableUpstreamStatusForRoute(route, status) {
				message := string(bytes.TrimSpace(responseBody))
				if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, nil, status, header) {
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "same_account_status", message, durationMS)
					if !sleepBeforeSameAccountRetry(r.Context(), route) {
						return lastRoute, 0, nil, nil, lastDuration, r.Context().Err()
					}
					continue
				}
				_ = s.completeRoute(context.Background(), route, false, status, message)
				s.applyUpstreamHeaderSignals(context.Background(), route, status, header)
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					lastErr = upstreamUnavailable("upstream_retryable", "Upstream returned a retryable error.")
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "retryable_status", message, durationMS)
					break
				}
				return route, status, header, responseBody, durationMS, nil
			}

			_ = s.completeRoute(context.Background(), route, true, status, "")
			s.applyUpstreamHeaderSignals(context.Background(), route, status, header)
			return route, status, header, responseBody, durationMS, nil
		}
	}

	if lastErr != nil {
		return lastRoute, 0, nil, nil, lastDuration, lastErr
	}
	return routeInfo{}, 0, nil, nil, 0, upstreamUnavailable("no_available_account", "No valid upstream account is available.")
}

func (s *Server) streamUpstream(
	w http.ResponseWriter,
	r *http.Request,
	route routeInfo,
	body []byte,
	reqCtx northboundRequestContext,
	reservation reservationInfo,
) {
	settings := s.reverseProxySettingsOrDefault(r.Context())
	startAttempt := 0
	excludedAccountIDs := []string{}

	for {
		attempt, err := s.openStreamWithRetryFromState(r, route, reqCtx.Model.ModelName, reqCtx.Endpoint, body, reqCtx.Auth.UserID, reqCtx.Auth.APIKeyID, reqCtx.Auth.RoutingMode, reqCtx.RouteAffinityKey, reqCtx.RouteAffinityTTL, reqCtx.RouteAffinityRuleName, reqCtx.RouteAffinitySkipRetry, reqCtx.RouteTags, startAttempt, excludedAccountIDs)
		route = attempt.Route
		if err != nil {
			statusCode := errorCode(err)
			if statusCode == "internal_error" {
				statusCode = upstreamFailureCode(err)
			}
			_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
			_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
				RequestID:          reqCtx.RequestID,
				RequestFingerprint: reqCtx.Fingerprint,
				UserID:             reqCtx.Auth.UserID,
				APIKeyID:           reqCtx.Auth.APIKeyID,
				ChannelID:          route.ChannelID,
				AccountID:          route.AccountID,
				RequestedModel:     reqCtx.Requested,
				UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
				Endpoint:           reqCtx.Endpoint,
				EstimatedCost:      reqCtx.Reserve,
				Pricing:            reqCtx.Model,
				Status:             "failed",
				ErrorCode:          statusCode,
				ErrorMessage:       err.Error(),
				DurationMS:         attempt.DurationMS,
			})
			_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
			var appErr appError
			if errors.As(err, &appErr) {
				writeError(w, r, err)
				return
			}
			writeError(w, r, upstreamUnavailable(statusCode, upstreamFailureMessage(err)))
			return
		}

		if attempt.Response == nil {
			respBody := attempt.Body
			if len(respBody) == 0 {
				respBody = []byte(`{"error":{"message":"upstream rejected request"}}`)
			}
			statusCode := upstreamStatusErrorCode(attempt.Status, respBody)
			_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
			_ = s.completeRoute(context.Background(), route, !isRetryableUpstreamStatusForRoute(route, attempt.Status), attempt.Status, string(bytes.TrimSpace(respBody)))
			s.applyUpstreamHeaderSignals(context.Background(), route, attempt.Status, attempt.Header)
			_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
				RequestID:          reqCtx.RequestID,
				RequestFingerprint: reqCtx.Fingerprint,
				UserID:             reqCtx.Auth.UserID,
				APIKeyID:           reqCtx.Auth.APIKeyID,
				ChannelID:          route.ChannelID,
				AccountID:          route.AccountID,
				RequestedModel:     reqCtx.Requested,
				UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
				Endpoint:           reqCtx.Endpoint,
				EstimatedCost:      reqCtx.Reserve,
				Pricing:            reqCtx.Model,
				Status:             "failed",
				ErrorCode:          statusCode,
				ErrorMessage:       string(bytes.TrimSpace(respBody)),
				UpstreamStatus:     attempt.Status,
				DurationMS:         attempt.DurationMS,
				MeteringMetadata:   usageMetadataWithClientStatusMapping(route, attempt.Status, nil),
			})
			_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
			copyRouteResponse(w, route, attempt.Status, attempt.Header, respBody)
			return
		}

		resp := attempt.Response
		streamBody, closeStreamBody, decodeErr := upstreamResponseBodyReader(resp)
		if decodeErr != nil {
			closeStreamAttempt(resp, closeStreamBody, attempt.Cancel)
			if canRetryStreamBootstrap(decodeErr, nil, attempt, reqCtx) {
				startAttempt, excludedAccountIDs = s.prepareStreamBootstrapRetry(r, route, reqCtx.Endpoint, attempt, resp.StatusCode, decodeErr.Error())
				continue
			}
			_ = s.releaseReservation(context.Background(), reservation, "upstream_error", route.AccountID)
			_ = s.completeRoute(context.Background(), route, false, resp.StatusCode, decodeErr.Error())
			_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
			writeError(w, r, upstreamUnavailable("upstream_error", "Upstream response could not be decoded."))
			return
		}

		adapter, adapterErr := providerAdapterFor(route.ProviderType)
		if adapterErr != nil {
			adapter = openaiCompatibleAdapter{}
		}
		streamWriter := newStreamCommitWriter(w, route, resp.StatusCode, resp.Header, time.Duration(settings.StreamKeepAliveSeconds)*time.Second)
		streamAcc := &streamMeteringAccumulator{}
		_, copyErr := copyStreamAndMeterForRoute(streamWriter, streamBody, adapter, route, reqCtx.Endpoint, body, streamAcc)
		if canRetryStreamBootstrap(copyErr, streamWriter, attempt, reqCtx) {
			streamWriter.Close()
			closeStreamAttempt(resp, closeStreamBody, attempt.Cancel)
			startAttempt, excludedAccountIDs = s.prepareStreamBootstrapRetry(r, route, reqCtx.Endpoint, attempt, resp.StatusCode, copyErr.Error())
			continue
		}
		if copyErr != nil && !streamWriter.Committed() && streamWriter.PayloadBytes() == 0 {
			streamWriter.Close()
			closeStreamAttempt(resp, closeStreamBody, attempt.Cancel)
			_ = s.releaseReservation(context.Background(), reservation, "stream_copy_error", route.AccountID)
			_ = s.completeRoute(context.Background(), route, false, resp.StatusCode, copyErr.Error())
			s.applyUpstreamHeaderSignals(context.Background(), route, resp.StatusCode, resp.Header)
			_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
				RequestID:          reqCtx.RequestID,
				RequestFingerprint: reqCtx.Fingerprint,
				UserID:             reqCtx.Auth.UserID,
				APIKeyID:           reqCtx.Auth.APIKeyID,
				ChannelID:          route.ChannelID,
				AccountID:          route.AccountID,
				RequestedModel:     reqCtx.Requested,
				UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
				Endpoint:           reqCtx.Endpoint,
				EstimatedCost:      reqCtx.Reserve,
				Pricing:            reqCtx.Model,
				Status:             "failed",
				ErrorCode:          "stream_copy_error",
				ErrorMessage:       copyErr.Error(),
				UpstreamStatus:     resp.StatusCode,
				DurationMS:         attempt.DurationMS,
				MeteringMetadata:   usageMetadataWithClientStatusMapping(route, resp.StatusCode, nil),
				EffectivePolicy:    reqCtx.Policy,
				RiskDecision:       reqCtx.Risk,
			})
			_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
			writeError(w, r, upstreamUnavailable("stream_copy_error", "Upstream stream interrupted before returning data."))
			return
		}
		if copyErr == nil && !streamWriter.Committed() {
			copyErr = streamWriter.Commit()
		}
		if shouldEmitStreamCopyErrorEvent(copyErr) {
			if _, err := streamWriter.Write(streamCopyErrorEventPayload(route, reqCtx.Endpoint, reqCtx.RequestID)); err != nil {
				slog.DebugContext(r.Context(), "stream copy error event write failed", "error", err, "request_id", reqCtx.RequestID)
			}
		}
		streamWriter.Close()
		closeStreamAttempt(resp, closeStreamBody, attempt.Cancel)

		fallbackMetrics := meteringMetricsFromResponse(reqCtx.Endpoint, body, nil)
		metering := streamAcc.meteringResult(fallbackMetrics, "stream_parsed")
		actualCost := applyBillingMultiplier(calculateActualCost(reqCtx.Model, metering.usageCounts(), metering.meteringMetrics()), reqCtx.Policy)
		if actualCost <= 0 {
			actualCost = reqCtx.Reserve
		}
		actualCost = actualCostForRoutingMode(reqCtx.Auth.RoutingMode, actualCost)
		if settleErr := s.settleReservation(context.Background(), reservation, actualCost, route.AccountID, reqCtx.Policy); settleErr != nil {
			slog.Error("stream settlement failed", "error", settleErr, "request_id", reqCtx.RequestID)
		}
		_ = s.completeRoute(context.Background(), route, true, resp.StatusCode, "")
		s.applyUpstreamHeaderSignals(context.Background(), route, resp.StatusCode, resp.Header)

		status := "success"
		errorCode := ""
		errorMessage := ""
		if copyErr != nil {
			status = "failed"
			errorCode = "stream_copy_error"
			errorMessage = copyErr.Error()
		}
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			InputTokens:        metering.InputTokens,
			OutputTokens:       metering.OutputTokens,
			ImageCount:         metering.ImageCount,
			AudioSeconds:       metering.AudioSeconds,
			RequestCount:       metering.RequestCount,
			EstimatedCost:      reqCtx.Reserve,
			ActualCost:         actualCost,
			Pricing:            reqCtx.Model,
			Status:             status,
			ErrorCode:          errorCode,
			ErrorMessage:       errorMessage,
			UpstreamStatus:     resp.StatusCode,
			DurationMS:         attempt.DurationMS,
			UsageSource:        metering.UsageSource,
			StreamEventCount:   metering.StreamEventCount,
			MeteringMetadata:   usageMetadataWithClientStatusMapping(route, resp.StatusCode, metering.Metadata),
			EffectivePolicy:    reqCtx.Policy,
			RiskDecision:       reqCtx.Risk,
		})
		_, _ = s.db.ExecContext(context.Background(), "UPDATE api_keys SET last_used_at = now() WHERE id = $1", reqCtx.Auth.APIKeyID)
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, status)
		return
	}
}

func canRetryStreamBootstrap(err error, writer *streamCommitWriter, attempt streamOpenResult, reqCtx northboundRequestContext) bool {
	if err == nil {
		return false
	}
	if writer != nil && (writer.Committed() || writer.PayloadBytes() > 0) {
		return false
	}
	if reqCtx.RouteAffinitySkipRetry && strings.TrimSpace(reqCtx.RouteAffinityKey) != "" {
		return false
	}
	if !shouldEmitStreamCopyErrorEvent(err) {
		return false
	}
	return hasRemainingUpstreamAttempts(attempt.Attempt, attempt.MaxAttempts)
}

func (s *Server) prepareStreamBootstrapRetry(r *http.Request, route routeInfo, endpoint string, attempt streamOpenResult, upstreamStatus int, message string) (int, []string) {
	_ = s.completeRoute(context.Background(), route, false, upstreamStatus, message)
	s.applyUpstreamHeaderSignals(context.Background(), route, upstreamStatus, attempt.Header)
	excludedAccountIDs := appendFailedRouteAccountID(attempt.ExcludedAccountIDs, route)
	s.observeUpstreamRetry(r, route, endpoint, attempt.Attempt, attempt.MaxAttempts, upstreamStatus, "stream_bootstrap_error", message, attempt.DurationMS)
	return attempt.Attempt + 1, excludedAccountIDs
}

func closeStreamAttempt(resp *http.Response, bodyCloser io.Closer, cancel context.CancelFunc) {
	if bodyCloser != nil {
		_ = bodyCloser.Close()
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if cancel != nil {
		cancel()
	}
}

type streamCommitWriter struct {
	base      http.ResponseWriter
	route     routeInfo
	status    int
	header    http.Header
	keepAlive time.Duration

	mu        sync.Mutex
	committed bool
	written   int64
	done      chan struct{}
}

func newStreamCommitWriter(base http.ResponseWriter, route routeInfo, status int, header http.Header, keepAlive time.Duration) *streamCommitWriter {
	if keepAlive < 0 {
		keepAlive = 0
	}
	return &streamCommitWriter{base: base, route: route, status: status, header: header, keepAlive: keepAlive}
}

func (writer *streamCommitWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.commitLocked(); err != nil {
		return 0, err
	}
	n, err := writer.base.Write(data)
	writer.written += int64(n)
	if flusher, ok := writer.base.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func (writer *streamCommitWriter) Commit() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.commitLocked()
}

func (writer *streamCommitWriter) Committed() bool {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.committed
}

func (writer *streamCommitWriter) PayloadBytes() int64 {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.written
}

func (writer *streamCommitWriter) Close() {
	writer.mu.Lock()
	done := writer.done
	writer.done = nil
	writer.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (writer *streamCommitWriter) commitLocked() error {
	if writer.committed {
		return nil
	}
	copyHeaders(writer.base, writer.header)
	setStreamingProxyHeaders(writer.base)
	writer.base.WriteHeader(clientResponseStatus(writer.route, writer.status))
	writer.committed = true
	if flusher, ok := writer.base.(http.Flusher); ok {
		flusher.Flush()
	}
	if writer.keepAlive > 0 {
		writer.startKeepAliveLocked()
	}
	return nil
}

func (writer *streamCommitWriter) startKeepAliveLocked() {
	if writer.done != nil {
		return
	}
	done := make(chan struct{})
	writer.done = done
	interval := writer.keepAlive
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writer.mu.Lock()
				if writer.done == nil {
					writer.mu.Unlock()
					return
				}
				if _, err := writer.base.Write([]byte(": keep-alive\n\n")); err != nil {
					writer.done = nil
					writer.mu.Unlock()
					return
				}
				if flusher, ok := writer.base.(http.Flusher); ok {
					flusher.Flush()
				}
				writer.mu.Unlock()
			case <-done:
				return
			}
		}
	}()
}

func (s *Server) proxyRealtimeWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	route routeInfo,
	reqCtx northboundRequestContext,
	reservation reservationInfo,
) {
	startedAt := time.Now()
	attempt, err := s.openWebSocketWithRetry(r, route, reqCtx.Model.ModelName, reqCtx.Endpoint, reqCtx.Auth.UserID, reqCtx.Auth.APIKeyID, reqCtx.Auth.RoutingMode, reqCtx.RouteAffinityKey, reqCtx.RouteAffinityTTL, reqCtx.RouteAffinityRuleName, reqCtx.RouteAffinitySkipRetry, reqCtx.RouteTags)
	route = attempt.Route
	if err != nil {
		statusCode := errorCode(err)
		if statusCode == "internal_error" {
			statusCode = upstreamFailureCode(err)
		}
		_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			EstimatedCost:      reqCtx.Reserve,
			Pricing:            reqCtx.Model,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       err.Error(),
			DurationMS:         attempt.DurationMS,
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		var appErr appError
		if errors.As(err, &appErr) {
			writeError(w, r, err)
			return
		}
		writeError(w, r, upstreamUnavailable(statusCode, upstreamFailureMessage(err)))
		return
	}

	if attempt.UpstreamConn == nil {
		respBody := attempt.Body
		if len(respBody) == 0 {
			respBody = []byte(`{"error":{"message":"upstream rejected websocket request"}}`)
		}
		statusCode := upstreamStatusErrorCode(attempt.Status, respBody)
		_ = s.releaseReservation(context.Background(), reservation, statusCode, route.AccountID)
		_ = s.completeRoute(context.Background(), route, !isRetryableUpstreamStatusForRoute(route, attempt.Status), attempt.Status, string(bytes.TrimSpace(respBody)))
		s.applyUpstreamHeaderSignals(context.Background(), route, attempt.Status, attempt.Header)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			EstimatedCost:      reqCtx.Reserve,
			Pricing:            reqCtx.Model,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       string(bytes.TrimSpace(respBody)),
			UpstreamStatus:     attempt.Status,
			DurationMS:         attempt.DurationMS,
			MeteringMetadata:   usageMetadataWithClientStatusMapping(route, attempt.Status, nil),
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		copyRouteResponse(w, route, attempt.Status, attempt.Header, respBody)
		return
	}
	upstreamConn := attempt.UpstreamConn
	defer upstreamConn.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin:       func(_ *http.Request) bool { return true },
		EnableCompression: true,
	}
	clientConn, err := upgrader.Upgrade(w, r, cloneWebSocketResponseHeaders(attempt.Header))
	if err != nil {
		_ = s.releaseReservation(context.Background(), reservation, "client_upgrade_failed", route.AccountID)
		_ = s.completeRoute(context.Background(), route, true, http.StatusSwitchingProtocols, "")
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			RequestedModel:     reqCtx.Requested,
			UpstreamModel:      upstreamModelForRecord(route, reqCtx.Model),
			Endpoint:           reqCtx.Endpoint,
			EstimatedCost:      reqCtx.Reserve,
			Pricing:            reqCtx.Model,
			Status:             "failed",
			ErrorCode:          "client_websocket_upgrade_failed",
			ErrorMessage:       err.Error(),
			UpstreamStatus:     http.StatusSwitchingProtocols,
			DurationMS:         int(time.Since(startedAt).Milliseconds()),
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		return
	}
	defer clientConn.Close()

	adapter, adapterErr := providerAdapterFor(route.ProviderType)
	if adapterErr != nil {
		adapter = openaiCompatibleAdapter{}
	}
	wsStats := &webSocketMeteringStats{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = copyWebSocketMessages(clientConn, upstreamConn, adapter, reqCtx.Endpoint, wsStats, false, r, route)
		_ = upstreamConn.Close()
	}()
	go func() {
		defer wg.Done()
		if err := copyWebSocketMessages(upstreamConn, clientConn, adapter, reqCtx.Endpoint, wsStats, true, r, route); shouldCloseWebSocketWithUpstreamError(err) {
			_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, "Upstream WebSocket interrupted.")
		}
		_ = clientConn.Close()
	}()
	wg.Wait()

	fallbackMetrics := meteringMetrics{RequestCount: 1}
	wsMetering := wsStats.meteringResult(fallbackMetrics)
	actualCost := applyBillingMultiplier(calculateActualCost(reqCtx.Model, wsMetering.usageCounts(), wsMetering.meteringMetrics()), reqCtx.Policy)
	if !wsStats.hasProviderUsage() || actualCost <= 0 {
		actualCost = reqCtx.Reserve
		if actualCost <= 0 {
			actualCost = applyBillingMultiplier(calculateActualCost(reqCtx.Model, usageCounts{}, fallbackMetrics), reqCtx.Policy)
		}
	}
	actualCost = actualCostForRoutingMode(reqCtx.Auth.RoutingMode, actualCost)
	if settleErr := s.settleReservation(context.Background(), reservation, actualCost, route.AccountID, reqCtx.Policy); settleErr != nil {
		slog.Error("websocket settlement failed", "error", settleErr, "request_id", reqCtx.RequestID)
	}
	_ = s.completeRoute(context.Background(), route, true, http.StatusSwitchingProtocols, "")
	s.applyUpstreamHeaderSignals(context.Background(), route, http.StatusSwitchingProtocols, attempt.Header)
	durationMS := int(time.Since(startedAt).Milliseconds())
	_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
		RequestID:           reqCtx.RequestID,
		RequestFingerprint:  reqCtx.Fingerprint,
		UserID:              reqCtx.Auth.UserID,
		APIKeyID:            reqCtx.Auth.APIKeyID,
		ChannelID:           route.ChannelID,
		AccountID:           route.AccountID,
		RequestedModel:      reqCtx.Requested,
		UpstreamModel:       upstreamModelForRecord(route, reqCtx.Model),
		Endpoint:            reqCtx.Endpoint,
		InputTokens:         wsMetering.InputTokens,
		OutputTokens:        wsMetering.OutputTokens,
		RequestCount:        wsMetering.RequestCount,
		EstimatedCost:       reqCtx.Reserve,
		ActualCost:          actualCost,
		Pricing:             reqCtx.Model,
		Status:              "success",
		UpstreamStatus:      http.StatusSwitchingProtocols,
		DurationMS:          durationMS,
		UsageSource:         wsMetering.UsageSource,
		WebSocketFrameCount: wsMetering.WebSocketFrameCount,
		MeteringMetadata:    wsMetering.Metadata,
		EffectivePolicy:     reqCtx.Policy,
		RiskDecision:        reqCtx.Risk,
	})
	_, _ = s.db.ExecContext(context.Background(), "UPDATE api_keys SET last_used_at = now() WHERE id = $1", reqCtx.Auth.APIKeyID)
	_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "success")
}

func (s *Server) proxyClaudeCodeWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	route routeInfo,
	reqCtx northboundRequestContext,
) {
	startedAt := time.Now()
	attempt, err := s.openWebSocket(r, route)
	route = attempt.Route
	if err != nil {
		statusCode := upstreamFailureCode(err)
		_ = s.completeRoute(context.Background(), route, false, attempt.Status, upstreamFailureMessage(err)+" "+err.Error())
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			ProviderType:       route.ProviderType,
			RequestedModel:     "claude-code",
			UpstreamModel:      "claude-code",
			Endpoint:           reqCtx.Endpoint,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       err.Error(),
			DurationMS:         attempt.DurationMS,
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		writeError(w, r, upstreamUnavailable(statusCode, upstreamFailureMessage(err)))
		return
	}
	if attempt.UpstreamConn == nil {
		respBody := attempt.Body
		if len(respBody) == 0 {
			respBody = []byte(`{"error":{"message":"upstream rejected websocket request"}}`)
		}
		statusCode := upstreamStatusErrorCode(attempt.Status, respBody)
		_ = s.completeRoute(context.Background(), route, !isRetryableUpstreamStatusForRoute(route, attempt.Status), attempt.Status, string(bytes.TrimSpace(respBody)))
		s.applyUpstreamHeaderSignals(context.Background(), route, attempt.Status, attempt.Header)
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			ProviderType:       route.ProviderType,
			RequestedModel:     "claude-code",
			UpstreamModel:      "claude-code",
			Endpoint:           reqCtx.Endpoint,
			RequestCount:       1,
			Status:             "failed",
			ErrorCode:          statusCode,
			ErrorMessage:       string(bytes.TrimSpace(respBody)),
			UpstreamStatus:     attempt.Status,
			DurationMS:         attempt.DurationMS,
			UsageSource:        "sidecar_websocket",
			MeteringMetadata:   usageMetadataWithClientStatusMapping(route, attempt.Status, map[string]any{"path": r.URL.Path}),
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		copyRouteResponse(w, route, attempt.Status, attempt.Header, respBody)
		return
	}
	upstreamConn := attempt.UpstreamConn
	defer upstreamConn.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin:       func(_ *http.Request) bool { return true },
		EnableCompression: true,
	}
	clientConn, err := upgrader.Upgrade(w, r, cloneWebSocketResponseHeaders(attempt.Header))
	if err != nil {
		_ = s.completeRoute(context.Background(), route, true, http.StatusSwitchingProtocols, "")
		_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
			RequestID:          reqCtx.RequestID,
			RequestFingerprint: reqCtx.Fingerprint,
			UserID:             reqCtx.Auth.UserID,
			APIKeyID:           reqCtx.Auth.APIKeyID,
			ChannelID:          route.ChannelID,
			AccountID:          route.AccountID,
			ProviderType:       route.ProviderType,
			RequestedModel:     "claude-code",
			UpstreamModel:      "claude-code",
			Endpoint:           reqCtx.Endpoint,
			Status:             "failed",
			ErrorCode:          "client_websocket_upgrade_failed",
			ErrorMessage:       err.Error(),
			UpstreamStatus:     http.StatusSwitchingProtocols,
			DurationMS:         int(time.Since(startedAt).Milliseconds()),
		})
		_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "failed")
		return
	}
	defer clientConn.Close()

	wsStats := &webSocketMeteringStats{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = copyWebSocketMessages(clientConn, upstreamConn, anthropicAdapter{}, reqCtx.Endpoint, wsStats, false, r, route)
		_ = upstreamConn.Close()
	}()
	go func() {
		defer wg.Done()
		if err := copyWebSocketMessages(upstreamConn, clientConn, anthropicAdapter{}, reqCtx.Endpoint, wsStats, true, r, route); shouldCloseWebSocketWithUpstreamError(err) {
			_ = closeWebSocketWithError(clientConn, websocket.CloseTryAgainLater, "Upstream WebSocket interrupted.")
		}
		_ = clientConn.Close()
	}()
	wg.Wait()

	_ = s.completeRoute(context.Background(), route, true, http.StatusSwitchingProtocols, "")
	s.applyUpstreamHeaderSignals(context.Background(), route, http.StatusSwitchingProtocols, attempt.Header)
	durationMS := int(time.Since(startedAt).Milliseconds())
	metering := wsStats.meteringResult(meteringMetrics{RequestCount: 1})
	_ = s.recordNorthboundUsage(context.Background(), r, usageInput{
		RequestID:           reqCtx.RequestID,
		RequestFingerprint:  reqCtx.Fingerprint,
		UserID:              reqCtx.Auth.UserID,
		APIKeyID:            reqCtx.Auth.APIKeyID,
		ChannelID:           route.ChannelID,
		AccountID:           route.AccountID,
		ProviderType:        route.ProviderType,
		RequestedModel:      "claude-code",
		UpstreamModel:       "claude-code",
		Endpoint:            reqCtx.Endpoint,
		RequestCount:        metering.RequestCount,
		Status:              "success",
		UpstreamStatus:      http.StatusSwitchingProtocols,
		DurationMS:          durationMS,
		UsageSource:         "sidecar_websocket",
		WebSocketFrameCount: metering.WebSocketFrameCount,
		MeteringMetadata:    map[string]any{"path": r.URL.Path},
	})
	_, _ = s.db.ExecContext(context.Background(), "UPDATE api_keys SET last_used_at = now() WHERE id = $1", reqCtx.Auth.APIKeyID)
	_ = s.completeIdempotency(context.Background(), reqCtx.Auth, reqCtx.RequestID, "success")
}

type webSocketMeteringStats struct {
	mu  sync.Mutex
	acc streamMeteringAccumulator

	frameCount int
	byteCount  int64
}

func (stats *webSocketMeteringStats) recordFrame(payload []byte, adapter providerAdapter, endpoint string, parseUsage bool) {
	if stats == nil {
		return
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()

	stats.frameCount++
	stats.byteCount += int64(len(payload))
	if parseUsage && adapter != nil {
		adapter.ParseStreamEvent(endpoint, payload, &stats.acc)
	}
}

func (stats *webSocketMeteringStats) hasProviderUsage() bool {
	if stats == nil {
		return false
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	return stats.acc.HasProviderUsage
}

func (stats *webSocketMeteringStats) meteringResult(fallback meteringMetrics) meteringResult {
	if stats == nil {
		return meteringResult{
			RequestCount: nonZeroRequestCount(fallback.RequestCount),
			UsageSource:  "estimated_fallback",
		}
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()

	result := stats.acc.meteringResult(fallback, "websocket_parsed")
	result.WebSocketFrameCount = stats.frameCount
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["websocket_byte_count"] = stats.byteCount
	return result
}

func copyWebSocketMessages(src *websocket.Conn, dst *websocket.Conn, adapter providerAdapter, endpoint string, stats *webSocketMeteringStats, parseUsage bool, original *http.Request, route routeInfo) error {
	for {
		messageType, payload, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if !parseUsage && messageType == websocket.TextMessage && isCodexOfficialRoute(route) {
			payload = rewriteCodexOfficialWebSocketFrame(original, route, payload)
		}
		stats.recordFrame(payload, adapter, endpoint, parseUsage)
		if err := dst.WriteMessage(messageType, payload); err != nil {
			return err
		}
	}
}

func shouldCloseWebSocketWithUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"use of closed network connection",
		"websocket: close 1000",
		"websocket: close 1001",
		"websocket: close 1005",
	} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	return true
}

func closeWebSocketWithError(conn *websocket.Conn, code int, message string) error {
	if conn == nil {
		return nil
	}
	return conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, truncateForStorage(message, 120)),
		time.Now().Add(1*time.Second),
	)
}

type streamOpenResult struct {
	Route              routeInfo
	Response           *http.Response
	Cancel             context.CancelFunc
	Status             int
	Header             http.Header
	Body               []byte
	DurationMS         int
	Attempt            int
	MaxAttempts        int
	ExcludedAccountIDs []string
}

func (s *Server) openStreamWithRetry(r *http.Request, initial routeInfo, model string, endpoint string, body []byte, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, affinitySkipRetry bool, routeTags []string) (streamOpenResult, error) {
	return s.openStreamWithRetryFromState(r, initial, model, endpoint, body, userID, apiKeyID, routingMode, affinityKey, affinityTTLSeconds, affinityRuleName, affinitySkipRetry, routeTags, 0, nil)
}

func (s *Server) openStreamWithRetryFromState(r *http.Request, initial routeInfo, model string, endpoint string, body []byte, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, affinitySkipRetry bool, routeTags []string, startAttempt int, excludedAccountIDs []string) (streamOpenResult, error) {
	var last streamOpenResult
	var lastErr error
	excludedAccountIDs = append([]string(nil), excludedAccountIDs...)

	maxAttempts := upstreamMaxAttemptsFromRoute(initial)
	if startAttempt < 0 {
		startAttempt = 0
	}
	for attempt := startAttempt; attempt < maxAttempts; attempt++ {
		route := initial
		if attempt > 0 {
			if affinitySkipRetry && strings.TrimSpace(affinityKey) != "" {
				if lastErr != nil {
					return last, lastErr
				}
				return last, upstreamUnavailable("affinity_retry_disabled", "Affinity rule disabled cross-account retry.")
			}
			var err error
			route, err = s.checkoutRouteAttemptExcluding(r.Context(), model, endpoint, userID, apiKeyID, routingMode, affinityKey, affinityTTLSeconds, affinityRuleName, routeTags, excludedAccountIDs, attempt)
			if err != nil {
				if lastErr != nil {
					return last, lastErr
				}
				return streamOpenResult{}, err
			}
		}
		last.Route = route
		last.Attempt = attempt
		last.MaxAttempts = maxAttempts
		last.ExcludedAccountIDs = append([]string(nil), excludedAccountIDs...)

		sameAccountRetries := sameAccountMaxRetriesFromRoute(route)
		attemptBody := body
		signatureRepairTried := false
		for sameAttempt := 0; ; sameAttempt++ {
			req, cancel, err := s.newUpstreamRequest(r, route, attemptBody)
			if err != nil {
				lastErr = err
				if isRoutePreparationError(err) {
					_ = s.releaseRouteCheckout(context.Background(), route)
					return last, lastErr
				}
				_ = s.completeRoute(context.Background(), route, false, 0, err.Error())
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "prepare_request_error", err.Error(), 0)
					break
				}
				return last, lastErr
			}

			client, err := s.upstreamHTTPClient(route)
			if err != nil {
				cancel()
				lastErr = err
				_ = s.completeRoute(context.Background(), route, false, 0, err.Error())
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "client_config_error", err.Error(), 0)
					break
				}
				return last, lastErr
			}

			startedAt := time.Now()
			resp, err := client.Do(req)
			durationMS := int(time.Since(startedAt).Milliseconds())
			last.DurationMS = durationMS
			if err != nil {
				cancel()
				lastErr = err
				if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, err, 0, nil) {
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "same_account_error", err.Error(), durationMS)
					if !sleepBeforeSameAccountRetry(r.Context(), route) {
						return last, r.Context().Err()
					}
					continue
				}
				message := upstreamFailureMessage(err) + " " + err.Error()
				_ = s.completeRoute(context.Background(), route, false, 0, message)
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, upstreamFailureCode(err), err.Error(), durationMS)
					break
				}
				return last, lastErr
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				respBody, readErr := readUpstreamHTTPResponseBody(resp)
				if readErr != nil {
					respBody = []byte(`{"error":{"message":"upstream rejected request"}}`)
				}
				header := resp.Header.Clone()
				_ = resp.Body.Close()
				cancel()

				last = streamOpenResult{
					Route:              route,
					Status:             resp.StatusCode,
					Header:             header,
					Body:               respBody,
					DurationMS:         durationMS,
					Attempt:            attempt,
					MaxAttempts:        maxAttempts,
					ExcludedAccountIDs: append([]string(nil), excludedAccountIDs...),
				}
				if !signatureRepairTried && s.shouldRetryClaudeSignatureRepair(r.Context(), route, resp.StatusCode, respBody) {
					repairedBody, repaired := repairClaudeThinkingBlocksForRetry(attemptBody)
					if repaired {
						signatureRepairTried = true
						attemptBody = repairedBody
						s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, resp.StatusCode, "anthropic_signature_repair", string(bytes.TrimSpace(respBody)), durationMS)
						continue
					}
				}
				if isRetryableUpstreamStatusForRoute(route, resp.StatusCode) {
					message := string(bytes.TrimSpace(respBody))
					if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, nil, resp.StatusCode, header) {
						s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, resp.StatusCode, "same_account_status", message, durationMS)
						if !sleepBeforeSameAccountRetry(r.Context(), route) {
							return last, r.Context().Err()
						}
						continue
					}
					if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
						_ = s.completeRoute(context.Background(), route, false, resp.StatusCode, message)
						s.applyUpstreamHeaderSignals(context.Background(), route, resp.StatusCode, header)
						lastErr = upstreamUnavailable("upstream_retryable", "Upstream returned a retryable error.")
						excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
						s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, resp.StatusCode, "retryable_status", message, durationMS)
						break
					}
				}
				return last, nil
			}

			return streamOpenResult{
				Route:              route,
				Response:           resp,
				Cancel:             cancel,
				Status:             resp.StatusCode,
				Header:             resp.Header.Clone(),
				DurationMS:         durationMS,
				Attempt:            attempt,
				MaxAttempts:        maxAttempts,
				ExcludedAccountIDs: append([]string(nil), excludedAccountIDs...),
			}, nil
		}
	}

	if lastErr != nil {
		return last, lastErr
	}
	return last, upstreamUnavailable("no_available_account", "No valid upstream account is available.")
}

type webSocketOpenResult struct {
	Route        routeInfo
	UpstreamConn *websocket.Conn
	Status       int
	Header       http.Header
	Body         []byte
	DurationMS   int
}

func (s *Server) openWebSocketWithRetry(r *http.Request, initial routeInfo, model string, endpoint string, userID string, apiKeyID string, routingMode string, affinityKey string, affinityTTLSeconds int, affinityRuleName string, affinitySkipRetry bool, routeTags []string) (webSocketOpenResult, error) {
	var last webSocketOpenResult
	var lastErr error
	excludedAccountIDs := []string{}

	maxAttempts := upstreamMaxAttemptsFromRoute(initial)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		route := initial
		if attempt > 0 {
			if affinitySkipRetry && strings.TrimSpace(affinityKey) != "" {
				if lastErr != nil {
					return last, lastErr
				}
				return last, upstreamUnavailable("affinity_retry_disabled", "Affinity rule disabled cross-account retry.")
			}
			var err error
			route, err = s.checkoutRouteAttemptExcluding(r.Context(), model, endpoint, userID, apiKeyID, routingMode, affinityKey, affinityTTLSeconds, affinityRuleName, routeTags, excludedAccountIDs, attempt)
			if err != nil {
				if lastErr != nil {
					return last, lastErr
				}
				return webSocketOpenResult{}, err
			}
		}
		last.Route = route

		upstreamURL, headers, dialer, err := s.newUpstreamWebSocketDial(r, route)
		if err != nil {
			lastErr = err
			if isRoutePreparationError(err) {
				_ = s.releaseRouteCheckout(context.Background(), route)
				return last, lastErr
			}
			_ = s.completeRoute(context.Background(), route, false, 0, err.Error())
			if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
				excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
				s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "prepare_websocket_error", err.Error(), 0)
				continue
			}
			return last, lastErr
		}

		sameAccountRetries := sameAccountMaxRetriesFromRoute(route)
		for sameAttempt := 0; ; sameAttempt++ {
			startedAt := time.Now()
			conn, response, err := dialer.DialContext(r.Context(), upstreamURL, headers)
			durationMS := int(time.Since(startedAt).Milliseconds())
			last.DurationMS = durationMS
			if err != nil {
				status := 0
				header := http.Header{}
				body := []byte(nil)
				if response != nil {
					status = response.StatusCode
					header = response.Header.Clone()
					if response.Body != nil {
						var readErr error
						body, readErr = readUpstreamHTTPResponseBody(response)
						if readErr != nil {
							body = []byte(`{"error":{"message":"upstream rejected websocket request"}}`)
						}
						_ = response.Body.Close()
					}
				}
				last = webSocketOpenResult{
					Route:      route,
					Status:     status,
					Header:     header,
					Body:       body,
					DurationMS: durationMS,
				}
				if status != 0 && isRetryableUpstreamStatusForRoute(route, status) {
					message := string(bytes.TrimSpace(body))
					if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, nil, status, header) {
						s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "same_account_status", message, durationMS)
						if !sleepBeforeSameAccountRetry(r.Context(), route) {
							return last, r.Context().Err()
						}
						continue
					}
					if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
						_ = s.completeRoute(context.Background(), route, false, status, message)
						s.applyUpstreamHeaderSignals(context.Background(), route, status, header)
						lastErr = upstreamUnavailable("upstream_retryable", "Upstream returned a retryable WebSocket error.")
						excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
						s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, status, "retryable_status", message, durationMS)
						break
					}
				}
				if status != 0 {
					return last, nil
				}
				lastErr = err
				if sameAttempt < sameAccountRetries && shouldRetrySameAccount(route, err, 0, nil) {
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, "same_account_error", err.Error(), durationMS)
					if !sleepBeforeSameAccountRetry(r.Context(), route) {
						return last, r.Context().Err()
					}
					continue
				}
				message := upstreamFailureMessage(err) + " " + err.Error()
				_ = s.completeRoute(context.Background(), route, false, 0, message)
				if hasRemainingUpstreamAttempts(attempt, maxAttempts) {
					excludedAccountIDs = appendFailedRouteAccountID(excludedAccountIDs, route)
					s.observeUpstreamRetry(r, route, endpoint, attempt, maxAttempts, 0, upstreamFailureCode(err), err.Error(), durationMS)
					break
				}
				return last, lastErr
			}

			header := http.Header{}
			if response != nil {
				header = response.Header.Clone()
			}
			return webSocketOpenResult{
				Route:        route,
				UpstreamConn: conn,
				Status:       http.StatusSwitchingProtocols,
				Header:       header,
				DurationMS:   durationMS,
			}, nil
		}
	}

	if lastErr != nil {
		return last, lastErr
	}
	return last, upstreamUnavailable("no_available_account", "No valid upstream account is available.")
}

func (s *Server) openWebSocket(r *http.Request, route routeInfo) (webSocketOpenResult, error) {
	upstreamURL, headers, dialer, err := s.newUpstreamWebSocketDial(r, route)
	if err != nil {
		return webSocketOpenResult{Route: route}, err
	}
	startedAt := time.Now()
	conn, response, err := dialer.DialContext(r.Context(), upstreamURL, headers)
	durationMS := int(time.Since(startedAt).Milliseconds())
	if err != nil {
		status := 0
		header := http.Header{}
		body := []byte(nil)
		if response != nil {
			status = response.StatusCode
			header = response.Header.Clone()
			if response.Body != nil {
				var readErr error
				body, readErr = readUpstreamHTTPResponseBody(response)
				if readErr != nil {
					body = []byte(`{"error":{"message":"upstream rejected websocket request"}}`)
				}
				_ = response.Body.Close()
			}
		}
		result := webSocketOpenResult{
			Route:      route,
			Status:     status,
			Header:     header,
			Body:       body,
			DurationMS: durationMS,
		}
		if status != 0 {
			return result, nil
		}
		return result, err
	}

	header := http.Header{}
	if response != nil {
		header = response.Header.Clone()
	}
	return webSocketOpenResult{
		Route:        route,
		UpstreamConn: conn,
		Status:       http.StatusSwitchingProtocols,
		Header:       header,
		DurationMS:   durationMS,
	}, nil
}

func (s *Server) newUpstreamRequest(r *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	adapter, err := providerAdapterFor(route.ProviderType)
	if err != nil {
		return nil, func() {}, err
	}
	body, err = s.applyReverseProxyParamOverrides(r.Context(), r, route, body)
	if err != nil {
		return nil, func() {}, err
	}
	req, cancel, err := adapter.PrepareRequest(r, route, body)
	if err != nil {
		return nil, cancel, err
	}
	if err := applyCustomEndpointMapping(r, route, req); err != nil {
		cancel()
		return nil, func() {}, err
	}
	if err := s.applyReverseProxyHeaderPolicies(r.Context(), r, req.Header, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func (s *Server) newUpstreamWebSocketDial(r *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	adapter, err := providerAdapterFor(route.ProviderType)
	if err != nil {
		return "", nil, nil, err
	}
	upstreamURL, header, dialer, err := adapter.PrepareWebSocket(r, route)
	if err != nil {
		return "", nil, nil, err
	}
	upstreamURL, err = applyCustomWebSocketEndpointMapping(r, route, upstreamURL)
	if err != nil {
		return "", nil, nil, err
	}
	if err := s.applyReverseProxyHeaderPolicies(r.Context(), r, header, route); err != nil {
		return "", nil, nil, err
	}
	return upstreamURL, header, dialer, nil
}

func websocketScheme(scheme string) string {
	if scheme == "https" || scheme == "wss" {
		return "wss"
	}
	return "ws"
}

func rewriteRequestModel(body []byte, contentType string, upstreamModel string) []byte {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return body
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	if _, ok := payload["model"]; !ok {
		return body
	}
	payload["model"] = upstreamModel
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return rewritten
}

func upstreamModelForRecord(route routeInfo, model modelInfo) string {
	if strings.TrimSpace(route.UpstreamModel) != "" {
		return route.UpstreamModel
	}
	return model.ModelName
}

func (s *Server) upstreamHTTPClient(route routeInfo) (*http.Client, error) {
	if s.upstreamPool == nil {
		s.upstreamPool = newUpstreamClientPool()
	}
	requiresPooledTransport := strings.TrimSpace(route.ProxyURL) != "" || routeTLSFingerprintProfile(route) != ""
	if strings.TrimSpace(route.ProxyURL) == "" {
		if isCodexOfficialRoute(route) {
			if s.httpClient != nil && route.BaseURL != "" && !isOpenAIHost(route.BaseURL) {
				return s.httpClient, nil
			}
			return s.upstreamPool.client(route, true)
		}
		if requiresPooledTransport {
			return s.upstreamPool.client(route, false)
		}
		return s.httpClient, nil
	}
	if isCodexOfficialRoute(route) {
		return s.upstreamPool.client(route, true)
	}
	return s.upstreamPool.client(route, false)
}

func isOpenAIHost(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.openai.com" || host == "chatgpt.com" || strings.HasSuffix(host, ".openai.com") || strings.HasSuffix(host, ".chatgpt.com")
}

type usageCounts struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

func parseUsage(body []byte) usageCounts {
	var payload struct {
		Usage struct {
			PromptTokens             int `json:"prompt_tokens"`
			CompletionTokens         int `json:"completion_tokens"`
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			InputTokensDetails       struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return usageCounts{}
	}
	input := payload.Usage.InputTokens
	if input == 0 {
		input = payload.Usage.PromptTokens
	}
	output := payload.Usage.OutputTokens
	if output == 0 {
		output = payload.Usage.CompletionTokens
	}
	cacheRead := payload.Usage.CacheReadInputTokens
	if cacheRead == 0 {
		cacheRead = payload.Usage.InputTokensDetails.CachedTokens
	}
	if cacheRead == 0 {
		cacheRead = payload.Usage.PromptTokensDetails.CachedTokens
	}
	return usageCounts{InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead, CacheWriteTokens: payload.Usage.CacheCreationInputTokens}
}

func calculateActualCost(model modelInfo, usage usageCounts, metrics meteringMetrics) float64 {
	requestCount := metrics.RequestCount
	if requestCount <= 0 {
		requestCount = 1
	}
	if strings.EqualFold(model.BillingMode, "tiered_expr") && strings.TrimSpace(model.BillingExpr) != "" {
		if total, err := evaluateBillingExpression(model.BillingExpr, billingExpressionVars(usage, metrics)); err == nil {
			return total
		}
	}
	inputTokens := usage.InputTokens
	if inputTokens <= 0 {
		inputTokens = metrics.InputTokens
	}
	outputTokens := usage.OutputTokens
	if outputTokens <= 0 {
		outputTokens = metrics.OutputTokens
	}
	tokenCost := (float64(inputTokens)/1000)*model.InputUSDPer1K + (float64(outputTokens)/1000)*model.OutputUSDPer1K
	cacheCost := (float64(usage.CacheReadTokens)/1000)*model.CacheReadUSDPer1K + (float64(usage.CacheWriteTokens)/1000)*model.CacheWriteUSDPer1K
	unitCost := float64(metrics.ImageCount)*model.ImageUSDPerUnit + metrics.AudioSeconds*model.AudioUSDPerSecond
	total := (model.RequestUSD * float64(requestCount)) + tokenCost + cacheCost + unitCost
	minimum := model.MinChargeUSD * float64(requestCount)
	if total < minimum {
		total = minimum
	}
	return total
}

func actualCostForRoutingMode(routingMode string, actualCost float64) float64 {
	if routingMode == "byo" {
		return 0
	}
	return actualCost
}

type meteringMetrics struct {
	InputTokens  int
	OutputTokens int
	ImageCount   int
	AudioSeconds float64
	RequestCount int
}

func meteringMetricsFromResponse(endpoint string, requestBody []byte, responseBody []byte) meteringMetrics {
	metrics := meteringMetrics{RequestCount: 1}
	var requestPayload map[string]any
	_ = json.Unmarshal(requestBody, &requestPayload)

	if requestPayload != nil {
		if n := positiveIntField(requestPayload, "n"); n > 0 {
			metrics.RequestCount = n
		}
	}

	switch endpoint {
	case "images":
		metrics.ImageCount = metrics.RequestCount
		var responsePayload struct {
			Data []any `json:"data"`
		}
		if err := json.Unmarshal(responseBody, &responsePayload); err == nil && len(responsePayload.Data) > 0 {
			metrics.ImageCount = len(responsePayload.Data)
			metrics.RequestCount = len(responsePayload.Data)
		}
	case "audio":
		if requestPayload != nil {
			metrics.InputTokens = len(requestBody) / 4
		}
	case "embeddings", "rerank":
		metrics.InputTokens = len(requestBody) / 4
	case "chat", "responses", "messages":
		metrics.InputTokens = len(requestBody) / 4
	}
	if metrics.InputTokens < 1 && len(bytes.TrimSpace(requestBody)) > 0 && endpoint != "images" {
		metrics.InputTokens = 1
	}
	return metrics
}

func copyResponse(w http.ResponseWriter, status int, header http.Header, body []byte) {
	copyHeaders(w, header)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

const upstreamResponseReadMaxBytes int64 = 64 << 20

var errUpstreamResponseTooLarge = errors.New("upstream response body too large")

func readUpstreamHTTPResponseBody(resp *http.Response) ([]byte, error) {
	reader, closer, err := upstreamResponseBodyReader(resp)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		defer closer.Close()
	}
	return readUpstreamResponseBody(reader)
}

func upstreamResponseBodyReader(resp *http.Response) (io.Reader, io.Closer, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil, errors.New("upstream response body is nil")
	}
	if resp.Uncompressed {
		return resp.Body, nil, nil
	}
	encodings := contentEncodingTokens(resp.Header.Get("Content-Encoding"))
	if len(encodings) == 0 {
		return resp.Body, nil, nil
	}
	return contentEncodedReader(resp.Body, encodings, "upstream")
}

func contentEncodedReader(reader io.Reader, encodings []string, direction string) (io.Reader, io.Closer, error) {
	closers := []io.Closer{}
	for i := len(encodings) - 1; i >= 0; i-- {
		next, closer, err := wrapContentEncodedReader(reader, encodings[i], direction)
		if err != nil {
			_ = closeAll(closers)
			return nil, nil, err
		}
		reader = next
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	return reader, multiCloser(closers), nil
}

func wrapContentEncodedReader(reader io.Reader, encoding string, direction string) (io.Reader, io.Closer, error) {
	switch encoding {
	case "gzip":
		next, err := gzip.NewReader(reader)
		return next, next, err
	case "deflate":
		next, err := zlib.NewReader(reader)
		return next, next, err
	case "zstd":
		next, err := zstd.NewReader(reader)
		if err != nil {
			return nil, nil, err
		}
		return next, closeFunc(func() error {
			next.Close()
			return nil
		}), nil
	case "br":
		return brotli.NewReader(reader), nil, nil
	default:
		if direction == "request" {
			return nil, nil, badRequest("Unsupported request content encoding.")
		}
		return nil, nil, errors.New("unsupported upstream content encoding: " + encoding)
	}
}

type closeFunc func() error

func (fn closeFunc) Close() error {
	return fn()
}

type multiCloser []io.Closer

func (closers multiCloser) Close() error {
	return closeAll(closers)
}

func closeAll(closers []io.Closer) error {
	var firstErr error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func readUpstreamResponseBody(reader io.Reader) ([]byte, error) {
	return readUpstreamResponseBodyLimited(reader, upstreamResponseReadMaxBytes)
}

func readUpstreamResponseBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("upstream response body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = upstreamResponseReadMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errUpstreamResponseTooLarge
	}
	return body, nil
}

var proxyHopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

var proxyBlockedRequestHeaders = map[string]struct{}{
	"Authorization":            {},
	"Proxy-Authorization":      {},
	"Cookie":                   {},
	"X-Api-Key":                {},
	"X-Goog-Api-Key":           {},
	"Anthropic-Api-Key":        {},
	"Openai-Organization":      {},
	"Openai-Project":           {},
	"Content-Length":           {},
	"Accept-Encoding":          {},
	"Sec-Websocket-Accept":     {},
	"Sec-Websocket-Extensions": {},
	"Sec-Websocket-Key":        {},
	"Sec-Websocket-Protocol":   {},
	"Sec-Websocket-Version":    {},
	"X-Elucid-Relay-Session":   {},
	"X-Relay-Session":          {},
	"X-Subrouter-Session":      {},
	"X-Subrouter-Agent":        {},
	"X-Subrouter-User-Email":   {},
	"X-Subrouter-User":         {},
	"X-Subrouter-Account-Id":   {},
	"X-Subrouter-Account":      {},
	"X-User-Email":             {},
	"Cdn-Loop":                 {},
	"Cf-Access-Client-Id":      {},
	"Cf-Access-Client-Secret":  {},
	"Cf-Connecting-Ip":         {},
	"Cf-Ipcountry":             {},
	"Cf-Ray":                   {},
	"Cf-Visitor":               {},
	"Fastly-Client-Ip":         {},
	"Forwarded":                {},
	"True-Client-Ip":           {},
	"Via":                      {},
	"X-Forwarded-For":          {},
	"X-Forwarded-Host":         {},
	"X-Forwarded-Port":         {},
	"X-Forwarded-Proto":        {},
	"X-Forwarded-Protocol":     {},
	"X-Forwarded-Server":       {},
	"X-Forwarded-Ssl":          {},
	"X-Original-Forwarded-For": {},
	"X-Real-Ip":                {},
	"X-Request-Start":          {},
}

var proxyBlockedResponseHeaders = map[string]struct{}{
	"Authorization":            {},
	"Proxy-Authenticate":       {},
	"Proxy-Authorization":      {},
	"Set-Cookie":               {},
	"X-Api-Key":                {},
	"X-Goog-Api-Key":           {},
	"Anthropic-Api-Key":        {},
	"Content-Length":           {},
	"Content-Encoding":         {},
	"Sec-Websocket-Accept":     {},
	"Sec-Websocket-Extensions": {},
	"Sec-Websocket-Key":        {},
	"Sec-Websocket-Protocol":   {},
	"Sec-Websocket-Version":    {},
}

var gatewayHeaderPrefixes = []string{
	"x-litellm-",
	"helicone-",
	"x-portkey-",
	"cf-aig-",
	"x-kong-",
	"x-bt-",
}

var defaultUpstreamRequestHeaders = map[string]struct{}{
	"Accept":            {},
	"Content-Type":      {},
	"Openai-Beta":       {},
	"Anthropic-Version": {},
	"Anthropic-Beta":    {},
}

var proxyBlockedRouteOverrideHeaders = map[string]struct{}{
	"Host":                {},
	"Content-Length":      {},
	"Accept-Encoding":     {},
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

type routeHeaderRules struct {
	Set          map[string]string
	PassExact    []string
	PassPatterns []*regexp.Regexp
	Remove       []string
	PassAllSafe  bool
}

func copyUpstreamRequestHeaders(dst http.Header, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	connectionScoped := connectionScopedHeaders(src)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(key)
		if _, ok := defaultUpstreamRequestHeaders[canonicalKey]; !ok {
			continue
		}
		if proxyHeaderBlocked(canonicalKey, proxyBlockedRequestHeaders, connectionScoped) {
			continue
		}
		if hasGatewayHeaderPrefix(canonicalKey) {
			continue
		}
		for _, value := range values {
			dst.Add(canonicalKey, value)
		}
	}
}

func applyRouteHeaderRules(req *http.Request, original *http.Request, route routeInfo) error {
	rules, err := routeHeaderRulesFromMetadata(route.ChannelMeta, route.AbilityMeta, route.AccountMeta)
	if err != nil {
		return err
	}
	if rules.empty() {
		return nil
	}

	connectionScoped := connectionScopedHeaders(original.Header)
	if rules.PassAllSafe {
		passSafeRequestHeaders(req.Header, original.Header, connectionScoped)
	}
	for _, key := range rules.PassExact {
		passRequestHeader(req.Header, original.Header, key, connectionScoped)
	}
	if len(rules.PassPatterns) > 0 {
		for key := range original.Header {
			canonicalKey := http.CanonicalHeaderKey(key)
			for _, pattern := range rules.PassPatterns {
				if pattern.MatchString(canonicalKey) || pattern.MatchString(strings.ToLower(canonicalKey)) {
					passRequestHeader(req.Header, original.Header, canonicalKey, connectionScoped)
					break
				}
			}
		}
	}
	for key, value := range rules.Set {
		resolved := resolveRouteHeaderValue(value, original, route)
		setSafeOverrideHeader(req.Header, key, resolved)
	}
	for _, key := range rules.Remove {
		req.Header.Del(http.CanonicalHeaderKey(key))
	}
	return nil
}

func (rules routeHeaderRules) empty() bool {
	return len(rules.Set) == 0 &&
		len(rules.PassExact) == 0 &&
		len(rules.PassPatterns) == 0 &&
		len(rules.Remove) == 0 &&
		!rules.PassAllSafe
}

func routeHeaderRulesFromMetadata(raw ...string) (routeHeaderRules, error) {
	merged := routeHeaderRules{Set: map[string]string{}}
	for _, item := range raw {
		rules, err := decodeRouteHeaderRules(item)
		if err != nil {
			return routeHeaderRules{}, err
		}
		mergeRouteHeaderRules(&merged, rules)
	}
	return merged, nil
}

func decodeRouteHeaderRules(raw string) (routeHeaderRules, error) {
	rules := routeHeaderRules{Set: map[string]string{}}
	if strings.TrimSpace(raw) == "" {
		return rules, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return routeHeaderRules{}, upstreamUnavailable("invalid_route_header_rules", "Route metadata contains invalid JSON.")
	}

	if headers, ok := objectValue(metadata["request_headers"]); ok {
		if err := mergeDecodedHeaderMap(rules.Set, headers["set"]); err != nil {
			return routeHeaderRules{}, err
		}
		if err := mergeDecodedHeaderMap(rules.Set, headers["override"]); err != nil {
			return routeHeaderRules{}, err
		}
		if err := mergeDecodedHeaderMap(rules.Set, headers["overrides"]); err != nil {
			return routeHeaderRules{}, err
		}
		pass, passAll, err := decodeHeaderPassList(firstPresent(headers, "pass", "pass_headers", "passthrough", "passthrough_headers", "allow"))
		if err != nil {
			return routeHeaderRules{}, err
		}
		rules.PassAllSafe = rules.PassAllSafe || passAll
		rules.PassExact = append(rules.PassExact, pass.exact...)
		rules.PassPatterns = append(rules.PassPatterns, pass.patterns...)
		remove, err := decodeHeaderList(firstPresent(headers, "remove", "remove_headers", "drop", "drop_headers"))
		if err != nil {
			return routeHeaderRules{}, err
		}
		rules.Remove = append(rules.Remove, remove...)
	}

	if err := mergeDecodedHeaderMap(rules.Set, metadata["header_overrides"]); err != nil {
		return routeHeaderRules{}, err
	}
	if err := mergeDecodedHeaderMap(rules.Set, metadata["headers"]); err != nil {
		return routeHeaderRules{}, err
	}
	pass, passAll, err := decodeHeaderPassList(firstPresent(metadata, "pass_headers", "passthrough_headers"))
	if err != nil {
		return routeHeaderRules{}, err
	}
	rules.PassAllSafe = rules.PassAllSafe || passAll
	rules.PassExact = append(rules.PassExact, pass.exact...)
	rules.PassPatterns = append(rules.PassPatterns, pass.patterns...)
	remove, err := decodeHeaderList(metadata["remove_headers"])
	if err != nil {
		return routeHeaderRules{}, err
	}
	rules.Remove = append(rules.Remove, remove...)
	return rules, nil
}

type decodedPassHeaders struct {
	exact    []string
	patterns []*regexp.Regexp
}

func mergeDecodedHeaderMap(dst map[string]string, value any) error {
	items, err := decodeHeaderMap(value)
	if err != nil {
		return err
	}
	for key, item := range items {
		dst[key] = item
	}
	return nil
}

func decodeHeaderMap(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := objectValue(value)
	if !ok {
		return nil, upstreamUnavailable("invalid_route_header_rules", "Route header override rules must be an object.")
	}
	decoded := map[string]string{}
	for key, item := range raw {
		headerName := strings.TrimSpace(key)
		if headerName == "" {
			continue
		}
		switch typed := item.(type) {
		case string:
			decoded[http.CanonicalHeaderKey(headerName)] = typed
		case float64, bool:
			decoded[http.CanonicalHeaderKey(headerName)] = strings.TrimSpace(toJSONScalar(typed))
		default:
			return nil, upstreamUnavailable("invalid_route_header_rules", "Route header override values must be scalar.")
		}
	}
	return decoded, nil
}

func decodeHeaderPassList(value any) (decodedPassHeaders, bool, error) {
	headers, err := decodeHeaderList(value)
	if err != nil {
		return decodedPassHeaders{}, false, err
	}
	var decoded decodedPassHeaders
	passAll := false
	for _, item := range headers {
		if item == "*" {
			passAll = true
			continue
		}
		if pattern, ok := strings.CutPrefix(item, "regex:"); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return decodedPassHeaders{}, false, upstreamUnavailable("invalid_route_header_rules", "Route header pass regex is invalid.")
			}
			decoded.patterns = append(decoded.patterns, compiled)
			continue
		}
		if pattern, ok := strings.CutPrefix(item, "re:"); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return decodedPassHeaders{}, false, upstreamUnavailable("invalid_route_header_rules", "Route header pass regex is invalid.")
			}
			decoded.patterns = append(decoded.patterns, compiled)
			continue
		}
		decoded.exact = append(decoded.exact, http.CanonicalHeaderKey(item))
	}
	return decoded, passAll, nil
}

func decodeHeaderList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case []any:
		items := []string{}
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, upstreamUnavailable("invalid_route_header_rules", "Route header lists must contain only strings.")
			}
			text = strings.TrimSpace(text)
			if text != "" {
				items = append(items, text)
			}
		}
		return items, nil
	case []string:
		items := []string{}
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				items = append(items, item)
			}
		}
		return items, nil
	case string:
		typed = strings.TrimSpace(typed)
		if typed == "" {
			return nil, nil
		}
		return []string{typed}, nil
	default:
		return nil, upstreamUnavailable("invalid_route_header_rules", "Route header list rules must be strings or arrays.")
	}
}

func mergeRouteHeaderRules(dst *routeHeaderRules, src routeHeaderRules) {
	if dst.Set == nil {
		dst.Set = map[string]string{}
	}
	for key, value := range src.Set {
		dst.Set[key] = value
	}
	dst.PassExact = append(dst.PassExact, src.PassExact...)
	dst.PassPatterns = append(dst.PassPatterns, src.PassPatterns...)
	dst.Remove = append(dst.Remove, src.Remove...)
	dst.PassAllSafe = dst.PassAllSafe || src.PassAllSafe
}

func firstPresent(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func objectValue(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	if typed, ok := value.(map[string]any); ok {
		return typed, true
	}
	return nil, false
}

var requestHeaderPlaceholderPattern = regexp.MustCompile(`\{(?:client_header|request_header):([^}]+)\}`)
var routeHeaderPlaceholderPattern = regexp.MustCompile(`\{route:([^}]+)\}`)

func resolveRouteHeaderValue(value string, original *http.Request, route routeInfo) string {
	resolved := strings.ReplaceAll(value, "{api_key}", route.APIKey)
	resolved = requestHeaderPlaceholderPattern.ReplaceAllStringFunc(resolved, func(match string) string {
		parts := requestHeaderPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		if original == nil {
			return ""
		}
		return original.Header.Get(strings.TrimSpace(parts[1]))
	})
	return routeHeaderPlaceholderPattern.ReplaceAllStringFunc(resolved, func(match string) string {
		parts := routeHeaderPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		switch strings.ToLower(strings.TrimSpace(parts[1])) {
		case "account_id":
			return route.AccountID
		case "channel_id":
			return route.ChannelID
		case "provider_type":
			return route.ProviderType
		case "upstream_model":
			return route.UpstreamModel
		case "base_url":
			return route.BaseURL
		default:
			return ""
		}
	})
}

func setSafeOverrideHeader(header http.Header, key string, value string) {
	canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
	if canonicalKey == "" {
		return
	}
	if proxyHeaderBlocked(canonicalKey, proxyBlockedRouteOverrideHeaders, nil) {
		return
	}
	if hasGatewayHeaderPrefix(canonicalKey) {
		return
	}
	header.Set(canonicalKey, value)
}

func passSafeRequestHeaders(dst http.Header, src http.Header, connectionScoped map[string]struct{}) {
	for key := range src {
		passRequestHeader(dst, src, key, connectionScoped)
	}
}

func passRequestHeader(dst http.Header, src http.Header, key string, connectionScoped map[string]struct{}) {
	canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
	if canonicalKey == "" {
		return
	}
	if proxyHeaderBlocked(canonicalKey, proxyBlockedRequestHeaders, connectionScoped) {
		return
	}
	if hasGatewayHeaderPrefix(canonicalKey) {
		return
	}
	values := src.Values(canonicalKey)
	if len(values) == 0 {
		return
	}
	dst.Del(canonicalKey)
	for _, value := range values {
		dst.Add(canonicalKey, value)
	}
}

func stripWebSocketDialHeaders(headers http.Header) {
	for _, key := range []string{
		"Connection",
		"Upgrade",
		"Host",
		"Sec-Websocket-Accept",
		"Sec-Websocket-Extensions",
		"Sec-Websocket-Key",
		"Sec-Websocket-Protocol",
		"Sec-Websocket-Version",
		"Sec-WebSocket-Accept",
		"Sec-WebSocket-Extensions",
		"Sec-WebSocket-Key",
		"Sec-WebSocket-Protocol",
		"Sec-WebSocket-Version",
	} {
		headers.Del(key)
	}
}

func toJSONScalar(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(body)
}

func copyHeaders(w http.ResponseWriter, header http.Header) {
	if header == nil {
		return
	}
	connectionScoped := connectionScopedHeaders(header)
	dst := w.Header()
	for key, values := range header {
		canonicalKey := http.CanonicalHeaderKey(key)
		if proxyHeaderBlocked(canonicalKey, proxyBlockedResponseHeaders, connectionScoped) {
			continue
		}
		if hasGatewayHeaderPrefix(canonicalKey) {
			continue
		}
		if dst.Get(canonicalKey) != "" {
			continue
		}
		for _, value := range values {
			dst.Add(canonicalKey, value)
		}
	}
}

func cloneWebSocketResponseHeaders(header http.Header) http.Header {
	out := http.Header{}
	if header == nil {
		return out
	}
	connectionScoped := connectionScopedHeaders(header)
	for key, values := range header {
		canonicalKey := http.CanonicalHeaderKey(key)
		if proxyHeaderBlocked(canonicalKey, proxyBlockedResponseHeaders, connectionScoped) {
			continue
		}
		if hasGatewayHeaderPrefix(canonicalKey) {
			continue
		}
		for _, value := range values {
			out.Add(canonicalKey, value)
		}
	}
	return out
}

func connectionScopedHeaders(header http.Header) map[string]struct{} {
	scoped := make(map[string]struct{})
	for _, rawValue := range header.Values("Connection") {
		for _, token := range strings.Split(rawValue, ",") {
			headerName := strings.TrimSpace(token)
			if headerName == "" {
				continue
			}
			scoped[http.CanonicalHeaderKey(headerName)] = struct{}{}
		}
	}
	return scoped
}

func proxyHeaderBlocked(key string, blocked map[string]struct{}, connectionScoped map[string]struct{}) bool {
	if _, ok := proxyHopByHopHeaders[key]; ok {
		return true
	}
	if _, ok := blocked[key]; ok {
		return true
	}
	if _, ok := connectionScoped[key]; ok {
		return true
	}
	return false
}

func hasGatewayHeaderPrefix(key string) bool {
	lowerKey := strings.ToLower(key)
	for _, prefix := range gatewayHeaderPrefixes {
		if strings.HasPrefix(lowerKey, prefix) {
			return true
		}
	}
	return false
}

func isRetryableUpstreamStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func setStreamingProxyHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("X-Accel-Buffering", "no")
}

func copyStreamAndMeter(dst io.Writer, src io.Reader, adapter providerAdapter, endpoint string, acc *streamMeteringAccumulator) (int64, error) {
	return copyStreamAndMeterForRoute(dst, src, adapter, routeInfo{}, endpoint, nil, acc)
}

func copyStreamAndMeterForRoute(dst io.Writer, src io.Reader, adapter providerAdapter, route routeInfo, endpoint string, requestBody []byte, acc *streamMeteringAccumulator) (int64, error) {
	if transformer := newProviderRawStreamTransformer(adapter, route, endpoint, requestBody); transformer != nil {
		return copyTransformedRawStreamAndMeter(dst, src, adapter, endpoint, acc, transformer)
	}
	if transformer := newProviderSSETransformer(adapter, route, endpoint, requestBody); transformer != nil {
		return copyTransformedSSEAndMeter(dst, src, adapter, endpoint, acc, transformer)
	}
	if shouldFrameResponsesSSE(route, endpoint) {
		src = newResponsesSSEFramerReader(src)
	}

	parser := sseMeteringParser{
		adapter:  adapter,
		endpoint: endpoint,
		acc:      acc,
	}
	buffer := make([]byte, 32*1024)
	var written int64
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			writeN, writeErr := dst.Write(chunk)
			written += int64(writeN)
			parser.feed(chunk)
			if writeErr != nil {
				return written, writeErr
			}
			if writeN != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
	}
}

func copyTransformedRawStreamAndMeter(dst io.Writer, src io.Reader, adapter providerAdapter, endpoint string, acc *streamMeteringAccumulator, transformer providerRawStreamTransformer) (int64, error) {
	buffer := make([]byte, 32*1024)
	var written int64
	meteringParser := sseMeteringParser{
		adapter:  adapter,
		endpoint: endpoint,
		acc:      acc,
	}
	writeOutputs := func(outputs [][]byte) error {
		for _, output := range outputs {
			meteringParser.feed(output)
			writeN, writeErr := dst.Write(output)
			written += int64(writeN)
			if writeErr != nil {
				return writeErr
			}
			if writeN != len(output) {
				return io.ErrShortWrite
			}
		}
		return nil
	}
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			outputs, transformErr := transformer.TransformChunk(buffer[:n])
			if transformErr != nil {
				return written, transformErr
			}
			if err := writeOutputs(outputs); err != nil {
				return written, err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				outputs, transformErr := transformer.Finish()
				if transformErr != nil {
					return written, transformErr
				}
				if err := writeOutputs(outputs); err != nil {
					return written, err
				}
				return written, nil
			}
			return written, readErr
		}
	}
}

func copyTransformedSSEAndMeter(dst io.Writer, src io.Reader, adapter providerAdapter, endpoint string, acc *streamMeteringAccumulator, transformer providerSSEEventTransformer) (int64, error) {
	buffer := make([]byte, 32*1024)
	pending := []byte{}
	var written int64
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			for {
				event, rest, ok := nextSSEEvent(pending)
				if !ok {
					break
				}
				pending = rest
				data, ok := sseEventData(event)
				if !ok || data == "[DONE]" {
					continue
				}
				if acc != nil {
					acc.EventCount++
					adapter.ParseStreamEvent(endpoint, []byte(data), acc)
				}
				outputs, transformErr := transformer.TransformEvent([]byte(data))
				if transformErr != nil {
					return written, transformErr
				}
				for _, output := range outputs {
					writeN, writeErr := dst.Write(output)
					written += int64(writeN)
					if writeErr != nil {
						return written, writeErr
					}
					if writeN != len(output) {
						return written, io.ErrShortWrite
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if len(bytes.TrimSpace(pending)) > 0 {
					data, ok := sseEventData(pending)
					if ok && data != "[DONE]" {
						if acc != nil {
							acc.EventCount++
							adapter.ParseStreamEvent(endpoint, []byte(data), acc)
						}
						outputs, transformErr := transformer.TransformEvent([]byte(data))
						if transformErr != nil {
							return written, transformErr
						}
						for _, output := range outputs {
							writeN, writeErr := dst.Write(output)
							written += int64(writeN)
							if writeErr != nil {
								return written, writeErr
							}
							if writeN != len(output) {
								return written, io.ErrShortWrite
							}
						}
					}
				}
				outputs, transformErr := transformer.Finish()
				if transformErr != nil {
					return written, transformErr
				}
				for _, output := range outputs {
					writeN, writeErr := dst.Write(output)
					written += int64(writeN)
					if writeErr != nil {
						return written, writeErr
					}
					if writeN != len(output) {
						return written, io.ErrShortWrite
					}
				}
				return written, nil
			}
			return written, readErr
		}
	}
}

func shouldEmitStreamCopyErrorEvent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrShortWrite) || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"broken pipe",
		"connection reset by peer",
		"client disconnected",
		"use of closed network connection",
	} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	return true
}

func streamCopyErrorEventPayload(route routeInfo, endpoint string, requestID string) []byte {
	message := "Upstream stream interrupted."
	code := "stream_interrupted"
	if isAnthropicStreamDialect(route, endpoint) {
		payload := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": message,
			},
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Upstream stream interrupted.\"}}\n\n")
		}
		return []byte("event: error\ndata: " + string(encoded) + "\n\n")
	}
	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    code,
		},
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte("data: {\"error\":{\"message\":\"Upstream stream interrupted.\",\"type\":\"upstream_error\",\"code\":\"stream_interrupted\"}}\n\n")
	}
	return []byte("data: " + string(encoded) + "\n\n")
}

func isAnthropicStreamDialect(route routeInfo, endpoint string) bool {
	if endpoint == "messages" {
		return true
	}
	provider := strings.ToLower(strings.TrimSpace(route.ProviderType))
	return provider == "anthropic" || provider == "anthropic_compatible" || provider == "claude_compatible"
}

func sseEventData(event []byte) (string, bool) {
	normalized := strings.ReplaceAll(string(event), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	dataLines := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data != "" {
			dataLines = append(dataLines, data)
		}
	}
	if len(dataLines) == 0 {
		return "", false
	}
	return strings.Join(dataLines, "\n"), true
}

type sseMeteringParser struct {
	pending  []byte
	adapter  providerAdapter
	endpoint string
	acc      *streamMeteringAccumulator
}

func (parser *sseMeteringParser) feed(chunk []byte) {
	if parser == nil || parser.adapter == nil || parser.acc == nil || len(chunk) == 0 {
		return
	}
	parser.pending = append(parser.pending, chunk...)
	for {
		event, rest, ok := nextSSEEvent(parser.pending)
		if !ok {
			return
		}
		parser.pending = rest
		parser.parseEvent(event)
	}
}

func (parser *sseMeteringParser) parseEvent(event []byte) {
	normalized := strings.ReplaceAll(string(event), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	dataLines := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data != "" {
			dataLines = append(dataLines, data)
		}
	}
	if len(dataLines) == 0 {
		return
	}
	data := strings.Join(dataLines, "\n")
	if data == "[DONE]" {
		return
	}
	parser.acc.EventCount++
	parser.adapter.ParseStreamEvent(parser.endpoint, []byte(data), parser.acc)
}

func nextSSEEvent(buffer []byte) ([]byte, []byte, bool) {
	separators := [][]byte{[]byte("\n\n"), []byte("\r\n\r\n"), []byte("\r\r")}
	bestIndex := -1
	bestLength := 0
	for _, separator := range separators {
		index := bytes.Index(buffer, separator)
		if index < 0 {
			continue
		}
		if bestIndex < 0 || index < bestIndex {
			bestIndex = index
			bestLength = len(separator)
		}
	}
	if bestIndex < 0 {
		return nil, nil, false
	}
	return buffer[:bestIndex], buffer[bestIndex+bestLength:], true
}

type flushWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (w flushWriter) Header() http.Header {
	return w.w.Header()
}

func (w flushWriter) WriteHeader(statusCode int) {
	w.w.WriteHeader(statusCode)
}

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	w.flusher.Flush()
	return n, err
}

type usageInput struct {
	RequestID           string
	RequestFingerprint  string
	UserID              string
	APIKeyID            string
	GroupID             string
	ChannelID           string
	AccountID           string
	ProviderType        string
	RequestedModel      string
	UpstreamModel       string
	Endpoint            string
	InputTokens         int
	OutputTokens        int
	ImageCount          int
	AudioSeconds        float64
	RequestCount        int
	EstimatedCost       float64
	ActualCost          float64
	BillingMultiplier   float64
	EffectivePolicy     effectivePolicy
	RiskDecision        riskDecision
	Pricing             modelInfo
	Status              string
	ErrorCode           string
	ErrorMessage        string
	UpstreamStatus      int
	DurationMS          int
	UsageSource         string
	StreamEventCount    int
	WebSocketFrameCount int
	MeteringMetadata    map[string]any
}

func (s *Server) recordUsage(ctx context.Context, input usageInput) error {
	var channel any
	if input.ChannelID != "" {
		channel = input.ChannelID
	}
	var account any
	if input.AccountID != "" {
		account = input.AccountID
	}
	var group any
	if input.GroupID == "" && input.EffectivePolicy.GroupID != "" {
		input.GroupID = input.EffectivePolicy.GroupID
	}
	if input.GroupID != "" {
		group = input.GroupID
	}
	if input.BillingMultiplier <= 0 {
		input.BillingMultiplier = input.EffectivePolicy.BillingMultiplier
	}
	if input.BillingMultiplier <= 0 {
		input.BillingMultiplier = 1
	}
	usageSource := strings.TrimSpace(input.UsageSource)
	if usageSource == "" {
		usageSource = "estimated_fallback"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_records (
			request_id, request_fingerprint, user_id, api_key_id, group_id, channel_id, account_id, requested_model, upstream_model, endpoint,
			input_tokens, output_tokens, image_count, audio_seconds, request_count, estimated_cost, actual_cost,
			status, error_code, error_message, pricing_snapshot_json, settlement_snapshot_json, upstream_status, duration_ms,
			usage_source, stream_event_count, websocket_frame_count, metering_metadata_json, billing_multiplier,
			effective_policy_json, risk_decision_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::numeric, $15, $16::numeric,
		        $17::numeric, $18, $19, $20, $21::jsonb, $22::jsonb, $23, $24, $25, $26, $27, $28::jsonb,
		        $29::numeric, $30::jsonb, $31::jsonb)
		ON CONFLICT DO NOTHING
	`, input.RequestID, input.RequestFingerprint, input.UserID, input.APIKeyID, group, channel, account, input.RequestedModel, input.UpstreamModel, input.Endpoint, input.InputTokens, input.OutputTokens,
		input.ImageCount,
		strconv.FormatFloat(input.AudioSeconds, 'f', 10, 64),
		nonZeroRequestCount(input.RequestCount),
		strconv.FormatFloat(input.EstimatedCost, 'f', 10, 64),
		strconv.FormatFloat(input.ActualCost, 'f', 10, 64),
		input.Status,
		input.ErrorCode,
		input.ErrorMessage,
		mustEncodeJSON(pricingSnapshot(input.Pricing)),
		mustEncodeJSON(settlementSnapshot(input.EstimatedCost, input.ActualCost, input.AccountID, usageSource, input.StreamEventCount, input.WebSocketFrameCount)),
		nullableInt(input.UpstreamStatus),
		input.DurationMS,
		usageSource,
		input.StreamEventCount,
		input.WebSocketFrameCount,
		mustEncodeJSON(input.MeteringMetadata),
		strconv.FormatFloat(input.BillingMultiplier, 'f', 10, 64),
		encodeEffectivePolicy(input.EffectivePolicy),
		encodeRiskDecision(input.RiskDecision))
	return err
}

func (s *Server) recordNorthboundUsage(ctx context.Context, r *http.Request, input usageInput) error {
	if input.RequestFingerprint == "" {
		input.RequestFingerprint = requestFingerprint(r.Method, r.URL.Path, nil)
	}
	usageInput := input
	usageInput.MeteringMetadata = usageMetadataWithParamOverrideAudit(r, usageInput.MeteringMetadata)
	if usageInput.EffectivePolicy.BillingMultiplier <= 0 && strings.TrimSpace(usageInput.UserID) != "" {
		modelName := firstNonEmpty(usageInput.UpstreamModel, usageInput.RequestedModel)
		if strings.TrimSpace(modelName) != "" {
			if policy, err := s.resolveEffectivePolicy(ctx, usageInput.UserID, modelName, usageInput.Endpoint); err == nil {
				usageInput.EffectivePolicy = policy
				usageInput.GroupID = policy.GroupID
				usageInput.BillingMultiplier = policy.BillingMultiplier
			}
		}
	}
	if strings.TrimSpace(usageInput.ProviderType) == "" {
		if providerType, err := s.providerTypeForUsage(ctx, usageInput.ChannelID, usageInput.AccountID); err == nil {
			usageInput.ProviderType = providerType
		}
	}
	err := s.recordUsage(ctx, usageInput)
	audit(ctx, s.db, usageInput.UserID, "personal_user", "northbound."+usageInput.Status, "northbound_request", usageInput.RequestID, r, map[string]any{
		"api_key_id":          usageInput.APIKeyID,
		"requested_model":     usageInput.RequestedModel,
		"upstream_model":      usageInput.UpstreamModel,
		"endpoint":            usageInput.Endpoint,
		"status":              usageInput.Status,
		"error_code":          usageInput.ErrorCode,
		"upstream_status":     nullableInt(usageInput.UpstreamStatus),
		"actual_cost":         usageInput.ActualCost,
		"request_count":       nonZeroRequestCount(usageInput.RequestCount),
		"request_fingerprint": usageInput.RequestFingerprint,
	})
	return err
}

func (s *Server) providerTypeForUsage(ctx context.Context, channelID string, accountID string) (string, error) {
	if strings.TrimSpace(accountID) != "" {
		var providerType string
		err := s.db.QueryRowContext(ctx, `
			SELECT p.provider_type
			FROM accounts a
			JOIN channels c ON c.id = a.channel_id
			JOIN providers p ON p.id = c.provider_id
			WHERE a.id = $1
		`, accountID).Scan(&providerType)
		if err == nil {
			return providerType, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if strings.TrimSpace(channelID) != "" {
		var providerType string
		err := s.db.QueryRowContext(ctx, `
			SELECT p.provider_type
			FROM channels c
			JOIN providers p ON p.id = c.provider_id
			WHERE c.id = $1
		`, channelID).Scan(&providerType)
		if err == nil {
			return providerType, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", sql.ErrNoRows
}

func nonZeroRequestCount(value int) int {
	if value <= 0 {
		return 1
	}
	return value
}

func pricingSnapshot(model modelInfo) map[string]any {
	if model.ModelName == "" {
		return map[string]any{}
	}
	return map[string]any{
		"model_name":             model.ModelName,
		"input_usd_per_1k":       model.InputUSDPer1K,
		"output_usd_per_1k":      model.OutputUSDPer1K,
		"request_usd":            model.RequestUSD,
		"min_charge_usd":         model.MinChargeUSD,
		"billing_mode":           defaultString(model.BillingMode, "standard"),
		"billing_expr":           model.BillingExpr,
		"cache_read_usd_per_1k":  model.CacheReadUSDPer1K,
		"cache_write_usd_per_1k": model.CacheWriteUSDPer1K,
		"image_usd_per_unit":     model.ImageUSDPerUnit,
		"audio_usd_per_second":   model.AudioUSDPerSecond,
	}
}

func settlementSnapshot(estimatedCost float64, actualCost float64, accountID string, usageSource string, streamEventCount int, webSocketFrameCount int) map[string]any {
	return map[string]any{
		"estimated_cost":        estimatedCost,
		"actual_cost":           actualCost,
		"account_id":            accountID,
		"usage_source":          usageSource,
		"stream_event_count":    streamEventCount,
		"websocket_frame_count": webSocketFrameCount,
	}
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableSQLInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func errorCode(err error) string {
	var appErr appError
	if errors.As(err, &appErr) {
		return appErr.code
	}
	return "internal_error"
}
