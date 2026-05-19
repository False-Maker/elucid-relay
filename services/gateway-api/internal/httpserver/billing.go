package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type orderRecord struct {
	ID                      string
	UserID                  string
	PlanID                  string
	Status                  string
	AmountUSD               string
	Currency                string
	FeatureFlag             string
	Metadata                string
	OrderType               string
	StripeCheckoutSessionID string
	StripePaymentIntentID   string
	StripeSubscriptionID    string
	StripeRefundID          string
	CheckoutURL             string
	PaymentProvider         string
	PaymentMethod           string
	ProviderInstanceID      string
	PayCurrency             string
	PayAmountCents          int
	FXRate                  string
	UpstreamTradeNo         string
	UpstreamTransactionID   string
	PaidAt                  sql.NullTime
	RefundedAt              sql.NullTime
	ExpiresAt               sql.NullTime
	RefundBlockedReason     string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type paymentEventRecord struct {
	ID              string
	OrderID         string
	Provider        string
	ProviderEventID string
	EventType       string
	Payload         string
	Status          string
	Attempts        int
	ProcessedAt     sql.NullTime
	ProcessingError string
	LastAttemptAt   sql.NullTime
	NextAttemptAt   sql.NullTime
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type stripeWebhookEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object map[string]any `json:"object"`
	} `json:"data"`
}

func (s *Server) portalSubscriptionPlans(w http.ResponseWriter, r *http.Request, auth authContext) error {
	plans, err := s.listSubscriptionPlans(r.Context(), true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plans, nil)
	return nil
}

func (s *Server) adminSubscriptionPlans(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		plans, err := s.listSubscriptionPlans(r.Context(), false)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, plans, nil)
		return nil
	case http.MethodPost:
		id, err := s.upsertSubscriptionPlan(r, "", true)
		if err != nil {
			return err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "subscription_plan.create", "subscription_plan", id, r, nil)
		writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
		return nil
	default:
		return notFound("Endpoint was not found.")
	}
}

func (s *Server) adminPatchSubscriptionPlan(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertSubscriptionPlan(r, r.PathValue("planId"), false)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "subscription_plan.update", "subscription_plan", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) listSubscriptionPlans(ctx context.Context, publicOnly bool) ([]map[string]any, error) {
	statusClause := ""
	if publicOnly {
		statusClause = "WHERE sp.status = 'active'"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sp.id::text, sp.name, sp.status, sp.price_usd::text, sp.billing_period,
		       sp.wallet_credit_usd::text, COALESCE(sp.group_id::text, ''), COALESCE(sp.stripe_price_id, ''),
		       sp.features_json::text, sp.metadata_json::text, sp.created_at, sp.updated_at,
		       COALESCE(g.name, '')
		FROM subscription_plans sp
		LEFT JOIN groups g ON g.id = sp.group_id
		`+statusClause+`
		ORDER BY sp.price_usd ASC, sp.created_at DESC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, status, price, period, credit, groupID, stripePriceID, features, metadata, groupName string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &name, &status, &price, &period, &credit, &groupID, &stripePriceID, &features, &metadata, &createdAt, &updatedAt, &groupName); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":                id,
			"name":              name,
			"status":            status,
			"price_usd":         price,
			"billing_period":    period,
			"wallet_credit_usd": credit,
			"group_id":          groupID,
			"group_name":        groupName,
			"stripe_price_id":   stripePriceID,
			"features":          jsonArrayRaw(features),
			"metadata":          jsonRaw(metadata),
			"created_at":        createdAt.UTC().Format(time.RFC3339),
			"updated_at":        updatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) upsertSubscriptionPlan(r *http.Request, id string, insert bool) (string, error) {
	var req struct {
		Name            string         `json:"name"`
		Status          string         `json:"status"`
		PriceUSD        string         `json:"price_usd"`
		BillingPeriod   string         `json:"billing_period"`
		WalletCreditUSD string         `json:"wallet_credit_usd"`
		GroupID         string         `json:"group_id"`
		StripePriceID   string         `json:"stripe_price_id"`
		Features        []string       `json:"features"`
		Metadata        map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	if insert && strings.TrimSpace(req.Name) == "" {
		return "", badRequest("name is required.")
	}
	status, err := defaultedEnum(req.Status, "draft", "draft", "active", "archived")
	if err != nil {
		return "", err
	}
	period, err := defaultedEnum(req.BillingPeriod, "month", "month", "year", "one_time")
	if err != nil {
		return "", err
	}
	price := defaultString(req.PriceUSD, "0")
	if _, err := nullableNonNegativeDecimal(&price); err != nil {
		return "", badRequest("price_usd must be a non-negative decimal string.")
	}
	credit := defaultString(req.WalletCreditUSD, "0")
	if _, err := nullableNonNegativeDecimal(&credit); err != nil {
		return "", badRequest("wallet_credit_usd must be a non-negative decimal string.")
	}
	features, err := encodeJSON(normalizeStringList(req.Features))
	if err != nil {
		return "", err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return "", err
	}
	groupID := nullUUID(req.GroupID)
	if insert {
		err = s.db.QueryRowContext(r.Context(), `
			INSERT INTO subscription_plans (name, status, price_usd, billing_period, wallet_credit_usd, group_id, stripe_price_id, features_json, metadata_json)
			VALUES ($1, $2, $3::numeric, $4, $5::numeric, $6, $7, $8::jsonb, $9::jsonb)
			RETURNING id::text
		`, strings.TrimSpace(req.Name), status, price, period, credit, groupID, strings.TrimSpace(req.StripePriceID), features, metadata).Scan(&id)
		return id, err
	}
	if err := s.requireIDExists(r.Context(), "subscription_plans", id, "Subscription plan was not found."); err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(r.Context(), `
		UPDATE subscription_plans
		SET name = COALESCE(NULLIF($2, ''), name),
		    status = $3,
		    price_usd = $4::numeric,
		    billing_period = $5,
		    wallet_credit_usd = $6::numeric,
		    group_id = $7,
		    stripe_price_id = $8,
		    features_json = $9::jsonb,
		    metadata_json = $10::jsonb
		WHERE id = $1
	`, id, strings.TrimSpace(req.Name), status, price, period, credit, groupID, strings.TrimSpace(req.StripePriceID), features, metadata)
	return id, err
}

func (s *Server) portalOrders(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		orders, err := s.listOrders(r.Context(), "user_id = $2::uuid", []any{limitFromRequest(r, 100, 500), auth.UserID})
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, orders, nil)
		return nil
	case http.MethodPost:
		id, err := s.createPortalOrder(r, auth)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
		return nil
	default:
		return notFound("Endpoint was not found.")
	}
}

func (s *Server) portalOrder(w http.ResponseWriter, r *http.Request, auth authContext) error {
	order, err := s.getOrder(r.Context(), r.PathValue("orderId"), auth.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, orderResponse(order), nil)
	return nil
}

