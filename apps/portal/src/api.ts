import type { ApiEnvelope, ApiFailure, SessionResponse } from "@elucid-relay/contracts";

export const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:18080";
export const RELAY_API_BASE = import.meta.env.VITE_RELAY_API_BASE_URL || API_BASE;

export function apiErrorMessage(failure: ApiFailure | undefined, fallback: string) {
  const message = failure?.error.message || fallback;
  const labels: Record<string, string> = {
    "A valid email is required.": "请输入有效邮箱。",
    "Password must be at least 8 characters.": "密码至少需要 8 位。",
    "Invalid email or password.": "邮箱或密码错误。",
    "User is disabled.": "账号已停用。",
    "Portal is only available to personal users.": "该账号是管理员账号，正在切换到管理后台。",
    "Admin permission is required.": "需要管理员权限。",
    "Authentication is required.": "请先登录。",
    "Invalid or expired session.": "登录已过期，请重新登录。",
    "Email verification code is required.": "请输入邮箱验证码。",
    "Invalid or expired email verification code.": "邮箱验证码错误或已过期。",
    "Verification email could not be sent.": "验证码邮件发送失败，请检查邮箱配置。",
    "SMTP is not configured.": "SMTP 尚未配置。",
    "Test email could not be sent.": "测试邮件发送失败，请检查 SMTP 配置。",
    "CSRF token is required.": "会话校验失败，请刷新后重试。",
    "Invalid JSON request body.": "请求 JSON 格式不正确。",
    "Invalid JSON field.": "JSON 字段格式不正确。",
    "Provider name is required.": "请输入供应商名称。",
    "provider_id and name are required.": "请选择供应商并填写名称。",
    "provider_id, name, and base_url are required.": "请选择供应商，并填写通道名称和基础地址。",
    "name and proxy_url are required.": "请填写代理名称和代理地址。",
    "Channel ability requires model_name and endpoint.": "通道能力需要填写模型和端点。",
    "account_id and window_type are required.": "请选择账号并填写配额类型。",
    "account_id is required for channel tests.": "请选择一个绑定到该通道的上游账号再检测。",
    "model and endpoint query parameters are required.": "请填写模型和端点。",
    "user_id is required when routing_mode=byo.": "自带账号路由需要选择所属用户。",
  };
  return labels[message] ?? message;
}

export async function publicRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (!headers.has("Content-Type") && init.body) headers.set("Content-Type", "application/json");

  const response = await fetch(`${API_BASE}${path}`, { ...init, headers });
  const payload = (await response.json().catch(() => ({}))) as ApiEnvelope<T> | ApiFailure;
  if (!response.ok || "error" in payload) {
    throw new Error(apiErrorMessage("error" in payload ? payload : undefined, `HTTP ${response.status}`));
  }
  return (payload as ApiEnvelope<T>).data;
}

export class ApiClient {
  token = localStorage.getItem("portal_session_token") || "";

  setToken(token: string) {
    this.token = token;
    localStorage.setItem("portal_session_token", token);
  }

  clearToken() {
    this.token = "";
    localStorage.removeItem("portal_session_token");
  }

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers);
    if (!headers.has("Content-Type") && init.body) headers.set("Content-Type", "application/json");
    const token = this.token || localStorage.getItem("admin_session_token") || "";
    if (token) headers.set("Authorization", `Bearer ${token}`);

    const response = await fetch(`${API_BASE}${path}`, { ...init, headers });
    const payload = (await response.json().catch(() => ({}))) as ApiEnvelope<T> | ApiFailure;
    if (!response.ok || "error" in payload) {
      throw new Error(apiErrorMessage("error" in payload ? payload : undefined, `HTTP ${response.status}`));
    }
    return (payload as ApiEnvelope<T>).data;
  }

  async auth(path: string, email: string, password: string, displayName = "", verificationCode = "") {
    const data = await this.request<SessionResponse>(path, {
      method: "POST",
      body: JSON.stringify({ email, password, display_name: displayName, verification_code: verificationCode }),
    });
    this.setToken(data.session.session_token);
    return data;
  }

  async login(email: string, password: string) {
    return this.request<SessionResponse>("/api/auth/v1/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    });
  }
}

export const api = new ApiClient();
