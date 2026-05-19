import type { ApiEnvelope, ApiFailure, SessionResponse } from "@elucid-relay/contracts";
import { API_BASE, apiErrorMessage } from "./api";

export class AdminApiClient {
  token = localStorage.getItem("admin_session_token") || "";

  setToken(token: string) {
    this.token = token;
    localStorage.setItem("admin_session_token", token);
  }

  clearToken() {
    this.token = "";
    localStorage.removeItem("admin_session_token");
  }

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    if (!headers.has("Content-Type") && init.body) headers.set("Content-Type", "application/json");
    if (this.token) headers.set("Authorization", `Bearer ${this.token}`);

    const response = await fetch(`${API_BASE}${path}`, { ...init, headers });
    const payload = (await response.json().catch(() => ({}))) as ApiEnvelope<T> | ApiFailure;
    if (!response.ok || "error" in payload) {
      throw new Error(apiErrorMessage("error" in payload ? payload : undefined, `HTTP ${response.status}`));
    }
    return (payload as ApiEnvelope<T>).data;
  }

  async login(email: string, password: string) {
    const data = await this.request<SessionResponse>("/api/admin/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    });
    this.setToken(data.session.session_token);
    return data;
  }
}

export const adminApi = new AdminApiClient();