func (s *Server) createPortalOrder(r *http.Request, auth authContext) (string, error) {
	var req struct {
		OrderType     string         `json:"order_type"`
		AmountUSD     string         `json:"amount_usd"`
		PlanID        string         `json:"plan_id"`
		AffiliateCode string         `json:"affiliate_code"`
		Metadata      map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	orderType := defaultString(req.OrderType, "wallet_topup")
	if orderType != "wallet_topup" && orderType != "subscription" {
		return "", badRequest("order_type must be wallet_topup or subscription.")
	}
	amount := strings.TrimSpace(req.AmountUSD)
	metadata := defaultMap(req.Metadata)
	var planID any
	if orderType == "subscription" {
		if strings.TrimSpace(req.PlanID) == "" {
			return "", badRequest("plan_id is required.")
		}
		var status string
		if err := s.db.QueryRowContext(r.Context(), "SELECT price_usd::text, status FROM subscription_plans WHERE id = $1", req.PlanID).Scan(&amount, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", notFound("Subscription plan was not found.")
			}
			return "", err
		}
		if status != "active" {
			return "", badRequest("Subscription plan is not active.")
		}
		planID = req.PlanID
	} else if _, err := amountString(amount); err != nil {
		return "", err
	}
	metadata["order_type"] = orderType
	if req.AffiliateCode != "" {
		metadata["affiliate_code"] = strings.TrimSpace(req.AffiliateCode)
	}
	metadataJSON, err := encodeJSON(metadata)
	if err != nil {
		return "", err
	}
	var id string
	if err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO orders (user_id, plan_id, amount_usd, currency, feature_flag, metadata_json, order_type)
		VALUES ($1, $2, $3::numeric, 'USD', 'payments', $4::jsonb, $5)
		RETURNING id::text
	`, auth.UserID, planID, amount, metadataJSON, orderType).Scan(&id); err != nil {
		return "", err
	}
	if req.AffiliateCode != "" {
		_ = s.attributeAffiliateCode(r.Context(), auth.UserID, id, req.AffiliateCode)
	}
	return id, nil
}

func (s *Server) portalOrderCheckout(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		PaymentMethod string `json:"payment_method"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
	}
	order, err := s.getOrder(r.Context(), r.PathValue("orderId"), auth.UserID)
	if err != nil {
		return err
	}
	if order.Status != "pending" {
		writeJSON(w, http.StatusOK, orderResponse(order), nil)
		return nil
	}
	result, err := s.createUnifiedCheckout(r.Context(), order, req.PaymentMethod)
	if err != nil {
		return err
	}
	if err := s.updateOrderAfterCheckout(r.Context(), order, result); err != nil {
		return err
	}
	order.PaymentProvider = result.Provider
	order.PaymentMethod = result.Method
	order.ProviderInstanceID = result.ProviderInstanceID
	order.UpstreamTradeNo = result.UpstreamTradeNo
	order.UpstreamTransactionID = result.UpstreamTransactionID
	order.PayCurrency = result.PayCurrency
	order.PayAmountCents = result.PayAmountCents
	order.FXRate = result.FXRate
	order.CheckoutURL = result.CheckoutURL
	order.ExpiresAt = sql.NullTime{Time: result.ExpiresAt, Valid: !result.ExpiresAt.IsZero()}
	if result.Provider == "stripe" {
		order.StripeCheckoutSessionID = result.UpstreamTradeNo
	}
	writeJSON(w, http.StatusOK, orderResponse(order), nil)
	return nil
}

func (s *Server) adminOrders(w http.ResponseWriter, r *http.Request, auth authContext) error {
	orders, err := s.listOrders(r.Context(), "", []any{limitFromRequest(r, 100, 500)})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, orders, nil)
	return nil
}

