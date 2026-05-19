package httpserver

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/config"
	"github.com/redis/go-redis/v9"
)

type Server struct {
	cfg                            config.Config
	db                             *sql.DB
	redis                          *redis.Client
	httpClient                     *http.Client
	upstreamPool                   *upstreamClientPool
	codexModels                    *codexModelsCache
	trustedNets                    []*net.IPNet
	settingsMu                     sync.Mutex
	reverseProxySettingsCache      reverseProxySettings
	reverseProxySettingsCacheUntil time.Time
}

func New(cfg config.Config, database *sql.DB, redisClient *redis.Client) *http.Server {
	trustedNets := trustedProxyNetworks(cfg.TrustedProxyCIDRs)
	app := &Server{
		cfg:         cfg,
		db:          database,
		redis:       redisClient,
		trustedNets: trustedNets,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		upstreamPool: newUpstreamClientPool(),
		codexModels:  newCodexModelsCache(),
	}

	mux := http.NewServeMux()
	app.registerRoutes(mux)

	server := &http.Server{
		Addr:              cfg.HTTPAddr(),
		Handler:           app.recover(app.cors(app.clientIP(app.rateLimit(app.requestID(mux))))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if database != nil {
		cleanupCtx, cancel := context.WithCancel(context.Background())
		server.RegisterOnShutdown(cancel)
		app.startExpiredNorthboundCleanup(cleanupCtx, expiredNorthboundCleanupInterval)
		app.startNotificationDispatcher(cleanupCtx, 15*time.Second)
		app.startAccountPoolWorker(cleanupCtx, time.Minute)
		app.startPaymentReconciler(cleanupCtx, time.Minute)
	}
	return server
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("HEAD /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("HEAD /readyz", s.readyz)
	mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	mux.HandleFunc("POST /api/setup", s.setupCreateOwner)
	mux.HandleFunc("GET /api/public/v1/pricing", s.publicPricing)
	mux.HandleFunc("GET /api/public/v1/channel-status", s.publicChannelStatus)
	mux.HandleFunc("GET /api/public/v1/rankings", s.publicRankings)
	mux.HandleFunc("GET /api/public/v1/announcements", s.publicAnnouncements)
	mux.HandleFunc("GET /api/public/v1/site-settings", s.publicSiteSettings)
	mux.HandleFunc("GET /api/public/v1/pages", s.publicPages)
	mux.HandleFunc("GET /api/public/v1/pages/{slug}", s.publicContentPage)
	mux.HandleFunc("POST /api/billing/v1/stripe/webhook", s.stripeWebhook)
	mux.HandleFunc("POST /api/billing/v1/payment/webhook/{provider}/{providerId}", s.paymentWebhook)

	mux.HandleFunc("POST /api/auth/v1/login", s.unifiedLogin)
	mux.HandleFunc("GET /api/portal/v1/auth/registration-email-verification", s.portalRegistrationEmailVerificationStatus)
	mux.HandleFunc("POST /api/portal/v1/auth/registration-email-verification", s.portalRequestRegistrationEmailCode)
	mux.HandleFunc("POST /api/portal/v1/auth/register", s.portalRegister)
	mux.HandleFunc("POST /api/portal/v1/auth/login", s.portalLogin)
	mux.HandleFunc("POST /api/portal/v1/auth/password-reset/request", s.portalRequestPasswordReset)
	mux.HandleFunc("POST /api/portal/v1/auth/password-reset/confirm", s.portalConfirmPasswordReset)
	mux.HandleFunc("POST /api/portal/v1/auth/email-verification/confirm", s.portalConfirmEmailVerification)
	mux.HandleFunc("POST /api/portal/v1/auth/logout", s.withPortalSession(s.portalLogout))
	mux.HandleFunc("GET /api/portal/v1/me", s.withPortalSession(s.portalMe))
	mux.HandleFunc("POST /api/portal/v1/me/email-verification", s.withPortalSession(s.portalRequestEmailVerification))
	mux.HandleFunc("GET /api/portal/v1/wallet", s.withPortalSession(s.portalWallet))
	mux.HandleFunc("GET /api/portal/v1/wallet/ledger", s.withPortalSession(s.portalWalletLedger))
	mux.HandleFunc("GET /api/portal/v1/spend-limits", s.withPortalSession(s.portalSpendLimits))
	mux.HandleFunc("POST /api/portal/v1/redeem", s.withPortalSession(s.portalRedeem))
	mux.HandleFunc("GET /api/portal/v1/api-keys", s.withPortalSession(s.portalAPIKeys))
	mux.HandleFunc("POST /api/portal/v1/api-keys", s.withPortalSession(s.portalCreateAPIKey))
	mux.HandleFunc("PATCH /api/portal/v1/api-keys/{apiKeyId}", s.withPortalSession(s.portalPatchAPIKey))
	mux.HandleFunc("PUT /api/portal/v1/api-keys/{apiKeyId}/spend-limit", s.withPortalSession(s.portalPutAPIKeySpendLimit))
	mux.HandleFunc("GET /api/portal/v1/api-keys/{apiKeyId}/usage", s.withPortalSession(s.portalAPIKeyUsage))
	mux.HandleFunc("DELETE /api/portal/v1/api-keys/{apiKeyId}", s.withPortalSession(s.portalDeleteAPIKey))
	mux.HandleFunc("GET /api/portal/v1/oauth/options", s.withPortalSession(s.portalOAuthOptions))
	mux.HandleFunc("GET /api/portal/v1/oauth/accounts", s.withPortalSession(s.portalOAuthAccounts))
	mux.HandleFunc("POST /api/portal/v1/oauth/accounts", s.withPortalSession(s.portalCreateOAuthAccount))
	mux.HandleFunc("POST /api/portal/v1/oauth/jobs/{jobId}/input", s.withPortalSession(s.portalSubmitOAuthJobInput))
	mux.HandleFunc("POST /api/portal/v1/oauth/accounts/{accountId}/reauth", s.withPortalSession(s.portalReauthOAuthAccount))
	mux.HandleFunc("POST /api/portal/v1/oauth/accounts/{accountId}/revoke", s.withPortalSession(s.portalRevokeOAuthAccount))
	mux.HandleFunc("GET /api/portal/v1/usage", s.withPortalSession(s.portalUsage))
	mux.HandleFunc("GET /api/portal/v1/models", s.withPortalSession(s.portalModels))
	mux.HandleFunc("GET /api/portal/v1/subscription-plans", s.withPortalSession(s.portalSubscriptionPlans))
	mux.HandleFunc("GET /api/portal/v1/payment-methods", s.withPortalSession(s.portalPaymentMethods))
	mux.HandleFunc("GET /api/portal/v1/orders", s.withPortalSession(s.portalOrders))
	mux.HandleFunc("POST /api/portal/v1/orders", s.withPortalSession(s.portalOrders))
	mux.HandleFunc("GET /api/portal/v1/orders/{orderId}", s.withPortalSession(s.portalOrder))
	mux.HandleFunc("POST /api/portal/v1/orders/{orderId}/checkout", s.withPortalSession(s.portalOrderCheckout))
	mux.HandleFunc("GET /api/portal/v1/subscriptions", s.withPortalSession(s.portalSubscriptions))
	mux.HandleFunc("POST /api/portal/v1/affiliate-attribution", s.withPortalSession(s.affiliateAttribution))

	mux.HandleFunc("POST /api/admin/v1/auth/login", s.adminLogin)
	mux.HandleFunc("POST /api/admin/v1/auth/logout", s.withAdminSession(s.adminLogout))
	mux.HandleFunc("GET /api/admin/v1/me", s.withAdminSession(s.adminMe))
	mux.HandleFunc("GET /api/admin/v1/overview", s.withAdminSessionPermission(adminPermOverview, s.adminOverview))
	mux.HandleFunc("GET /api/admin/v1/ops/overview", s.withAdminSessionPermission(adminPermOverview, s.adminOpsOverview))
	mux.HandleFunc("GET /api/admin/v1/setup/checklist", s.withAdminSessionPermission(adminPermOverview, s.adminSetupChecklist))
	mux.HandleFunc("POST /api/admin/v1/setup/checks/{checkId}/test", s.withAdminSessionPermission(adminPermOverview, s.adminTestSetupCheck))
	mux.HandleFunc("GET /api/admin/v1/system-settings/effective", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminEffectiveSystemSettings))
	mux.HandleFunc("GET /api/admin/v1/reverse-proxy-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminReverseProxySettings))
	mux.HandleFunc("PUT /api/admin/v1/reverse-proxy-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminReverseProxySettings))
	mux.HandleFunc("POST /api/admin/v1/reverse-proxy/param-override-preview", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminReverseProxyParamOverridePreview))
	mux.HandleFunc("GET /api/admin/v1/stripe-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminStripeSettings))
	mux.HandleFunc("PUT /api/admin/v1/stripe-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPutStripeSettings))
	mux.HandleFunc("POST /api/admin/v1/stripe-settings/test", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminTestStripeSettings))
	mux.HandleFunc("GET /api/admin/v1/payment-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentSettings))
	mux.HandleFunc("PUT /api/admin/v1/payment-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentSettings))
	mux.HandleFunc("GET /api/admin/v1/payment-providers", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentProviders))
	mux.HandleFunc("POST /api/admin/v1/payment-providers", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentProviders))
	mux.HandleFunc("PATCH /api/admin/v1/payment-providers/{providerId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchPaymentProvider))
	mux.HandleFunc("POST /api/admin/v1/payment-providers/{providerId}/test", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminTestPaymentProvider))
	mux.HandleFunc("GET /api/admin/v1/payment-method-routes", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentMethodRoutes))
	mux.HandleFunc("PUT /api/admin/v1/payment-method-routes", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentMethodRoutes))
	mux.HandleFunc("GET /api/admin/v1/users", s.withAdminSessionPermission(adminPermUsersRead, s.adminUsers))
	mux.HandleFunc("POST /api/admin/v1/users", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateUser))
	mux.HandleFunc("GET /api/admin/v1/users/{userId}", s.withAdminSessionPermission(adminPermUsersRead, s.adminUserDetail))
	mux.HandleFunc("PATCH /api/admin/v1/users/{userId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchUser))
	mux.HandleFunc("POST /api/admin/v1/users/{userId}/password-reset", s.withAdminSessionPermission(adminPermUsersReset, s.adminCreateUserPasswordReset))
	mux.HandleFunc("GET /api/admin/v1/users/{userId}/api-keys", s.withAdminSessionPermission(adminPermUsersRead, s.adminUserAPIKeys))
	mux.HandleFunc("GET /api/admin/v1/users/{userId}/wallet", s.withAdminSessionPermission(adminPermUsersRead, s.adminUserWallet))
	mux.HandleFunc("GET /api/admin/v1/users/{userId}/wallet/ledger", s.withAdminSessionPermission(adminPermUsersRead, s.adminUserWalletLedger))
	mux.HandleFunc("POST /api/admin/v1/users/{userId}/wallet/adjustments", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminWalletAdjustment))
	mux.HandleFunc("GET /api/admin/v1/redeem-codes", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminRedeemCodes))
	mux.HandleFunc("POST /api/admin/v1/redeem-codes", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateRedeemCodes))
	mux.HandleFunc("PATCH /api/admin/v1/redeem-codes/{redeemCodeId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchRedeemCode))
	mux.HandleFunc("GET /api/admin/v1/redeem-codes/{redeemCodeId}/claims", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminRedeemCodeClaims))
	mux.HandleFunc("GET /api/admin/v1/usage", s.withAdminSessionPermission(adminPermUsageRead, s.adminUsage))
	mux.HandleFunc("GET /api/admin/v1/usage/summary", s.withAdminSessionPermission(adminPermUsageRead, s.adminUsageSummary))
	mux.HandleFunc("GET /api/admin/v1/usage/analytics", s.withAdminSessionPermission(adminPermUsageRead, s.adminUsageAnalytics))
	mux.HandleFunc("GET /api/admin/v1/usage/export", s.withAdminSessionPermission(adminPermUsageRead, s.adminUsageExport))
	mux.HandleFunc("POST /api/admin/v1/usage/cleanup", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminUsageCleanup))
	mux.HandleFunc("GET /api/admin/v1/audit", s.withAdminSessionPermission(adminPermAudit, s.adminAudit))
	mux.HandleFunc("GET /api/admin/v1/spend-limits", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminSpendLimits))
	mux.HandleFunc("PUT /api/admin/v1/spend-limits", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPutSpendLimit))
	mux.HandleFunc("GET /api/admin/v1/notification-channels", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminNotificationChannels))
	mux.HandleFunc("POST /api/admin/v1/notification-channels", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateNotificationChannel))
	mux.HandleFunc("PATCH /api/admin/v1/notification-channels/{channelId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchNotificationChannel))
	mux.HandleFunc("POST /api/admin/v1/notification-channels/{channelId}/test", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminTestNotificationChannel))
	mux.HandleFunc("GET /api/admin/v1/notification-events", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminNotificationEvents))
	mux.HandleFunc("POST /api/admin/v1/notification-events/{eventId}/retry", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminRetryNotificationEvent))
	mux.HandleFunc("GET /api/admin/v1/payment-events", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPaymentEvents))
	mux.HandleFunc("POST /api/admin/v1/payment-events/{eventId}/replay", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminReplayPaymentEvent))
	mux.HandleFunc("GET /api/admin/v1/models", s.withAdminSessionPermission(adminPermModels, s.adminModels))
	mux.HandleFunc("POST /api/admin/v1/models", s.withAdminSessionPermission(adminPermModels, s.adminCreateModel))
	mux.HandleFunc("GET /api/admin/v1/models/conflicts", s.withAdminSessionPermission(adminPermModels, s.adminModelConflicts))
	mux.HandleFunc("GET /api/admin/v1/models/missing", s.withAdminSessionPermission(adminPermModels, s.adminMissingModels))
	mux.HandleFunc("POST /api/admin/v1/models/batch", s.withAdminSessionPermission(adminPermModels, s.adminModelBatch))
	mux.HandleFunc("POST /api/admin/v1/models/sync-from-channels", s.withAdminSessionPermission(adminPermModels, s.adminModelSyncFromChannels))
	mux.HandleFunc("PATCH /api/admin/v1/models/{modelName}", s.withAdminSessionPermission(adminPermModels, s.adminPatchModel))
	mux.HandleFunc("GET /api/admin/v1/announcements", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAnnouncements))
	mux.HandleFunc("POST /api/admin/v1/announcements", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateAnnouncement))
	mux.HandleFunc("PATCH /api/admin/v1/announcements/{announcementId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchAnnouncement))
	mux.HandleFunc("GET /api/admin/v1/content-pages", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminContentPages))
	mux.HandleFunc("POST /api/admin/v1/content-pages", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateContentPage))
	mux.HandleFunc("PATCH /api/admin/v1/content-pages/{pageId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchContentPage))
	mux.HandleFunc("GET /api/admin/v1/groups", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminGroups))
	mux.HandleFunc("POST /api/admin/v1/groups", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateGroup))
	mux.HandleFunc("PATCH /api/admin/v1/groups/{groupId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchGroup))
	mux.HandleFunc("GET /api/admin/v1/groups/effective-policy", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminEffectivePolicy))
	mux.HandleFunc("POST /api/admin/v1/groups/{groupId}/members", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAddGroupMember))
	mux.HandleFunc("DELETE /api/admin/v1/groups/{groupId}/members/{userId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminDeleteGroupMember))
	mux.HandleFunc("POST /api/admin/v1/groups/{groupId}/model-permissions", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAddGroupModelPermission))
	mux.HandleFunc("GET /api/admin/v1/email-verification-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminEmailVerificationSettings))
	mux.HandleFunc("PUT /api/admin/v1/email-verification-settings", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPutEmailVerificationSettings))
	mux.HandleFunc("POST /api/admin/v1/email-verification-settings/test", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminTestEmailVerificationSettings))
	mux.HandleFunc("GET /api/admin/v1/risk-controls", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminRiskControls))
	mux.HandleFunc("POST /api/admin/v1/risk-controls", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCreateRiskControl))
	mux.HandleFunc("PATCH /api/admin/v1/risk-controls/{ruleId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchRiskControl))
	mux.HandleFunc("GET /api/admin/v1/risk-events", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminRiskEvents))
	mux.HandleFunc("GET /api/admin/v1/providers", s.withAdminSessionPermission(adminPermUpstream, s.adminProviders))
	mux.HandleFunc("POST /api/admin/v1/providers", s.withAdminSessionPermission(adminPermUpstream, s.adminCreateProvider))
	mux.HandleFunc("PATCH /api/admin/v1/providers/{providerId}", s.withAdminSessionPermission(adminPermUpstream, s.adminPatchProvider))
	mux.HandleFunc("GET /api/admin/v1/provider-clients", s.withAdminSessionPermission(adminPermUpstream, s.adminProviderClients))
	mux.HandleFunc("POST /api/admin/v1/provider-clients", s.withAdminSessionPermission(adminPermUpstream, s.adminCreateProviderClient))
	mux.HandleFunc("PATCH /api/admin/v1/provider-clients/{providerClientId}", s.withAdminSessionPermission(adminPermUpstream, s.adminPatchProviderClient))
	mux.HandleFunc("GET /api/admin/v1/channels", s.withAdminSessionPermission(adminPermUpstream, s.adminChannels))
	mux.HandleFunc("POST /api/admin/v1/channels", s.withAdminSessionPermission(adminPermUpstream, s.adminCreateChannel))
	mux.HandleFunc("PATCH /api/admin/v1/channels/{channelId}", s.withAdminSessionPermission(adminPermUpstream, s.adminPatchChannel))
	mux.HandleFunc("POST /api/admin/v1/channels/{channelId}/model-sync", s.withAdminSessionPermission(adminPermUpstream, s.adminChannelModelSync))
	mux.HandleFunc("GET /api/admin/v1/model-sync-jobs", s.withAdminSessionPermission(adminPermUpstream, s.adminModelSyncJobs))
	mux.HandleFunc("POST /api/admin/v1/model-sync-jobs", s.withAdminSessionPermission(adminPermUpstream, s.adminRunModelSyncJobs))
	mux.HandleFunc("GET /api/admin/v1/accounts", s.withAdminSessionPermission(adminPermPool, s.adminAccounts))
	mux.HandleFunc("POST /api/admin/v1/accounts", s.withAdminSessionPermission(adminPermPool, s.adminCreateAccount))
	mux.HandleFunc("GET /api/admin/v1/accounts/export", s.withAdminSessionPermission(adminPermPool, s.adminExportAccounts))
	mux.HandleFunc("GET /api/admin/v1/account-import-templates", s.withAdminSessionPermission(adminPermPool, s.adminAccountImportTemplates))
	mux.HandleFunc("POST /api/admin/v1/accounts/import-preview", s.withAdminSessionPermission(adminPermPool, s.adminPreviewAccountImport))
	mux.HandleFunc("POST /api/admin/v1/accounts/import", s.withAdminSessionPermission(adminPermPool, s.adminImportAccounts))
	mux.HandleFunc("POST /api/admin/v1/accounts/import-keys", s.withAdminSessionPermission(adminPermPool, s.adminImportAccountKeys))
	mux.HandleFunc("POST /api/admin/v1/accounts/batch", s.withAdminSessionPermission(adminPermPool, s.adminAccountBatch))
	mux.HandleFunc("POST /api/admin/v1/accounts/health-check", s.withAdminSessionPermission(adminPermPool, s.adminAccountHealthCheck))
	mux.HandleFunc("POST /api/admin/v1/accounts/quota-refresh", s.withAdminSessionPermission(adminPermPool, s.adminRefreshAccountsQuota))
	mux.HandleFunc("GET /api/admin/v1/account-quality", s.withAdminSessionPermission(adminPermPool, s.adminAccountQuality))
	mux.HandleFunc("POST /api/admin/v1/account-quality/recompute", s.withAdminSessionPermission(adminPermPool, s.adminRecomputeAccountQuality))
	mux.HandleFunc("GET /api/admin/v1/account-pool-strategy-events", s.withAdminSessionPermission(adminPermPool, s.adminAccountPoolStrategyEvents))
	mux.HandleFunc("GET /api/admin/v1/account-wakeup-jobs", s.withAdminSessionPermission(adminPermPool, s.adminWakeupJobs))
	mux.HandleFunc("POST /api/admin/v1/account-wakeup-jobs", s.withAdminSessionPermission(adminPermPool, s.adminCreateWakeupJob))
	mux.HandleFunc("POST /api/admin/v1/account-wakeup-jobs/{jobId}/run", s.withAdminSessionPermission(adminPermPool, s.adminRunWakeupJob))
	mux.HandleFunc("GET /api/admin/v1/account-platform-configs", s.withAdminSessionPermission(adminPermPool, s.adminAccountPlatformConfigs))
	mux.HandleFunc("PUT /api/admin/v1/account-platform-configs/{providerType}", s.withAdminSessionPermission(adminPermPool, s.adminPutAccountPlatformConfig))
	mux.HandleFunc("PATCH /api/admin/v1/accounts/{accountId}", s.withAdminSessionPermission(adminPermPool, s.adminPatchAccount))
	mux.HandleFunc("POST /api/admin/v1/accounts/{accountId}/quality-action", s.withAdminSessionPermission(adminPermPool, s.adminAccountQualityAction))
	mux.HandleFunc("POST /api/admin/v1/accounts/{accountId}/quota-refresh", s.withAdminSessionPermission(adminPermPool, s.adminRefreshAccountQuota))
	mux.HandleFunc("GET /api/admin/v1/account-pool-groups", s.withAdminSessionPermission(adminPermPool, s.adminAccountPoolGroups))
	mux.HandleFunc("POST /api/admin/v1/account-pool-groups", s.withAdminSessionPermission(adminPermPool, s.adminCreateAccountPoolGroup))
	mux.HandleFunc("PATCH /api/admin/v1/account-pool-groups/{groupId}", s.withAdminSessionPermission(adminPermPool, s.adminPatchAccountPoolGroup))
	mux.HandleFunc("POST /api/admin/v1/account-pool-groups/{groupId}/members", s.withAdminSessionPermission(adminPermPool, s.adminAddAccountPoolGroupMember))
	mux.HandleFunc("DELETE /api/admin/v1/account-pool-groups/{groupId}/members/{accountId}", s.withAdminSessionPermission(adminPermPool, s.adminDeleteAccountPoolGroupMember))
	mux.HandleFunc("GET /api/admin/v1/account-auth-states", s.withAdminSessionPermission(adminPermOAuth, s.adminAccountAuthStates))
	mux.HandleFunc("PATCH /api/admin/v1/account-auth-states/{accountId}", s.withAdminSessionPermission(adminPermOAuth, s.adminPatchAccountAuthState))
	mux.HandleFunc("GET /api/admin/v1/oauth/jobs", s.withAdminSessionPermission(adminPermOAuth, s.adminOAuthJobs))
	mux.HandleFunc("POST /api/admin/v1/oauth/jobs", s.withAdminSessionPermission(adminPermOAuth, s.adminCreateOAuthJob))
	mux.HandleFunc("POST /api/admin/v1/oauth/jobs/{jobId}/input", s.withAdminSessionPermission(adminPermOAuth, s.adminSubmitOAuthJobInput))
	mux.HandleFunc("PATCH /api/admin/v1/oauth/jobs/{jobId}", s.withAdminSessionPermission(adminPermOAuth, s.adminPatchOAuthJob))
	mux.HandleFunc("GET /api/admin/v1/account-quota-snapshots", s.withAdminSessionPermission(adminPermPool, s.adminQuotaSnapshots))
	mux.HandleFunc("GET /api/admin/v1/account-quota-refresh-jobs", s.withAdminSessionPermission(adminPermPool, s.adminQuotaRefreshJobs))
	mux.HandleFunc("GET /api/admin/v1/account-quota-windows", s.withAdminSessionPermission(adminPermPool, s.adminQuotaWindows))
	mux.HandleFunc("POST /api/admin/v1/account-quota-windows", s.withAdminSessionPermission(adminPermPool, s.adminCreateQuotaWindow))
	mux.HandleFunc("PATCH /api/admin/v1/account-quota-windows/{quotaWindowId}", s.withAdminSessionPermission(adminPermPool, s.adminPatchQuotaWindow))
	mux.HandleFunc("GET /api/admin/v1/proxies", s.withAdminSessionPermission(adminPermProxies, s.adminProxies))
	mux.HandleFunc("POST /api/admin/v1/proxies", s.withAdminSessionPermission(adminPermProxies, s.adminCreateProxy))
	mux.HandleFunc("GET /api/admin/v1/proxies/export", s.withAdminSessionPermission(adminPermProxies, s.adminExportProxies))
	mux.HandleFunc("POST /api/admin/v1/proxies/import", s.withAdminSessionPermission(adminPermProxies, s.adminImportProxies))
	mux.HandleFunc("POST /api/admin/v1/proxies/batch", s.withAdminSessionPermission(adminPermProxies, s.adminProxyBatch))
	mux.HandleFunc("POST /api/admin/v1/proxies/quality", s.withAdminSessionPermission(adminPermProxies, s.adminProxyQuality))
	mux.HandleFunc("GET /api/admin/v1/proxy-test-results", s.withAdminSessionPermission(adminPermProxies, s.adminProxyTestResults))
	mux.HandleFunc("POST /api/admin/v1/proxies/{proxyId}/test", s.withAdminSessionPermission(adminPermProxies, s.adminTestProxy))
	mux.HandleFunc("PATCH /api/admin/v1/proxies/{proxyId}", s.withAdminSessionPermission(adminPermProxies, s.adminPatchProxy))
	mux.HandleFunc("DELETE /api/admin/v1/proxies/{proxyId}", s.withAdminSessionPermission(adminPermProxies, s.adminDeleteProxy))
	mux.HandleFunc("GET /api/admin/v1/channel-tests", s.withAdminSessionPermission(adminPermUpstream, s.adminChannelTests))
	mux.HandleFunc("POST /api/admin/v1/channel-tests", s.withAdminSessionPermission(adminPermUpstream, s.adminCreateChannelTest))
	mux.HandleFunc("GET /api/admin/v1/runtime/route-explain", s.withAdminSessionPermission(adminPermOverview, s.adminRouteExplain))
	mux.HandleFunc("GET /api/admin/v1/runtime/overview", s.withAdminSessionPermission(adminPermOverview, s.adminRuntimeOverview))
	mux.HandleFunc("GET /api/admin/v1/runtime/affinity-stats", s.withAdminSessionPermission(adminPermOverview, s.adminAffinityStats))
	mux.HandleFunc("POST /api/admin/v1/runtime/affinity-cleanup", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAffinityCleanup))
	mux.HandleFunc("GET /api/admin/v1/subscription-plans", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminSubscriptionPlans))
	mux.HandleFunc("POST /api/admin/v1/subscription-plans", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminSubscriptionPlans))
	mux.HandleFunc("PATCH /api/admin/v1/subscription-plans/{planId}", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminPatchSubscriptionPlan))
	mux.HandleFunc("GET /api/admin/v1/orders", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminOrders))
	mux.HandleFunc("POST /api/admin/v1/orders/{orderId}/refund", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminRefundOrder))
	mux.HandleFunc("GET /api/admin/v1/subscriptions", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminSubscriptions))
	mux.HandleFunc("POST /api/admin/v1/subscriptions/{subscriptionId}/cancel", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminCancelSubscription))
	mux.HandleFunc("GET /api/admin/v1/affiliate-codes", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAffiliateCodes))
	mux.HandleFunc("POST /api/admin/v1/affiliate-codes", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAffiliateCodes))
	mux.HandleFunc("GET /api/admin/v1/affiliate-rebates", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminAffiliateRebates))
	mux.HandleFunc("POST /api/admin/v1/affiliate-rebates/{rebateId}/settle", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminSettleAffiliateRebate))
	mux.HandleFunc("GET /api/admin/v1/finance/summary", s.withAdminSessionPermission(adminPermPlatformOwner, s.adminFinanceSummary))

	mux.HandleFunc("POST /api/oauth-wrapper/v1/jobs/claim", s.oauthClaimJob)
	mux.HandleFunc("GET /api/oauth-wrapper/v1/jobs/{jobId}/input", s.oauthJobInput)
	mux.HandleFunc("POST /api/oauth-wrapper/v1/jobs/{jobId}/progress", s.oauthJobProgress)
	mux.HandleFunc("POST /api/oauth-wrapper/v1/jobs/{jobId}/complete", s.oauthJobComplete)
	mux.HandleFunc("POST /api/oauth-wrapper/v1/jobs/{jobId}/fail", s.oauthJobFail)

	mux.HandleFunc("GET /v1/models", s.northboundModels)
	mux.HandleFunc("GET /v1/models/{model}", s.northboundModel)
	mux.HandleFunc("GET /v1/usage", s.northboundKeyUsage)
	mux.HandleFunc("POST /v1/chat/completions", s.northboundProxy)
	mux.HandleFunc("POST /v1/responses", s.northboundProxy)
	mux.HandleFunc("GET /v1/responses", s.northboundResponsesWebSocket)
	mux.HandleFunc("POST /v1/messages", s.northboundProxy)
	mux.HandleFunc("GET /v1/files", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/files", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/files/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/mcp_servers", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/mcp_servers/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/sessions/ws/{rest...}", s.northboundClaudeCodeWebSocket)
	mux.HandleFunc("GET /v1/sessions", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/sessions", s.northboundClaudeCodeProxy)
	mux.HandleFunc("PATCH /v1/sessions/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/sessions/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/sessions/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/code/sessions", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/code/sessions/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/code/sessions/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/session_ingress/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/environment_providers", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/environment_providers/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/environment_providers/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /v1/environments/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/environments/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("DELETE /v1/environments/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /api/oauth/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /api/oauth/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("PATCH /api/oauth/{rest...}", s.northboundClaudeCodeProxy)
	mux.HandleFunc("GET /api/claude_cli_profile", s.northboundClaudeCodeProxy)
	mux.HandleFunc("POST /v1/embeddings", s.northboundProxy)
	mux.HandleFunc("POST /v1/images/generations", s.northboundProxy)
	mux.HandleFunc("POST /v1/audio/transcriptions", s.northboundProxy)
	mux.HandleFunc("POST /v1/audio/translations", s.northboundProxy)
	mux.HandleFunc("POST /v1/audio/speech", s.northboundProxy)
	mux.HandleFunc("POST /v1/realtime/sessions", s.northboundProxy)
	mux.HandleFunc("GET /v1/realtime", s.northboundRealtimeWebSocket)
	mux.HandleFunc("GET /v1/realtime/{rest...}", s.northboundRealtimeWebSocket)
	mux.HandleFunc("POST /v1/rerank", s.northboundProxy)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("ok\n"))
	}
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	status, checks, httpStatus := s.readinessState(ctx)

	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodHead {
		writeJSON(w, httpStatus, map[string]any{
			"status": status,
			"checks": checks,
			"time":   time.Now().UTC().Format(time.RFC3339),
		}, nil)
		return
	}
	w.WriteHeader(httpStatus)
}
