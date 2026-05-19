import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiRequest } from "@/lib/api-client";
import { useAuthStore } from "@/lib/stores/auth";

export type ApiKeyRecord = {
  id: string;
  name: string;
  key_prefix: string;
  routing_mode: string;
  status: string;
  model_scope_json: string;
  ip_allowlist_json: string;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
};

export type CreateKeyResponse = ApiKeyRecord & { secret: string };

export function useApiKeys() {
  const token = useAuthStore((s) => s.token);
  return useQuery({
    queryKey: ["portal", "api-keys"],
    queryFn: ({ signal }) =>
      apiRequest<ApiKeyRecord[]>("/api/portal/v1/api-keys", token, { signal }),
    enabled: !!token,
    staleTime: 30_000,
  });
}

export function useCreateApiKey() {
  const token = useAuthStore((s) => s.token);
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (vars: {
      name: string;
      routing_mode?: string;
      model_scope?: string[];
      ip_allowlist?: string[];
      expires_at?: string;
    }) =>
      apiRequest<CreateKeyResponse>("/api/portal/v1/api-keys", token, {
        method: "POST",
        body: JSON.stringify(vars),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "api-keys"] });
    },
  });
}

export function useToggleApiKey() {
  const token = useAuthStore((s) => s.token);
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (vars: { id: string; status: "active" | "disabled" }) =>
      apiRequest<void>(`/api/portal/v1/api-keys/${vars.id}`, token, {
        method: "PATCH",
        body: JSON.stringify({ status: vars.status }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "api-keys"] });
    },
  });
}

export function useDeleteApiKey() {
  const token = useAuthStore((s) => s.token);
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (id: string) =>
      apiRequest<void>(`/api/portal/v1/api-keys/${id}`, token, {
        method: "DELETE",
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "api-keys"] });
    },
  });
}
