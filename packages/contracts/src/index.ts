export type ApiEnvelope<T> = {
  data: T;
  meta?: unknown;
};

export type ApiFailure = {
  error: {
    code: string;
    message: string;
    type: string;
    request_id?: string;
  };
};

export type SessionResponse = {
  user: RelayUser;
  workspace: SessionAudience;
  session: {
    session_id: string;
    session_token: string;
    csrf_token: string;
    expires_at: string;
    audience: SessionAudience;
  };
};

export type SessionAudience = "portal" | "admin";

export type RelayUser = {
  id: string;
  user_type: "personal_user" | "operator" | "platform_owner";
  email: string;
  display_name: string;
  status: "active" | "disabled" | "pending";
  email_verified_at?: string | null;
};

export type Wallet = {
  id: string;
  user_id: string;
  balance: string;
  reserved_balance: string;
  currency: "USD";
  status: string;
};

export type ModelPricing = {
  input_usd_per_1k: string;
  output_usd_per_1k: string;
  request_usd: string;
  min_charge_usd: string;
  billing_mode?: "standard" | "tiered_expr" | string;
  billing_expr?: string;
  cache_read_usd_per_1k?: string;
  cache_write_usd_per_1k?: string;
  image_usd_per_unit?: string;
  audio_usd_per_second?: string;
};

export type ModelRecord = {
  model_name: string;
  display_name: string;
  provider_hint: string;
  aliases: string[];
  endpoint_capabilities: string[];
  pricing: ModelPricing;
  effective_pricing?: ModelPricing | null;
  billing_multiplier?: string | null;
  active_channel_count?: number;
  active_account_count?: number;
  providers?: string[];
  available_endpoints?: string[];
  health?: Record<string, unknown>;
  description?: string;
  icon?: string;
  tags?: string[];
  vendor?: string;
  pricing_version?: string;
  supported_endpoint_types?: string[];
  metadata?: Record<string, unknown>;
  public_visible: boolean;
  status: string;
};

export type AdminOverview = {
  metrics: Record<string, string>;
  config_status: Record<string, unknown>;
  checklist: SetupChecklistItem[];
};

export type SetupChecklistItem = {
  id: string;
  label: string;
  complete: boolean;
  target_view: string;
};

export type SetupChecklistResponse = {
  items: SetupChecklistItem[];
  config_status: Record<string, unknown>;
};

export type StripeSettings = {
  secret_key_configured: boolean;
  webhook_secret_configured: boolean;
  secret_key_source?: string;
  webhook_secret_source?: string;
  success_url: string;
  cancel_url: string;
  success_url_source?: string;
  cancel_url_source?: string;
  mode?: string;
};

export type PaymentEventRecord = {
  id: string;
  order_id?: string | null;
  provider: string;
  provider_event_id: string;
  event_type: string;
  status: "pending" | "processed" | "failed" | "replayed" | string;
  attempts: number;
  processed_at?: string | null;
  last_attempt_at?: string | null;
  next_attempt_at?: string | null;
  processing_error?: string;
  last_error?: string;
  created_at: string;
  updated_at?: string;
};

export type AdminUserDetail = {
  user: RelayUser & {
    last_login_at?: string | null;
    created_at: string;
    updated_at: string;
  };
  wallet?: Wallet | null;
  counts: Record<string, string>;
  groups: Array<{
    id: string;
    name: string;
    status: string;
    role: string;
    created_at: string;
  }>;
  orders: Array<Record<string, unknown>>;
  subscriptions: Array<Record<string, unknown>>;
  audit: AdminAuditRecord[];
};

export type AdminAuditRecord = {
  id: string;
  actor_user_id: string;
  actor_type: string;
  action: string;
  target_type: string;
  target_id: string;
  ip_address: string;
  user_agent: string;
  metadata: unknown;
  created_at: string;
};
