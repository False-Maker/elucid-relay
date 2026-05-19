import { useQuery } from "@tanstack/react-query";
import type { Wallet } from "@elucid-relay/contracts";
import { apiRequest } from "@/lib/api-client";
import { useAuthStore } from "@/lib/stores/auth";

export function useWallet() {
  const token = useAuthStore((s) => s.token);
  return useQuery({
    queryKey: ["portal", "wallet"],
    queryFn: ({ signal }) =>
      apiRequest<Wallet>("/api/portal/v1/wallet", token, { signal }),
    enabled: !!token,
    staleTime: 30_000,
  });
}

export type LedgerEntry = {
  id: string;
  entry_type: string;
  amount: string;
  balance_after: string;
  reserved_after: string;
  description?: string;
  created_at: string;
};

export function useWalletLedger() {
  const token = useAuthStore((s) => s.token);
  return useQuery({
    queryKey: ["portal", "wallet", "ledger"],
    queryFn: ({ signal }) =>
      apiRequest<LedgerEntry[]>("/api/portal/v1/wallet/ledger", token, {
        signal,
      }),
    enabled: !!token,
    staleTime: 60_000,
  });
}