func (s *Server) adminPaymentEvents(w http.ResponseWriter, r *http.Request, auth authContext) error {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	eventType := strings.TrimSpace(r.URL.Query().Get("event_type"))
	orderID := strings.TrimSpace(r.URL.Query().Get("order_id"))
	if status != "" && !validPaymentEventStatus(status) {
		return badRequest("Invalid payment event status.")
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, COALESCE(order_id::text, ''), provider, provider_event_id, event_type,
		       payload_json::text, status, attempts, processed_at, COALESCE(processing_error, ''),
		       last_attempt_at, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at
		FROM payment_events
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR event_type = $2)
		  AND ($3 = '' OR order_id::text = $3)
		ORDER BY created_at DESC
		LIMIT $4
	`, status, eventType, orderID, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		event, err := scanPaymentEvent(rows)
		if err != nil {
			return err
		}
		items = append(items, paymentEventResponse(event))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminReplayPaymentEvent(w http.ResponseWriter, r *http.Request, auth authContext) error {
	eventID := strings.TrimSpace(r.PathValue("eventId"))
	event, err := s.getPaymentEvent(r.Context(), eventID)
	if err != nil {
		return err
	}
	if event.Provider != "stripe" {
		return badRequest("Only Stripe payment events can be replayed.")
	}
	if !paymentEventCanReplay(event.Status) {
		return conflict("Only pending or failed payment events can be replayed.")
	}
	stripeEvent, err := decodeStripeWebhookEvent([]byte(event.Payload))
	if err != nil {
		return err
	}
	orderID := event.OrderID
	if orderID == "" {
		orderID = stripeOrderID(stripeEvent.Data.Object)
	}
	if orderID == "" {
		orderID = s.lookupStripeOrderID(r.Context(), stripeEvent.Data.Object)
	}
	if err := s.markPaymentEventProcessing(r.Context(), event.ID); err != nil {
		return err
	}
	if err := s.handleStripeEvent(r.Context(), orderID, stripeEvent.Type, stripeEvent.Data.Object); err != nil {
		if updateErr := s.markPaymentEventFailed(r.Context(), event.ID, err); updateErr != nil {
			return updateErr
		}
		return err
	}
	updated, err := s.markPaymentEventSucceeded(r.Context(), event.ID, "replayed")
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "payment_event.replay", "payment_event", event.ID, r, map[string]any{"provider_event_id": event.ProviderEventID})
	writeJSON(w, http.StatusOK, paymentEventResponse(updated), nil)
	return nil
}

func (s *Server) listOrders(ctx context.Context, extraWhere string, args []any) ([]map[string]any, error) {
	where := ""
	if extraWhere != "" {
		where = "WHERE " + extraWhere
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, user_id::text, COALESCE(plan_id::text, ''), status, amount_usd::text, currency, feature_flag,
		       metadata_json::text, order_type, COALESCE(stripe_checkout_session_id, ''), COALESCE(stripe_payment_intent_id, ''), COALESCE(stripe_subscription_id, ''),
		       COALESCE(stripe_refund_id, ''), COALESCE(checkout_url, ''), COALESCE(payment_provider, ''), COALESCE(payment_method, ''),
		       COALESCE(provider_instance_id::text, ''), COALESCE(pay_currency, ''), COALESCE(pay_amount_cents, 0), COALESCE(fx_rate::text, ''),
		       COALESCE(upstream_trade_no, ''), COALESCE(upstream_transaction_id, ''), paid_at, refunded_at, expires_at, COALESCE(refund_blocked_reason, ''), created_at, updated_at
		FROM orders
		`+where+`
		ORDER BY created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		record, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, orderResponse(record))
	}
	return items, rows.Err()
}

func (s *Server) getOrder(ctx context.Context, orderID string, userID string) (orderRecord, error) {
	query := `
		SELECT id::text, user_id::text, COALESCE(plan_id::text, ''), status, amount_usd::text, currency, feature_flag,
		       metadata_json::text, order_type, COALESCE(stripe_checkout_session_id, ''), COALESCE(stripe_payment_intent_id, ''), COALESCE(stripe_subscription_id, ''),
		       COALESCE(stripe_refund_id, ''), COALESCE(checkout_url, ''), COALESCE(payment_provider, ''), COALESCE(payment_method, ''),
		       COALESCE(provider_instance_id::text, ''), COALESCE(pay_currency, ''), COALESCE(pay_amount_cents, 0), COALESCE(fx_rate::text, ''),
		       COALESCE(upstream_trade_no, ''), COALESCE(upstream_transaction_id, ''), paid_at, refunded_at, expires_at, COALESCE(refund_blocked_reason, ''), created_at, updated_at
		FROM orders
		WHERE id = $1
	`
	args := []any{orderID}
	if strings.TrimSpace(userID) != "" {
		query += " AND user_id = $2::uuid"
		args = append(args, userID)
	}
	var record orderRecord
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&record.ID, &record.UserID, &record.PlanID, &record.Status, &record.AmountUSD, &record.Currency, &record.FeatureFlag,
		&record.Metadata, &record.OrderType, &record.StripeCheckoutSessionID, &record.StripePaymentIntentID, &record.StripeSubscriptionID,
		&record.StripeRefundID, &record.CheckoutURL, &record.PaymentProvider, &record.PaymentMethod, &record.ProviderInstanceID,
		&record.PayCurrency, &record.PayAmountCents, &record.FXRate, &record.UpstreamTradeNo, &record.UpstreamTransactionID,
		&record.PaidAt, &record.RefundedAt, &record.ExpiresAt, &record.RefundBlockedReason, &record.CreatedAt, &record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return orderRecord{}, notFound("Order was not found.")
	}
	return record, err
}

func scanOrder(scanner interface{ Scan(...any) error }) (orderRecord, error) {
	var record orderRecord
	err := scanner.Scan(
		&record.ID, &record.UserID, &record.PlanID, &record.Status, &record.AmountUSD, &record.Currency, &record.FeatureFlag,
		&record.Metadata, &record.OrderType, &record.StripeCheckoutSessionID, &record.StripePaymentIntentID, &record.StripeSubscriptionID,
		&record.StripeRefundID, &record.CheckoutURL, &record.PaymentProvider, &record.PaymentMethod, &record.ProviderInstanceID,
		&record.PayCurrency, &record.PayAmountCents, &record.FXRate, &record.UpstreamTradeNo, &record.UpstreamTransactionID,
		&record.PaidAt, &record.RefundedAt, &record.ExpiresAt, &record.RefundBlockedReason, &record.CreatedAt, &record.UpdatedAt,
	)
	return record, err
}

func orderResponse(order orderRecord) map[string]any {
	return map[string]any{
		"id":                         order.ID,
		"user_id":                    order.UserID,
		"plan_id":                    order.PlanID,
		"status":                     order.Status,
		"amount_usd":                 order.AmountUSD,
		"currency":                   order.Currency,
		"feature_flag":               order.FeatureFlag,
		"metadata":                   jsonRaw(order.Metadata),
		"order_type":                 order.OrderType,
		"stripe_checkout_session_id": order.StripeCheckoutSessionID,
		"stripe_payment_intent_id":   order.StripePaymentIntentID,
		"stripe_subscription_id":     order.StripeSubscriptionID,
		"stripe_refund_id":           order.StripeRefundID,
		"checkout_url":               order.CheckoutURL,
		"payment_provider":           order.PaymentProvider,
		"payment_method":             order.PaymentMethod,
		"provider_instance_id":       order.ProviderInstanceID,
		"pay_currency":               order.PayCurrency,
		"pay_amount_cents":           order.PayAmountCents,
		"fx_rate":                    order.FXRate,
		"upstream_trade_no":          order.UpstreamTradeNo,
		"upstream_transaction_id":    order.UpstreamTransactionID,
		"paid_at":                    nullableSQLTime(order.PaidAt),
		"refunded_at":                nullableSQLTime(order.RefundedAt),
		"expires_at":                 nullableSQLTime(order.ExpiresAt),
		"refund_blocked_reason":      order.RefundBlockedReason,
		"created_at":                 order.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":                 order.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type stripeCheckoutSession struct {
	ID  string
	URL string
}

func (s *Server) createStripeCheckoutSession(ctx context.Context, order orderRecord) (stripeCheckoutSession, error) {
	settings, err := s.stripeSettings(ctx)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	paymentProvider, _ := s.firstActivePaymentProvider(ctx, "stripe", "stripe")
	secret := strings.TrimSpace(settings.SecretKey)
	if providerSecret := strings.TrimSpace(s.paymentSecret(paymentProvider, "secret_key")); providerSecret != "" {
		secret = providerSecret
	}
	if secret == "" {
		return stripeCheckoutSession{}, appError{status: http.StatusServiceUnavailable, code: "stripe_not_configured", message: "Stripe secret key is not configured.", typ: "billing_error"}
	}
	paymentSettings, _ := s.paymentSettings(ctx)
	form := url.Values{}
	form.Set("mode", "payment")
	successURL := paymentSettings.stripeSuccessURL(order.ID, s.cfg.PortalBaseURL)
	cancelURL := paymentSettings.stripeCancelURL(order.ID, s.cfg.PortalBaseURL)
	if successURL == "" {
		successURL = settings.successURL(order.ID, s.cfg.PortalBaseURL)
	}
	if cancelURL == "" {
		cancelURL = settings.cancelURL(order.ID, s.cfg.PortalBaseURL)
	}
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", order.ID)
	form.Set("metadata[order_id]", order.ID)
	form.Set("metadata[user_id]", order.UserID)
	form.Set("metadata[order_type]", order.OrderType)
	if order.OrderType == "subscription" {
		form.Set("mode", "subscription")
		var name, price, period, stripePriceID string
		if err := s.db.QueryRowContext(ctx, "SELECT name, price_usd::text, billing_period, COALESCE(stripe_price_id, '') FROM subscription_plans WHERE id = $1", order.PlanID).Scan(&name, &price, &period, &stripePriceID); err != nil {
			return stripeCheckoutSession{}, err
		}
		if stripePriceID != "" {
			form.Set("line_items[0][price]", stripePriceID)
		} else {
			form.Set("line_items[0][price_data][currency]", "usd")
			form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(usdCents(price)))
			form.Set("line_items[0][price_data][product_data][name]", name)
			form.Set("line_items[0][price_data][recurring][interval]", stripeInterval(period))
		}
		form.Set("subscription_data[metadata][order_id]", order.ID)
		form.Set("subscription_data[metadata][user_id]", order.UserID)
	} else {
		form.Set("line_items[0][price_data][currency]", "usd")
		form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(usdCents(order.AmountUSD)))
		form.Set("line_items[0][price_data][product_data][name]", "Wallet recharge")
	}
	form.Set("line_items[0][quantity]", "1")
	body, err := stripePostForm(ctx, s.httpClient, secret, "https://api.stripe.com/v1/checkout/sessions", form)
	if err != nil {
		return stripeCheckoutSession{}, err
	}
	var decoded struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.ID == "" || decoded.URL == "" {
		return stripeCheckoutSession{}, upstreamUnavailable("stripe_checkout_failed", "Stripe checkout session response was invalid.")
	}
	return stripeCheckoutSession{ID: decoded.ID, URL: decoded.URL}, nil
}

func stripePostForm(ctx context.Context, client *http.Client, secret string, endpoint string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, upstreamUnavailable("stripe_unavailable", "Stripe request failed.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, appError{status: http.StatusBadGateway, code: "stripe_request_failed", message: truncateForStorage(string(body), 500), typ: "billing_error"}
	}
	return body, nil
}

func (s *Server) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readLimitedRequestBody(r.Body, 1<<20)
	if err != nil {
		writeError(w, r, err)
		return
	}
	settings, err := s.stripeSettings(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	secret := strings.TrimSpace(settings.WebhookSecret)
	if secret != "" && !verifyStripeSignature(body, r.Header.Get("Stripe-Signature"), secret) {
		writeError(w, r, unauthorized("Invalid Stripe signature."))
		return
	}
	event, err := decodeStripeWebhookEvent(body)
	if err != nil {
		writeError(w, r, badRequest("Invalid Stripe webhook payload."))
		return
	}
	orderID := stripeOrderID(event.Data.Object)
	if orderID == "" {
		orderID = s.lookupStripeOrderID(r.Context(), event.Data.Object)
	}
	paymentEventID, inserted, err := s.insertPaymentEvent(r.Context(), orderID, event.ID, event.Type, body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !inserted {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": true}, nil)
		return
	}
	if err := s.markPaymentEventProcessing(r.Context(), paymentEventID); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.handleStripeEvent(r.Context(), orderID, event.Type, event.Data.Object); err != nil {
		_ = s.markPaymentEventFailed(r.Context(), paymentEventID, err)
		writeError(w, r, err)
		return
	}
	if _, err := s.markPaymentEventSucceeded(r.Context(), paymentEventID, "processed"); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true}, nil)
}

func (s *Server) handleStripeEvent(ctx context.Context, orderID string, eventType string, object map[string]any) error {
	switch eventType {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if orderID == "" {
			return nil
		}
		return s.markOrderPaid(ctx, orderID, stripeObjectString(object, "id"), stripeObjectString(object, "payment_intent"), stripeObjectString(object, "subscription"))
	case "checkout.session.async_payment_failed":
		if orderID == "" {
			return nil
		}
		_, err := s.db.ExecContext(ctx, "UPDATE orders SET status = 'failed' WHERE id = $1 AND status = 'pending'", orderID)
		return err
	case "checkout.session.expired":
		if orderID == "" {
			return nil
		}
		_, err := s.db.ExecContext(ctx, "UPDATE orders SET status = 'canceled' WHERE id = $1 AND status = 'pending'", orderID)
		return err
	case "invoice.payment_succeeded":
		if !stripeInvoiceIsSubscriptionRenewal(object) {
			return nil
		}
		subscriptionID := stripeObjectString(object, "subscription")
		if orderID == "" && subscriptionID != "" {
			var found string
			_ = s.db.QueryRowContext(ctx, "SELECT order_id::text FROM user_subscriptions WHERE stripe_subscription_id = $1 ORDER BY created_at DESC LIMIT 1", subscriptionID).Scan(&found)
			orderID = found
		}
		if orderID != "" {
			return s.grantSubscriptionRenewal(ctx, orderID, subscriptionID, object)
		}
	case "customer.subscription.deleted":
		subscriptionID := stripeObjectString(object, "id")
		if subscriptionID != "" {
			return s.cancelSubscriptionByStripeID(ctx, subscriptionID)
		}
	case "charge.refunded", "refund.updated", "refund.created":
		if orderID == "" {
			return nil
		}
		return s.markOrderRefunded(ctx, orderID, stripeObjectString(object, "id"))
	}
	return nil
}

func (s *Server) markOrderPaid(ctx context.Context, orderID string, sessionID string, paymentIntentID string, subscriptionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := getOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if order.Status == "paid" {
		return tx.Commit()
	}
	if order.Status != "pending" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE orders
		SET status = 'paid', paid_at = now(), stripe_checkout_session_id = COALESCE(NULLIF($2, ''), stripe_checkout_session_id),
		    stripe_payment_intent_id = COALESCE(NULLIF($3, ''), stripe_payment_intent_id),
		    stripe_subscription_id = COALESCE(NULLIF($4, ''), stripe_subscription_id)
		WHERE id = $1
	`, orderID, sessionID, paymentIntentID, subscriptionID); err != nil {
		return err
	}
	if order.OrderType == "subscription" {
		if err := grantSubscriptionBenefits(ctx, tx, order, subscriptionID); err != nil {
			return err
		}
	} else if err := creditWalletTx(ctx, tx, order.UserID, order.AmountUSD, "payment", "order", order.ID, map[string]any{"stripe_payment_intent_id": paymentIntentID}); err != nil {
		return err
	}
	if err := createAffiliateRebateTx(ctx, tx, order); err != nil {
		return err
	}
	return tx.Commit()
}

