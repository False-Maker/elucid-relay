import type { ApiEnvelope, ApiFailure } from "@elucid-relay/contracts";

export const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:18080";
export const RELAY_API_BASE = import.meta.env.VITE_RELAY_API_BASE_URL || API_BASE;

// Chinese translations for common API error messages.
const ERROR_LABELS: Record<string, string> = {
  "A valid email is required.": "请输入有效邮箱。",
  "Password must be at least 8 characters.": "密码至少需要 8 位。",
  "Invalid email or password.": "邮箱或密码错误。",
  "User is disabled.": "账号已停用。",
  "Portal is only available to personal users.":
    "该账号是管理员账号，正在切换到管理后台。",
  "Admin permission is required.": "需要管理员权限。",
  "Authentication is required.": "请先登录。",
  "Invalid or expired session.": "登录已过期，请重新登录。",
  "Email verification code is required.": "请输入邮箱验证码。",
  "Invalid or expired email verification code.":
    "邮箱验证码错误或已过期。",
  "Verification email could not be sent.":
    "验证码邮件发送失败，请检查邮箱配置。",
  "SMTP is not configured.": "SMTP 尚未配置。",
  "CSRF token is required.": "会话校验失败，请刷新后重试。",
  "Invalid JSON request body.": "请求 JSON 格式不正确。",
};

export class ApiError extends Error {
  code: string;
  status: number;

  constructor(message: string, code: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

function translateError(failure: ApiFailure | undefined, status: number): ApiError {
  const raw = failure?.error.message ?? `HTTP ${status}`;
  const message = ERROR_LABELS[raw] ?? raw;
  const code = failure?.error.code ?? "UNKNOWN";
  return new ApiError(message, code, status);
}

type RequestOptions = RequestInit & {
  /** Skip Authorization header even when token is available. */
  anonymous?: boolean;
};

/**
 * Typed fetch wrapper for the Relay gateway API.
 *
 * Features vs the old ApiClient:
 * - Returns typed `ApiError` (code + status) instead of raw Error
 * - Designed for React Query (pure functions, no internal state mutation)
 * - Token is passed explicitly per-call via the `token` parameter
 */
export async function apiRequest<T>(
  path: string,
  token: string | null,
  init: RequestOptions = {},
): Promise<T> {
  const headers = new Headers(init.headers);
  if (!headers.has("Content-Type") && init.body) {
    headers.set("Content-Type", "application/json");
  }
  if (token && !init.anonymous) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers,
    signal: init.signal,
  });

  const payload = (await response.json().catch(() => ({}))) as
    | ApiEnvelope<T>
    | ApiFailure;

  if (!response.ok || "error" in payload) {
    throw translateError(
      "error" in payload ? (payload as ApiFailure) : undefined,
      response.status,
    );
  }

  return (payload as ApiEnvelope<T>).data;
}

/** Unauthenticated request (public endpoints, auth flows). */
export async function publicRequest<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  return apiRequest<T>(path, null, { ...init, anonymous: true });
}
