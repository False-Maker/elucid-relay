package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type effectivePolicy struct {
	GroupID             string
	GroupName           string
	GroupPriority       int
	GroupMultiplier     float64
	ModelMultiplier     float64
	BillingMultiplier   float64
	GroupRPMLimit       sql.NullInt64
	ModelRPMLimit       sql.NullInt64
	MonthlyUSDLimit     sql.NullString
	GroupAllowCount     int
	PermissionID        string
	Permission          string
	PermissionEndpoint  string
	MembershipCreatedAt time.Time
}

func defaultEffectivePolicy() effectivePolicy {
	return effectivePolicy{
		GroupMultiplier:   1,
		ModelMultiplier:   1,
		BillingMultiplier: 1,
	}
}

func (s *Server) resolveEffectivePolicy(ctx context.Context, userID string, modelName string, endpoint string) (effectivePolicy, error) {
	policy := defaultEffectivePolicy()
	if strings.TrimSpace(userID) == "" {
		return policy, nil
	}

	var groupMultiplier, modelMultiplier string
	var permissionID sql.NullString
	var membershipCreatedAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT g.id::text, g.name, g.priority, g.model_multiplier::text, g.rpm_limit,
		       g.monthly_usd_limit::text, m.created_at,
		       COALESCE(p.id::text, ''), COALESCE(p.permission, ''), COALESCE(p.endpoint, ''),
		       p.rpm_limit, COALESCE(p.price_multiplier::text, '1'),
		       COALESCE(permission_summary.allow_count, 0)
		FROM user_group_memberships m
		JOIN groups g ON g.id = m.group_id
		LEFT JOIN LATERAL (
		  SELECT COUNT(*) FILTER (WHERE permission = 'allow')::int AS allow_count
		  FROM group_model_permissions
		  WHERE group_id = g.id
		) permission_summary ON true
		LEFT JOIN LATERAL (
		  SELECT id, permission, endpoint, rpm_limit, price_multiplier
		  FROM group_model_permissions
		  WHERE group_id = g.id
		    AND model_name = $2
		    AND (endpoint = '' OR $3 = '' OR endpoint = $3)
		  ORDER BY
		    CASE WHEN permission = 'deny' THEN 0 ELSE 1 END,
		    CASE WHEN endpoint = $3 THEN 0 ELSE 1 END,
		    created_at DESC
		  LIMIT 1
		) p ON true
		WHERE m.user_id = $1
		  AND g.status = 'active'
		ORDER BY g.priority ASC, m.created_at ASC
		LIMIT 1
	`, userID, strings.TrimSpace(modelName), strings.TrimSpace(endpoint)).Scan(
		&policy.GroupID,
		&policy.GroupName,
		&policy.GroupPriority,
		&groupMultiplier,
		&policy.GroupRPMLimit,
		&policy.MonthlyUSDLimit,
		&membershipCreatedAt,
		&permissionID,
		&policy.Permission,
		&policy.PermissionEndpoint,
		&policy.ModelRPMLimit,
		&modelMultiplier,
		&policy.GroupAllowCount,
	)
	if err == sql.ErrNoRows {
		return policy, nil
	}
	if err != nil {
		return effectivePolicy{}, err
	}
	policy.PermissionID = permissionID.String
	policy.MembershipCreatedAt = membershipCreatedAt
	policy.GroupMultiplier = parsePositiveFloat(groupMultiplier, 1)
	policy.ModelMultiplier = parsePositiveFloat(modelMultiplier, 1)
	policy.BillingMultiplier = policy.GroupMultiplier * policy.ModelMultiplier
	return policy, nil
}

func (policy effectivePolicy) enforceModelAccess() error {
	if policy.GroupID == "" {
		return nil
	}
	if policy.Permission == "deny" {
		return forbidden("Model is denied by the effective group policy.")
	}
	if policy.GroupAllowCount > 0 && policy.Permission != "allow" {
		return forbidden("Model is not allowed by the effective group policy.")
	}
	return nil
}

func (policy effectivePolicy) effectiveRPMLimit() (int64, bool) {
	if policy.ModelRPMLimit.Valid {
		return policy.ModelRPMLimit.Int64, true
	}
	if policy.GroupRPMLimit.Valid {
		return policy.GroupRPMLimit.Int64, true
	}
	return 0, false
}

func applyBillingMultiplier(amount float64, policy effectivePolicy) float64 {
	if amount <= 0 {
		return 0
	}
	multiplier := policy.BillingMultiplier
	if multiplier <= 0 {
		return 0
	}
	return amount * multiplier
}

func (s *Server) enforceEffectivePolicyLimits(ctx context.Context, auth apiKeyAuth, policy effectivePolicy, reserveAmount float64, modelName string, endpoint string) error {
	if policy.GroupID == "" {
		return nil
	}
	if policy.MonthlyUSDLimit.Valid {
		limit, _ := strconv.ParseFloat(policy.MonthlyUSDLimit.String, 64)
		if limit >= 0 {
			var usedText string
			if err := s.db.QueryRowContext(ctx, `
				SELECT COALESCE(SUM(actual_cost), 0)::text
				FROM usage_records
				WHERE user_id = $1::uuid
				  AND group_id = $2::uuid
				  AND created_at >= date_trunc('month', now())
				  AND status IN ('success', 'failed')
			`, auth.UserID, policy.GroupID).Scan(&usedText); err != nil {
				return err
			}
			used, _ := strconv.ParseFloat(usedText, 64)
			if used+reserveAmount > limit {
				return billingError("group_monthly_spend_limit_exceeded", "Group monthly spend limit exceeded.")
			}
		}
	}

	limit, ok := policy.effectiveRPMLimit()
	if !ok || limit <= 0 {
		return nil
	}
	if s.redis == nil {
		return appError{status: http.StatusServiceUnavailable, code: "group_rate_limit_unavailable", message: "Group rate limit is unavailable.", typ: "rate_limit_error"}
	}
	window := time.Now().UTC().Unix() / 60
	key := fmt.Sprintf("group_rpm:%s:%s:%s:%s:%d", policy.GroupID, auth.UserID, modelName, endpoint, window)
	count, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return appError{status: http.StatusServiceUnavailable, code: "group_rate_limit_unavailable", message: "Group rate limit is unavailable.", typ: "rate_limit_error"}
	}
	if count == 1 {
		_ = s.redis.Expire(ctx, key, 2*time.Minute).Err()
	}
	if count > limit {
		return appError{status: http.StatusTooManyRequests, code: "group_rate_limit_exceeded", message: "Group RPM limit exceeded.", typ: "rate_limit_error"}
	}
	return nil
}

func effectivePolicySnapshot(policy effectivePolicy) map[string]any {
	if policy.GroupID == "" {
		return map[string]any{"billing_multiplier": "1.0000000000"}
	}
	return map[string]any{
		"group_id":              policy.GroupID,
		"group_name":            policy.GroupName,
		"group_priority":        policy.GroupPriority,
		"group_multiplier":      formatMoney(policy.GroupMultiplier),
		"model_multiplier":      formatMoney(policy.ModelMultiplier),
		"billing_multiplier":    formatMoney(policy.BillingMultiplier),
		"group_rpm_limit":       nullableSQLInt(policy.GroupRPMLimit),
		"model_rpm_limit":       nullableSQLInt(policy.ModelRPMLimit),
		"monthly_usd_limit":     nullableSQLString(policy.MonthlyUSDLimit),
		"group_allow_count":     policy.GroupAllowCount,
		"permission_id":         policy.PermissionID,
		"permission":            policy.Permission,
		"permission_endpoint":   policy.PermissionEndpoint,
		"membership_created_at": policy.MembershipCreatedAt.UTC().Format(time.RFC3339),
	}
}

func encodeEffectivePolicy(policy effectivePolicy) string {
	return mustEncodeJSON(effectivePolicySnapshot(policy))
}

func policyAllowsListedModel(policy effectivePolicy) bool {
	return policy.Permission != "deny" && (policy.GroupAllowCount == 0 || policy.Permission == "allow")
}

func parsePositiveFloat(value string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func formatMoney(value float64) string {
	return strconv.FormatFloat(value, 'f', 10, 64)
}

func jsonArrayStrings(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}