func grantSubscriptionBenefits(ctx context.Context, tx *sql.Tx, order orderRecord, subscriptionID string) error {
	var groupID, credit, period string
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(group_id::text, ''), wallet_credit_usd::text, billing_period FROM subscription_plans WHERE id = $1", order.PlanID).Scan(&groupID, &credit, &period); err != nil {
		return err
	}
	if parsePositiveFloat(credit, 0) > 0 {
		if err := creditWalletTx(ctx, tx, order.UserID, credit, "subscription_credit", "order", order.ID, map[string]any{"stripe_subscription_id": subscriptionID}); err != nil {
			return err
		}
	}
	var endsAt any
	switch period {
	case "year":
		endsAt = time.Now().UTC().AddDate(1, 0, 0)
	case "month":
		endsAt = time.Now().UTC().AddDate(0, 1, 0)
	default:
		endsAt = nil
	}
	var subscriptionRowID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO user_subscriptions (user_id, plan_id, status, ends_at, stripe_subscription_id, granted_group_id, order_id, current_period_start, current_period_end)
		VALUES ($1, $2, 'active', $3, $4, NULLIF($5, '')::uuid, $6, now(), $3)
		RETURNING id::text
	`, order.UserID, order.PlanID, endsAt, subscriptionID, groupID, order.ID).Scan(&subscriptionRowID); err != nil {
		return err
	}
	if groupID != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_group_memberships (group_id, user_id, role)
			VALUES ($1, $2, $3)
			ON CONFLICT (group_id, user_id) DO UPDATE SET role = EXCLUDED.role
			WHERE user_group_memberships.role LIKE 'subscription:%'
		`, groupID, order.UserID, "subscription:"+subscriptionRowID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) grantSubscriptionRenewal(ctx context.Context, orderID string, subscriptionID string, object map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := getOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if order.OrderType != "subscription" {
		return tx.Commit()
	}
	var credit, period string
	if err := tx.QueryRowContext(ctx, "SELECT wallet_credit_usd::text, billing_period FROM subscription_plans WHERE id = $1", order.PlanID).Scan(&credit, &period); err != nil {
		return err
	}
	if parsePositiveFloat(credit, 0) > 0 {
		if err := creditWalletTx(ctx, tx, order.UserID, credit, "subscription_credit", "order", order.ID, map[string]any{"stripe_subscription_id": subscriptionID, "renewal": true}); err != nil {
			return err
		}
	}
	periodStart, periodEnd := stripeInvoicePeriod(object)
	if periodStart == nil {
		now := time.Now().UTC()
		periodStart = &now
	}
	if periodEnd == nil {
		next := subscriptionPeriodEnd(*periodStart, period)
		periodEnd = &next
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE user_subscriptions
		SET status = 'active', ends_at = $3, current_period_start = $2, current_period_end = $3
		WHERE id = (
		  SELECT id
		  FROM user_subscriptions
		  WHERE user_id = $1
		    AND plan_id = $4
		    AND stripe_subscription_id = $5
		  ORDER BY created_at DESC
		  LIMIT 1
		)
	`, order.UserID, periodStart, periodEnd, order.PlanID, subscriptionID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		if err := grantSubscriptionBenefits(ctx, tx, order, subscriptionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) adminCancelSubscription(w http.ResponseWriter, r *http.Request, auth authContext) error {
	subscriptionID := r.PathValue("subscriptionId")
	if err := s.cancelSubscriptionByID(r.Context(), subscriptionID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "subscription.cancel", "subscription", subscriptionID, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": subscriptionID, "canceled": true}, nil)
	return nil
}

func (s *Server) cancelSubscriptionByStripeID(ctx context.Context, stripeSubscriptionID string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id::text FROM user_subscriptions WHERE stripe_subscription_id = $1 AND status IN ('active', 'past_due')", stripeSubscriptionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if err := s.cancelSubscriptionByID(ctx, id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Server) cancelSubscriptionByID(ctx context.Context, subscriptionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID, groupID, status string
	err = tx.QueryRowContext(ctx, `
		SELECT user_id::text, COALESCE(granted_group_id::text, ''), status
		FROM user_subscriptions
		WHERE id = $1
		FOR UPDATE
	`, subscriptionID).Scan(&userID, &groupID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("Subscription was not found.")
	}
	if err != nil {
		return err
	}
	if status == "canceled" || status == "expired" {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE user_subscriptions SET status = 'canceled', ends_at = now() WHERE id = $1", subscriptionID); err != nil {
		return err
	}
	if groupID != "" {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM user_group_memberships
			WHERE group_id = $1
			  AND user_id = $2
			  AND role = $3
		`, groupID, userID, "subscription:"+subscriptionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) adminSubscriptions(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT us.id::text, us.user_id::text, u.email::text, us.plan_id::text, sp.name, us.status,
		       COALESCE(us.stripe_subscription_id, ''), COALESCE(us.granted_group_id::text, ''), us.starts_at, us.ends_at,
		       us.current_period_start, us.current_period_end, us.created_at, us.updated_at
		FROM user_subscriptions us
		JOIN users u ON u.id = us.user_id
		JOIN subscription_plans sp ON sp.id = us.plan_id
		ORDER BY us.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items, err := scanSubscriptions(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) portalSubscriptions(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT us.id::text, us.user_id::text, u.email::text, us.plan_id::text, sp.name, us.status,
		       COALESCE(us.stripe_subscription_id, ''), COALESCE(us.granted_group_id::text, ''), us.starts_at, us.ends_at,
		       us.current_period_start, us.current_period_end, us.created_at, us.updated_at
		FROM user_subscriptions us
		JOIN users u ON u.id = us.user_id
		JOIN subscription_plans sp ON sp.id = us.plan_id
		WHERE us.user_id = $1
		ORDER BY us.created_at DESC
		LIMIT $2
	`, auth.UserID, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items, err := scanSubscriptions(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func scanSubscriptions(rows *sql.Rows) ([]map[string]any, error) {
	items := []map[string]any{}
	for rows.Next() {
		var id, userID, email, planID, planName, status, stripeSubscriptionID, groupID string
		var startsAt, createdAt, updatedAt time.Time
		var endsAt, periodStart, periodEnd sql.NullTime
		if err := rows.Scan(&id, &userID, &email, &planID, &planName, &status, &stripeSubscriptionID, &groupID, &startsAt, &endsAt, &periodStart, &periodEnd, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":                     id,
			"user_id":                userID,
			"email":                  email,
			"plan_id":                planID,
			"plan_name":              planName,
			"status":                 status,
			"stripe_subscription_id": stripeSubscriptionID,
			"granted_group_id":       groupID,
			"starts_at":              startsAt.UTC().Format(time.RFC3339),
			"ends_at":                nullableSQLTime(endsAt),
			"current_period_start":   nullableSQLTime(periodStart),
			"current_period_end":     nullableSQLTime(periodEnd),
			"created_at":             createdAt.UTC().Format(time.RFC3339),
			"updated_at":             updatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) adminRefundOrder(w http.ResponseWriter, r *http.Request, auth authContext) error {
	order, err := s.getOrder(r.Context(), r.PathValue("orderId"), "")
	if err != nil {
		return err
	}
	if !orderStatusAllowsRefund(order.Status) {
		return badRequest("Only paid or refund-blocked orders can be refunded.")
	}
	if reason, err := s.refundBlockReason(r.Context(), order); err != nil {
		return err
	} else if reason != "" {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE orders SET status = 'refund_blocked', refund_blocked_reason = $2 WHERE id = $1 AND status IN ('paid', 'refund_blocked')", order.ID, reason); err != nil {
			return err
		}
		blocked, _ := s.getOrder(r.Context(), order.ID, "")
		writeJSON(w, http.StatusOK, orderResponse(blocked), nil)
		return nil
	}
	settings, err := s.stripeSettings(r.Context())
	if err != nil {
		return err
	}
	stripeSecret := settings.SecretKey
	if provider, err := s.firstActivePaymentProvider(r.Context(), "stripe", "stripe"); err == nil {
		if providerSecret := s.paymentSecret(provider, "secret_key"); providerSecret != "" {
			stripeSecret = providerSecret
		}
	}
	if order.StripePaymentIntentID != "" && stripeSecret != "" {
		form := url.Values{}
		form.Set("payment_intent", order.StripePaymentIntentID)
		form.Set("metadata[order_id]", order.ID)
		body, err := stripePostForm(r.Context(), s.httpClient, stripeSecret, "https://api.stripe.com/v1/refunds", form)
		if err != nil {
			return err
		}
		var decoded struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &decoded)
		order.StripeRefundID = decoded.ID
	}
	if err := s.markOrderRefunded(r.Context(), order.ID, order.StripeRefundID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "order.refund", "order", order.ID, r, nil)
	refunded, _ := s.getOrder(r.Context(), order.ID, "")
	writeJSON(w, http.StatusOK, orderResponse(refunded), nil)
	return nil
}

func (s *Server) markOrderRefunded(ctx context.Context, orderID string, stripeRefundID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := getOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if order.Status == "refunded" {
		return tx.Commit()
	}
	if !orderStatusAllowsRefund(order.Status) {
		return nil
	}
	reversalAmount, err := refundReversalAmountTx(ctx, tx, order)
	if err != nil {
		return err
	}
	if parsePositiveFloat(reversalAmount, 0) > 0 {
		if err := debitWalletTx(ctx, tx, order.UserID, reversalAmount, "refund_reversal", "order", order.ID, map[string]any{"stripe_refund_id": stripeRefundID}); err != nil {
			if _, updateErr := tx.ExecContext(ctx, "UPDATE orders SET status = 'refund_blocked', refund_blocked_reason = $2 WHERE id = $1", order.ID, err.Error()); updateErr != nil {
				return updateErr
			}
			return tx.Commit()
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE orders
		SET status = 'refunded', refunded_at = now(), stripe_refund_id = COALESCE(NULLIF($2, ''), stripe_refund_id), refund_blocked_reason = ''
		WHERE id = $1
	`, order.ID, stripeRefundID); err != nil {
		return err
	}
	if order.OrderType == "subscription" {
		if _, err := tx.ExecContext(ctx, "UPDATE user_subscriptions SET status = 'canceled', ends_at = now() WHERE order_id = $1", order.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM user_group_memberships m
			USING user_subscriptions us
			WHERE us.order_id = $1
			  AND us.granted_group_id = m.group_id
			  AND us.user_id = m.user_id
			  AND m.role = 'subscription:' || us.id::text
		`, order.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func orderStatusAllowsRefund(status string) bool {
	return status == "paid" || status == "refund_blocked"
}

func getOrderForUpdate(ctx context.Context, tx *sql.Tx, orderID string) (orderRecord, error) {
	var order orderRecord
	err := tx.QueryRowContext(ctx, `
		SELECT id::text, user_id::text, COALESCE(plan_id::text, ''), status, amount_usd::text, currency, feature_flag,
		       metadata_json::text, order_type, COALESCE(stripe_checkout_session_id, ''), COALESCE(stripe_payment_intent_id, ''), COALESCE(stripe_subscription_id, ''),
		       COALESCE(stripe_refund_id, ''), COALESCE(checkout_url, ''), COALESCE(payment_provider, ''), COALESCE(payment_method, ''),
		       COALESCE(provider_instance_id::text, ''), COALESCE(pay_currency, ''), COALESCE(pay_amount_cents, 0), COALESCE(fx_rate::text, ''),
		       COALESCE(upstream_trade_no, ''), COALESCE(upstream_transaction_id, ''), paid_at, refunded_at, expires_at, COALESCE(refund_blocked_reason, ''), created_at, updated_at
		FROM orders
		WHERE id = $1
		FOR UPDATE
	`, orderID).Scan(
		&order.ID, &order.UserID, &order.PlanID, &order.Status, &order.AmountUSD, &order.Currency, &order.FeatureFlag,
		&order.Metadata, &order.OrderType, &order.StripeCheckoutSessionID, &order.StripePaymentIntentID, &order.StripeSubscriptionID,
		&order.StripeRefundID, &order.CheckoutURL, &order.PaymentProvider, &order.PaymentMethod, &order.ProviderInstanceID,
		&order.PayCurrency, &order.PayAmountCents, &order.FXRate, &order.UpstreamTradeNo, &order.UpstreamTransactionID,
		&order.PaidAt, &order.RefundedAt, &order.ExpiresAt, &order.RefundBlockedReason, &order.CreatedAt, &order.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return orderRecord{}, notFound("Order was not found.")
	}
	return order, err
}

func creditWalletTx(ctx context.Context, tx *sql.Tx, userID string, amount string, entryType string, referenceType string, referenceID string, metadata map[string]any) error {
	var walletID, balanceAfter, reservedAfter string
	if err := tx.QueryRowContext(ctx, `
		UPDATE wallet_accounts
		SET balance = balance + $2::numeric
		WHERE user_id = $1 AND status = 'active'
		RETURNING id::text, balance::text, reserved_balance::text
	`, userID, amount).Scan(&walletID, &balanceAfter, &reservedAfter); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id, metadata_json)
		VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric, $6, $7, $8::jsonb)
	`, walletID, entryType, amount, balanceAfter, reservedAfter, referenceType, referenceID, mustEncodeJSON(metadata))
	return err
}

func debitWalletTx(ctx context.Context, tx *sql.Tx, userID string, amount string, entryType string, referenceType string, referenceID string, metadata map[string]any) error {
	var walletID, balanceAfter, reservedAfter string
	err := tx.QueryRowContext(ctx, `
		UPDATE wallet_accounts
		SET balance = balance - $2::numeric
		WHERE user_id = $1
		  AND status = 'active'
		  AND balance - $2::numeric >= reserved_balance
		RETURNING id::text, balance::text, reserved_balance::text
	`, userID, amount).Scan(&walletID, &balanceAfter, &reservedAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return billingError("refund_blocked", "Wallet balance is insufficient to reverse this order.")
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id, metadata_json)
		VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric, $6, $7, $8::jsonb)
	`, walletID, entryType, amount, balanceAfter, reservedAfter, referenceType, referenceID, mustEncodeJSON(metadata))
	return err
}

func refundReversalAmountTx(ctx context.Context, tx *sql.Tx, order orderRecord) (string, error) {
	if order.OrderType != "subscription" {
		return order.AmountUSD, nil
	}
	var credit string
	err := tx.QueryRowContext(ctx, "SELECT wallet_credit_usd::text FROM subscription_plans WHERE id = $1", order.PlanID).Scan(&credit)
	return credit, err
}

func (s *Server) refundBlockReason(ctx context.Context, order orderRecord) (string, error) {
	amount := order.AmountUSD
	if order.OrderType == "subscription" {
		if err := s.db.QueryRowContext(ctx, "SELECT wallet_credit_usd::text FROM subscription_plans WHERE id = $1", order.PlanID).Scan(&amount); err != nil {
			return "", err
		}
	}
	if parsePositiveFloat(amount, 0) <= 0 {
		return "", nil
	}
	var balanceText, reservedText string
	err := s.db.QueryRowContext(ctx, `
		SELECT balance::text, reserved_balance::text
		FROM wallet_accounts
		WHERE user_id = $1 AND status = 'active'
	`, order.UserID).Scan(&balanceText, &reservedText)
	if errors.Is(err, sql.ErrNoRows) {
		return "Active wallet account was not found.", nil
	}
	if err != nil {
		return "", err
	}
	balance := parsePositiveFloat(balanceText, 0)
	reserved := parsePositiveFloat(reservedText, 0)
	reversal := parsePositiveFloat(amount, 0)
	if balance-reversal < reserved {
		return "Wallet balance is insufficient to reverse this order.", nil
	}
	return "", nil
}

func (s *Server) affiliateAttribution(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.attributeAffiliateCode(r.Context(), auth.UserID, "", req.Code); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"attributed": true}, nil)
	return nil
}

func (s *Server) attributeAffiliateCode(ctx context.Context, userID string, orderID string, code string) error {
	var codeID string
	err := s.db.QueryRowContext(ctx, "SELECT id::text FROM affiliate_codes WHERE lower(code) = lower($1) AND status = 'active'", strings.TrimSpace(code)).Scan(&codeID)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("Affiliate code was not found.")
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO affiliate_attributions (user_id, affiliate_code_id, order_id)
		VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (user_id, affiliate_code_id)
		DO UPDATE SET order_id = COALESCE(EXCLUDED.order_id, affiliate_attributions.order_id), status = 'active'
	`, userID, codeID, orderID)
	return err
}

