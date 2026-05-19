import type { ReactNode } from "react";
import type { Wallet } from "@elucid-relay/contracts";

export type ApiKeyRecord = {
  id: string;
  display_prefix: string;
  name: string;
  status: string;
  routing_mode: string;
  expires_at: string | null;
  model_scope: string[];
  ip_allowlist: string[];
  created_at: string;
};

export type OAuthOption = {
  id: string;
  provider_id?: string;
  name: string;
  provider_type?: string;
  client_type?: string;
  abilities?: Array<{ model_name: string; endpoint: string; upstream_model: string }>;
};

export type OAuthOptions = {
  providers: OAuthOption[];
  channels: OAuthOption[];
  provider_clients: OAuthOption[];
  auth_modes: string[];
};

export type PortalNavTarget = "billing" | "keys" | "playground" | "usage" | "models" | "security" | "docs";

export function available(wallet: Wallet | null) {
  if (!wallet) return "0.00";
  return (Number(wallet.balance) - Number(wallet.reserved_balance)).toFixed(2);
}

export function splitCSV(value: string) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

export function optionLabel(option: OAuthOption) {
  const ability = option.abilities?.[0];
  if (!ability) return option.name;
  return `${option.name} · ${ability.model_name}/${ability.endpoint}`;
}

export function oauthProgress(row: any) {
  return row?.latest_job?.oauth_progress || row?.latest_job?.result?.oauth_progress || {};
}

export function authModeLabel(value: string) {
  const labels: Record<string, string> = {
    codex_cli: "Codex CLI",
    oauth: "OAuth",
  };
  return labels[value] ?? value;
}

export function EmptyState({ title, detail, action }: { title: string; detail: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <div>
        <strong>{title}</strong>
        <span>{detail}</span>
      </div>
      {action}
    </div>
  );
}
