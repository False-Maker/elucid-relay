import { useQuery } from "@tanstack/react-query";
import { apiRequest } from "@/lib/api-client";
import { useAuthStore } from "@/lib/stores/auth";

export type UsageRecord = {
  id: string;
  request_id: string;
  api_key_id: string;
  requested_model: string;
  upstream_model: string;
  endpoint: string;
  input_tokens: number;
  output_tokens: number;
  image_count: number;
  request_count: number;
  estimated_cost: string;
  actual_cost: string;
  upstream_status: number;
  duration_ms: number;
  status: string;
  created_at: string;
};

export type UsageFilters = {
  model?: string;
  status?: string;
  api_key_id?: string;
  date_from?: string;
  date_to?: string;
  limit?: number;
};

export function useUsage(filters: UsageFilters = {}) {
  const token = useAuthStore((s) => s.token);

  const params = new URLSearchParams();
  if (filters.model) params.set("model", filters.model);
  if (filters.status) params.set("status", filters.status);
  if (filters.api_key_id) params.set("api_key_id", filters.api_key_id);
  if (filters.date_from) params.set("date_from", filters.date_from);
  if (filters.date_to) params.set("date_to", filters.date_to);
  params.set("limit", String(filters.limit ?? 100));

  const qs = params.toString();
  return useQuery({
    queryKey: ["portal", "usage", filters],
    queryFn: ({ signal }) =>
      apiRequest<UsageRecord[]>(
        `/api/portal/v1/usage?${qs}`,
        token,
        { signal },
      ),
    enabled: !!token,
    staleTime: 30_000,
  });
}