func createAffiliateRebateTx(ctx context.Context, tx *sql.Tx, order orderRecord) error {
	var codeID, referredUserID, ownerUserID, rateText string
	err := tx.QueryRowContext(ctx, `
		SELECT ac.id::text, aa.user_id::text, ac.owner_user_id::text, ac.rebate_rate::text
		FROM affiliate_attributions aa
		JOIN affiliate_codes ac ON ac.id = aa.affiliate_code_id
		WHERE aa.user_id = $1
		  AND aa.status IN ('active', 'converted')
		ORDER BY aa.created_at ASC
		LIMIT 1
	`, order.UserID).Scan(&codeID, &referredUserID, &ownerUserID, &rateText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if ownerUserID == referredUserID {
		return nil
	}
	amount := parsePositiveFloat(order.AmountUSD, 0) * parsePositiveFloat(rateText, 0)
	if amount <= 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affiliate_rebates (affiliate_code_id, order_id, user_id, amount_usd, status, metadata_json)
		VALUES ($1, $2, $3, $4::numeric, 'pending', $5::jsonb)
	`, codeID, order.ID, order.UserID, formatMoney(amount), mustEncodeJSON(map[string]any{"rate": rateText}))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE affiliate_attributions SET status = 'converted', order_id = $2 WHERE user_id = $1 AND affiliate_code_id = $3", order.UserID, order.ID, codeID)
	return err
}

func (s *Server) adminAffiliateCodes(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.db.QueryContext(r.Context(), `
			SELECT ac.id::text, ac.owner_user_id::text, u.email::text, ac.code, ac.status, ac.rebate_rate::text, ac.metadata_json::text, ac.created_at, ac.updated_at
			FROM affiliate_codes ac
			JOIN users u ON u.id = ac.owner_user_id
			ORDER BY ac.created_at DESC
			LIMIT $1
		`, limitFromRequest(r, 100, 500))
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, ownerID, email, code, status, rate, metadata string
			var createdAt, updatedAt time.Time
			if err := rows.Scan(&id, &ownerID, &email, &code, &status, &rate, &metadata, &createdAt, &updatedAt); err != nil {
				return err
			}
			items = append(items, map[string]any{"id": id, "owner_user_id": ownerID, "email": email, "code": code, "status": status, "rebate_rate": rate, "metadata": jsonRaw(metadata), "created_at": createdAt.UTC().Format(time.RFC3339), "updated_at": updatedAt.UTC().Format(time.RFC3339)})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, items, nil)
		return nil
	case http.MethodPost:
		var req struct {
			OwnerUserID string         `json:"owner_user_id"`
			Code        string         `json:"code"`
			RebateRate  string         `json:"rebate_rate"`
			Status      string         `json:"status"`
			Metadata    map[string]any `json:"metadata"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if strings.TrimSpace(req.OwnerUserID) == "" || strings.TrimSpace(req.Code) == "" {
			return badRequest("owner_user_id and code are required.")
		}
		status, err := defaultedStatus(req.Status, "active", "active", "disabled")
		if err != nil {
			return err
		}
		rate := defaultString(req.RebateRate, "0.1")
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		var id string
		if err := s.db.QueryRowContext(r.Context(), `
			INSERT INTO affiliate_codes (owner_user_id, code, status, rebate_rate, metadata_json)
			VALUES ($1, $2, $3, $4::numeric, $5::jsonb)
			RETURNING id::text
		`, req.OwnerUserID, strings.TrimSpace(req.Code), status, rate, metadata).Scan(&id); err != nil {
			return err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "affiliate_code.create", "affiliate_code", id, r, nil)
		writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
		return nil
	default:
		return notFound("Endpoint was not found.")
	}
}

