import type { RelayUser } from "@elucid-relay/contracts";

export type AdminView = "overview" | "ops" | "users" | "redeem" | "models" | "pool" | "upstream" | "proxies" | "oauth" | "billing" | "controls" | "usage" | "content" | "groups" | "risk" | "public" | "audit" | "my_dashboard" | "my_keys" | "my_billing" | "my_usage" | "my_models" | "my_oauth" | "my_playground" | "my_docs" | "my_security";
export type AdminRole = Extract<RelayUser["user_type"], "operator" | "platform_owner">;

export type AdminTarget = {
  view: AdminView;
  tab?: string;
};

export const billingTabs = ["overview", "payments", "providers", "plans", "orders", "events", "subscriptions", "affiliates"] as const;
export type BillingTab = typeof billingTabs[number];

export const upstreamTabs = ["channels", "providers", "clients", "sync", "tests"] as const;
export type UpstreamTab = typeof upstreamTabs[number];

export const poolTabs = ["overview", "accounts", "groups", "cockpit", "batch", "add", "edit", "quota", "health", "quality", "events", "wakeup", "platforms", "import", "route"] as const;
export type PoolTab = typeof poolTabs[number];

export type SetupChecklistItem = {
  id: string;
  label: string;
  complete: boolean;
  target_view: AdminView | string;
};

export type SetupChecklistResponse = {
  items: SetupChecklistItem[];
  config_status: Record<string, unknown>;
};

export type PaymentSettings = {
  enabled: boolean;
  fx_usd_cny: string;
  order_timeout_minutes: number;
  max_pending_orders_per_user: number;
  cancel_cooldown_seconds: number;
  auto_reconcile_enabled: boolean;
  provider_selection: "priority" | "weighted" | string;
  help_text: string;
  stripe_success_url: string;
  stripe_cancel_url: string;
  stripe_success_url_source?: string;
  stripe_cancel_url_source?: string;
};

export type PaymentProvider = {
  id: string;
  provider_type: "stripe" | "easypay" | "alipay" | "wechat" | string;
  name: string;
  status: "active" | "disabled" | string;
  priority: number;
  weight: number;
  supported_methods: string[];
  min_amount_usd: string;
  max_amount_usd: string;
  daily_limit_usd: string;
  config: Record<string, unknown>;
  secret_configured: Record<string, boolean>;
  metadata: Record<string, unknown>;
  webhook_url: string;
  created_at: string;
  updated_at: string;
};

export type PaymentMethodRoute = {
  method: "stripe" | "alipay" | "wechat" | string;
  enabled: boolean;
  display_name: string;
  provider_types: string[];
  min_amount_usd: string;
  max_amount_usd: string;
  metadata: Record<string, unknown>;
};
