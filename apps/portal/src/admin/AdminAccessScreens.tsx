import { useState } from "react";
import type { RelayUser, SessionResponse } from "@elucid-relay/contracts";
import { adminApi } from "../adminApi";
import { publicRequest } from "../api";

export function SetupLoading() {
  return (
    <div className="auth">
      <section className="auth-panel">
        <div className="auth-brand">
          <span className="brand-mark"><span /></span>
          <div>
            <h1>Elucid Relay</h1>
            <p>正在检查初始化状态</p>
          </div>
        </div>
      </section>
    </div>
  );
}

export function SetupOffline() {
  return (
    <div className="auth">
      <section className="auth-panel">
        <div className="auth-brand">
          <span className="brand-mark"><span /></span>
          <div>
            <h1>Elucid Relay</h1>
            <p>Gateway API 未连接</p>
          </div>
        </div>
        <div className="error">
          管理员登录需要后端服务。请先启动 `docker compose up --build`，并确认 `http://localhost:18080/healthz` 返回 `ok`。
        </div>
      </section>
    </div>
  );
}

export function AdminUnavailable() {
  return (
    <div className="auth">
      <section className="auth-panel">
        <div className="auth-brand">
          <span className="brand-mark"><span /></span>
          <div>
            <h1>Elucid Relay</h1>
            <p>请从统一登录入口进入管理后台</p>
          </div>
        </div>
      </section>
    </div>
  );
}

export function AdminSetup({ onAuthed }: { onAuthed: (user: RelayUser) => void }) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function submit() {
    setError("");
    setLoading(true);
    try {
      const data = await publicRequest<SessionResponse>("/api/setup", {
        method: "POST",
        body: JSON.stringify({ email, password, display_name: displayName }),
      });
      adminApi.setToken(data.session.session_token);
      onAuthed(data.user);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="auth">
      <section className="auth-panel">
        <div className="auth-brand">
          <span className="brand-mark"><span /></span>
          <div>
            <h1>初始化 Elucid Relay</h1>
            <p>创建第一个平台管理员账号</p>
          </div>
        </div>
        <input value={email} onChange={(event) => setEmail(event.target.value)} placeholder="管理员邮箱" autoComplete="email" />
        <input value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="显示名称" />
        <input value={password} onChange={(event) => setPassword(event.target.value)} placeholder="管理员密码" type="password" autoComplete="new-password" />
        {error && <div className="error">{error}</div>}
        <button className="primary" onClick={submit} disabled={loading}>{loading ? "创建中..." : "创建管理员"}</button>
      </section>
    </div>
  );
}