func (s *Server) adminAffiliateRebates(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT ar.id::text, ar.affiliate_code_id::text, ac.code, ac.owner_user_id::text, COALESCE(ar.order_id::text, ''),
		       ar.user_id::text, ar.amount_usd::text, ar.status, COALESCE(ar.wallet_ledger_id::text, ''),
		       ar.metadata_json::text, ar.settled_at, ar.created_at, ar.updated_at
		FROM affiliate_rebates ar
		JOIN affiliate_codes ac ON ac.id = ar.affiliate_code_id
		ORDER BY ar.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, codeID, code, ownerID, orderID, userID, amount, status, ledgerID, metadata string
		var settledAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &codeID, &code, &ownerID, &orderID, &userID, &amount, &status, &ledgerID, &metadata, &settledAt, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "affiliate_code_id": codeID, "code": code, "owner_user_id": ownerID, "order_id": orderID, "user_id": userID, "amount_usd": amount, "status": status, "wallet_ledger_id": ledgerID, "metadata": jsonRaw(metadata), "settled_at": nullableSQLTime(settledAt), "created_at": createdAt.UTC().Format(time.RFC3339), "updated_at": updatedAt.UTC().Format(time.RFC3339)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminSettleAffiliateRebate(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rebateID := r.PathValue("rebateId")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerID, amount, status string
	if err := tx.QueryRowContext(r.Context(), `
		SELECT ac.owner_user_id::text, ar.amount_usd::text, ar.status
		FROM affiliate_rebates ar
		JOIN affiliate_codes ac ON ac.id = ar.affiliate_code_id
		WHERE ar.id = $1
		FOR UPDATE OF ar
	`, rebateID).Scan(&ownerID, &amount, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("Affiliate rebate was not found.")
		}
		return err
	}
	if status != "pending" {
		return badRequest("Only pending rebates can be settled.")
	}
	if err := creditWalletTx(r.Context(), tx, ownerID, amount, "affiliate_rebate", "affiliate_rebate", rebateID, map[string]any{"settled_by": auth.UserID}); err != nil {
		return err
	}
	var ledgerID string
	_ = tx.QueryRowContext(r.Context(), `
		SELECT id::text FROM wallet_ledgers WHERE reference_type = 'affiliate_rebate' AND reference_id = $1 ORDER BY created_at DESC LIMIT 1
	`, rebateID).Scan(&ledgerID)
	if _, err := tx.ExecContext(r.Context(), "UPDATE affiliate_rebates SET status = 'settled', settled_at = now(), wallet_ledger_id = NULLIF($2, '')::uuid WHERE id = $1", rebateID, ledgerID); err != nil {
		return err
	}
	audit(r.Context(), tx, auth.UserID, "admin", "affiliate_rebate.settle", "affiliate_rebate", rebateID, r, nil)
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rebateID, "settled": true}, nil)
	return nil
}

