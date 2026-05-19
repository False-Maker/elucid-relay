package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func usageWhereFromRequest(r *http.Request, base []string, args []any) (string, []any, error) {
	query := r.URL.Query()
	if value := strings.TrimSpace(query.Get("request_id")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("request_id = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("api_key_id")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("api_key_id::text = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("user_id")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("user_id::text = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("channel_id")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("channel_id::text = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("account_id")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("account_id::text = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("model")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("requested_model = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("endpoint")); value != "" {
		args = append(args, value)
		base = append(base, fmt.Sprintf("endpoint = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("status")); value != "" {
		if value != "success" && value != "failed" && value != "rejected" {
			return "", nil, badRequest("Invalid usage status.")
		}
		args = append(args, value)
		base = append(base, fmt.Sprintf("status = $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("date_from")); value != "" {
		parsed, err := parseUsageTime(value)
		if err != nil {
			return "", nil, badRequest("date_from must be RFC3339 or YYYY-MM-DD.")
		}
		args = append(args, parsed)
		base = append(base, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("date_to")); value != "" {
		parsed, err := parseUsageTime(value)
		if err != nil {
			return "", nil, badRequest("date_to must be RFC3339 or YYYY-MM-DD.")
		}
		args = append(args, parsed)
		base = append(base, fmt.Sprintf("created_at < $%d", len(args)))
	}
	if len(base) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(base, " AND "), args, nil
}

func parseUsageTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}
