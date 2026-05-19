import { useQuery } from "@tanstack/react-query";
import type { ModelRecord } from "@elucid-relay/contracts";
import { apiRequest } from "@/lib/api-client";
import { useAuthStore } from "@/lib/stores/auth";

export function useModels() {
  const token = useAuthStore((s) => s.token);
  return useQuery({
    queryKey: ["portal", "models"],
    queryFn: ({ signal }) =>
      apiRequest<ModelRecord[]>("/api/portal/v1/models", token, { signal }),
    enabled: !!token,
    staleTime: 5 * 60_000,
  });
}