func (s *Server) adminFinanceSummary(w http.ResponseWriter, r *http.Request, auth authContext) error {
	summary := map[string]any{}
	queries := map[string]string{
		"paid_revenue_usd":        "SELECT COALESCE(SUM(amount_usd), 0)::text FROM orders WHERE status = 'paid'",
		"refunded_usd":            "SELECT COALESCE(SUM(amount_usd), 0)::text FROM orders WHERE status = 'refunded'",
		"wallet_liability_usd":    "SELECT COALESCE(SUM(balance - reserved_balance), 0)::text FROM wallet_accounts WHERE status = 'active'",
		"subscription_mrr_usd":    "SELECT COALESCE(SUM(CASE WHEN sp.billing_period = 'year' THEN sp.price_usd / 12 ELSE sp.price_usd END), 0)::text FROM user_subscriptions us JOIN subscription_plans sp ON sp.id = us.plan_id WHERE us.status = 'active'",
		"affiliate_pending_usd":   "SELECT COALESCE(SUM(amount_usd), 0)::text FROM affiliate_rebates WHERE status = 'pending'",
		"usage_actual_cost_usd":   "SELECT COALESCE(SUM(actual_cost), 0)::text FROM usage_records WHERE status = 'success'",
		"refund_blocked_count":    "SELECT COUNT(*)::text FROM orders WHERE status = 'refund_blocked' OR refund_blocked_reason <> ''",
		"active_subscription_cnt": "SELECT COUNT(*)::text FROM user_subscriptions WHERE status = 'active'",
	}
	for key, query := range queries {
		var value string
		if err := s.db.QueryRowContext(r.Context(), query).Scan(&value); err != nil {
			return err
		}
		summary[key] = value
	}
	writeJSON(w, http.StatusOK, summary, nil)
	return nil
}

func (s *Server) adminEffectivePolicy(w http.ResponseWriter, r *http.Request, auth authContext) error {
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if userID == "" || model == "" {
		return badRequest("user_id and model are required.")
	}
	policy, err := s.resolveEffectivePolicy(r.Context(), userID, model, endpoint)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, effectivePolicySnapshot(policy), nil)
	return nil
}

func (s *Server) publicSiteSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT setting_key, setting_value_json::text
		FROM system_settings
		WHERE is_public = true
		ORDER BY setting_key
		LIMIT 200
	`)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()
	settings := map[string]any{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			writeError(w, r, err)
			return
		}
		settings[key] = jsonRaw(value)
	}
	stripeSettings, err := s.stripeSettings(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":         settings,
		"payments_enabled": stripeSettings.secretKeyConfigured(),
		"currency":         "USD",
	}, nil)
}

func (s *Server) publicPages(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, slug, title, body, page_type, public_visible, status, metadata_json::text, created_at, updated_at
		FROM content_pages
		WHERE public_visible = true AND status = 'published'
		ORDER BY page_type, slug
		LIMIT 200
	`)
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
	writeJSON(w, http.StatusOK, items, nil)
}

func (s *Server) getPaymentEvent(ctx context.Context, eventID string) (paymentEventRecord, error) {
	var event paymentEventRecord
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, COALESCE(order_id::text, ''), provider, provider_event_id, event_type,
		       payload_json::text, status, attempts, processed_at, COALESCE(processing_error, ''),
		       last_attempt_at, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at
		FROM payment_events
		WHERE id = $1
	`, eventID).Scan(
		&event.ID,
		&event.OrderID,
		&event.Provider,
		&event.ProviderEventID,
		&event.EventType,
		&event.Payload,
		&event.Status,
		&event.Attempts,
		&event.ProcessedAt,
		&event.ProcessingError,
		&event.LastAttemptAt,
		&event.NextAttemptAt,
		&event.LastError,
		&event.CreatedAt,
		&event.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return paymentEventRecord{}, notFound("Payment event was not found.")
	}
	return event, err
}

func scanPaymentEvent(scanner interface{ Scan(...any) error }) (paymentEventRecord, error) {
	var event paymentEventRecord
	err := scanner.Scan(
		&event.ID,
		&event.OrderID,
		&event.Provider,
		&event.ProviderEventID,
		&event.EventType,
		&event.Payload,
		&event.Status,
		&event.Attempts,
		&event.ProcessedAt,
		&event.ProcessingError,
		&event.LastAttemptAt,
		&event.NextAttemptAt,
		&event.LastError,
		&event.CreatedAt,
		&event.UpdatedAt,
	)
	return event, err
}

func paymentEventResponse(event paymentEventRecord) map[string]any {
	return map[string]any{
		"id":                event.ID,
		"order_id":          nullableString(event.OrderID),
		"provider":          event.Provider,
		"provider_event_id": event.ProviderEventID,
		"event_type":        event.EventType,
		"status":            event.Status,
		"attempts":          event.Attempts,
		"processed_at":      nullableSQLTime(event.ProcessedAt),
		"processing_error":  event.ProcessingError,
		"last_attempt_at":   nullableSQLTime(event.LastAttemptAt),
		"next_attempt_at":   nullableSQLTime(event.NextAttemptAt),
		"last_error":        event.LastError,
		"created_at":        event.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":        event.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) markPaymentEventProcessing(ctx context.Context, eventID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE payment_events
		SET status = 'processing',
		    attempts = attempts + 1,
		    last_attempt_at = now(),
		    next_attempt_at = now() + interval '1 minute',
		    last_error = '',
		    updated_at = now()
		WHERE id = $1
		  AND status IN ('pending', 'failed')
	`, eventID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return conflict("Payment event is not pending or failed.")
	}
	return nil
}

