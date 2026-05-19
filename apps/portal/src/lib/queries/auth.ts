import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { SessionResponse } from "@elucid-relay/contracts";
import { apiRequest, publicRequest } from "@/lib/api-client";
import { useAuthStore } from "@/lib/stores/auth";

export function useBootstrap() {
  const { token } = useAuthStore();
  return useQuery({
    queryKey: ["auth", "me"],
    queryFn: ({ signal }) =>
      apiRequest<{ user: import("@elucid-relay/contracts").RelayUser }>(
        "/api/portal/v1/me",
        token,
        { signal },
      ).then((d) => d.user),
    enabled: !!token,
    retry: false,
    staleTime: 5 * 60_000,
  });
}

export function useLogin() {
  const setSession = useAuthStore((s) => s.setSession);
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (vars: { email: string; password: string }) =>
      publicRequest<SessionResponse>("/api/auth/v1/login", {
        method: "POST",
        body: JSON.stringify(vars),
      }),
    onSuccess: (data) => {
      setSession(
        data.session.session_token,
        data.session.csrf_token,
        data.session.audience,
        data.user,
      );
      qc.invalidateQueries({ queryKey: ["auth"] });
    },
  });
}

export function useRegister() {
  const setSession = useAuthStore((s) => s.setSession);
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (vars: {
      email: string;
      password: string;
      display_name?: string;
      verification_code?: string;
    }) =>
      publicRequest<SessionResponse>("/api/portal/v1/auth/register", {
        method: "POST",
        body: JSON.stringify(vars),
      }),
    onSuccess: (data) => {
      setSession(
        data.session.session_token,
        data.session.csrf_token,
        data.session.audience,
        data.user,
      );
      qc.invalidateQueries({ queryKey: ["auth"] });
    },
  });
}

export function useLogout() {
  const { token, clearSession } = useAuthStore();
  const qc = useQueryClient();

  return useMutation({
    mutationFn: () =>
      apiRequest<void>("/api/portal/v1/auth/logout", token, {
        method: "POST",
      }),
    onSettled: () => {
      clearSession();
      qc.clear();
    },
  });
}

export function useRegistrationEmailVerification() {
  return useQuery({
    queryKey: ["auth", "email-verification-required"],
    queryFn: ({ signal }) =>
      publicRequest<{ required: boolean }>(
        "/api/portal/v1/auth/registration-email-verification",
        { signal },
      ).then((d) => d.required),
    staleTime: 10 * 60_000,
  });
}

export function useRequestRegistrationCode() {
  return useMutation({
    mutationFn: (vars: { email: string }) =>
      publicRequest<void>(
        "/api/portal/v1/auth/registration-email-verification",
        { method: "POST", body: JSON.stringify(vars) },
      ),
  });
}
