import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { RelayUser, SessionAudience } from "@elucid-relay/contracts";

interface AuthState {
  /** Portal or admin session token. */
  token: string | null;
  /** CSRF token returned by the login response. */
  csrfToken: string | null;
  /** Current audience (portal or admin). */
  audience: SessionAudience | null;
  /** Current user (set after successful login / bootstrap). */
  user: RelayUser | null;

  setSession: (token: string, csrfToken: string, audience: SessionAudience, user: RelayUser) => void;
  setUser: (user: RelayUser) => void;
  clearSession: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      csrfToken: null,
      audience: null,
      user: null,

      setSession: (token, csrfToken, audience, user) =>
        set({ token, csrfToken, audience, user }),

      setUser: (user) => set({ user }),

      clearSession: () =>
        set({ token: null, csrfToken: null, audience: null, user: null }),
    }),
    {
      name: "elucid-relay-auth",
      // Only persist token + audience, not the full user object.
      partialize: (state) => ({
        token: state.token,
        csrfToken: state.csrfToken,
        audience: state.audience,
      }),
    },
  ),
);