func (s *Server) markPaymentEventFailed(ctx context.Context, eventID string, processingErr error) error {
	message := truncateForStorage(processingErr.Error(), 1000)
	_, err := s.db.ExecContext(ctx, `
		UPDATE payment_events
		SET status = 'failed',
		    processing_error = $2,
		    last_error = $2,
		    next_attempt_at = now() + interval '5 minutes',
		    updated_at = now()
		WHERE id = $1
	`, eventID, message)
	return err
}

func (s *Server) markPaymentEventSucceeded(ctx context.Context, eventID string, status string) (paymentEventRecord, error) {
	if status != "processed" && status != "replayed" {
		status = "processed"
	}
	var event paymentEventRecord
	err := s.db.QueryRowContext(ctx, `
		UPDATE payment_events
		SET status = $2,
		    processed_at = now(),
		    processing_error = '',
		    last_error = '',
		    next_attempt_at = now(),
		    updated_at = now()
		WHERE id = $1
		RETURNING id::text, COALESCE(order_id::text, ''), provider, provider_event_id, event_type,
		          payload_json::text, status, attempts, processed_at, COALESCE(processing_error, ''),
		          last_attempt_at, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at
	`, eventID, status).Scan(
		&event.ID,
		&event.OrderID,
		&event.Provider,
		&event.ProviderEventID,
		&event.EventType,
		&event.Payload,
		&event.Status,
		&event.Attempts,
		&event.ProcessedAt,
		&event.ProcessingError,
		&event.LastAttemptAt,
		&event.NextAttemptAt,
		&event.LastError,
		&event.CreatedAt,
		&event.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return paymentEventRecord{}, notFound("Payment event was not found.")
	}
	return event, err
}

func validPaymentEventStatus(status string) bool {
	switch status {
	case "pending", "processing", "processed", "failed", "replayed":
		return true
	default:
		return false
	}
}

func paymentEventCanReplay(status string) bool {
	return status == "pending" || status == "failed"
}

func decodeStripeWebhookEvent(payload []byte) (stripeWebhookEvent, error) {
	var event stripeWebhookEvent
	if err := json.Unmarshal(payload, &event); err != nil || event.ID == "" {
		return stripeWebhookEvent{}, badRequest("Invalid Stripe webhook payload.")
	}
	if event.Data.Object == nil {
		event.Data.Object = map[string]any{}
	}
	return event, nil
}

func (s *Server) insertPaymentEvent(ctx context.Context, orderID string, eventID string, eventType string, payload []byte) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO payment_events (order_id, provider, provider_event_id, event_type, payload_json)
		VALUES (NULLIF($1, '')::uuid, 'stripe', $2, $3, $4::jsonb)
		ON CONFLICT DO NOTHING
		RETURNING id::text
	`, orderID, eventID, eventType, string(payload)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, id != "", err
}

func verifyStripeSignature(payload []byte, header string, secret string) bool {
	parts := strings.Split(header, ",")
	timestamp := ""
	signatures := []string{}
	for _, part := range parts {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if key == "t" {
			timestamp = value
		}
		if key == "v1" {
			signatures = append(signatures, value)
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return false
	}
	if unix, err := strconv.ParseInt(timestamp, 10, 64); err == nil && math.Abs(time.Since(time.Unix(unix, 0)).Seconds()) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, signature := range signatures {
		if hmac.Equal([]byte(expected), []byte(signature)) {
			return true
		}
	}
	return false
}

func stripeOrderID(object map[string]any) string {
	if metadata, ok := object["metadata"].(map[string]any); ok {
		if value, ok := metadata["order_id"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Server) lookupStripeOrderID(ctx context.Context, object map[string]any) string {
	fields := []struct {
		column string
		value  string
	}{
		{"stripe_checkout_session_id", stripeObjectString(object, "id")},
		{"stripe_payment_intent_id", stripeObjectString(object, "payment_intent")},
		{"stripe_subscription_id", stripeObjectString(object, "subscription")},
	}
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		var orderID string
		query := "SELECT id::text FROM orders WHERE " + field.column + " = $1 ORDER BY created_at DESC LIMIT 1"
		if err := s.db.QueryRowContext(ctx, query, field.value).Scan(&orderID); err == nil {
			return orderID
		}
	}
	return ""
}

func stripeObjectString(object map[string]any, key string) string {
	switch value := object[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		if id, ok := value["id"].(string); ok {
			return strings.TrimSpace(id)
		}
	default:
		return ""
	}
	return ""
}

func stripeInvoiceIsSubscriptionRenewal(object map[string]any) bool {
	reason := strings.TrimSpace(stripeObjectString(object, "billing_reason"))
	if reason == "" {
		return false
	}
	return reason == "subscription_cycle" || reason == "subscription_update"
}

func stripeInvoicePeriod(object map[string]any) (*time.Time, *time.Time) {
	var startAt, endAt *time.Time
	if unix, ok := stripeUnixTime(object["period_start"]); ok {
		value := time.Unix(unix, 0).UTC()
		startAt = &value
	}
	if unix, ok := stripeUnixTime(object["period_end"]); ok {
		value := time.Unix(unix, 0).UTC()
		endAt = &value
	}
	return startAt, endAt
}

func stripeUnixTime(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), typed > 0
	case int64:
		return typed, typed > 0
	case int:
		return int64(typed), typed > 0
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil && parsed > 0
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil && parsed > 0
	default:
		return 0, false
	}
}

func subscriptionPeriodEnd(start time.Time, period string) time.Time {
	switch period {
	case "year":
		return start.UTC().AddDate(1, 0, 0)
	case "month":
		return start.UTC().AddDate(0, 1, 0)
	default:
		return start.UTC()
	}
}

func (s *Server) billingSuccessURL(orderID string) string {
	settings, err := s.stripeSettings(context.Background())
	if err == nil {
		return settings.successURL(orderID, s.cfg.PortalBaseURL)
	}
	return strings.TrimRight(s.cfg.PortalBaseURL, "/") + "/?billing=success&order_id=" + url.QueryEscape(orderID)
}

func (s *Server) billingCancelURL(orderID string) string {
	settings, err := s.stripeSettings(context.Background())
	if err == nil {
		return settings.cancelURL(orderID, s.cfg.PortalBaseURL)
	}
	return strings.TrimRight(s.cfg.PortalBaseURL, "/") + "/?billing=cancel&order_id=" + url.QueryEscape(orderID)
}

func usdCents(amount string) int {
	parsed, _ := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	return int(math.Round(parsed * 100))
}

func stripeInterval(period string) string {
	if period == "year" {
		return "year"
	}
	return "month"
}
