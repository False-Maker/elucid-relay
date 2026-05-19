import { useEffect, useMemo, useState } from "react";
import { BookOpen, CreditCard, Database, Gauge, Globe2, KeyRound, Link, ListChecks, LogOut, PlayCircle, ShieldCheck } from "lucide-react";
import type { RelayUser } from "@elucid-relay/contracts";
import { AdminConsole } from "./AdminConsole";
import { adminApi } from "./adminApi";
import { api, publicRequest } from "./api";
import { ProductShell, type NavGroup } from "./components/ProductShell";
import { BillingView, DocsView, ModelsView, OAuthView, PlaygroundView, PublicInfoView, SecurityView, UsageView } from "./portal/PortalViews";
import { AuthScreen } from "./features/auth/AuthScreen";
import { Dashboard } from "./features/dashboard/Dashboard";
import { KeysPage } from "./features/keys/KeysPage";
import { useAuthStore } from "./lib/stores/auth";

type Workspace = "auth" | "portal" | "admin" | "public";
type View = "dashboard" | "billing" | "keys" | "oauth" | "security" | "usage" | "models" | "playground" | "public" | "docs";

const viewDetails: Record<View, { title: string; description: string }> = {
  dashboard: { title: "概览", description: "钱包、密钥和近期请求活动。" },
  billing: { title: "计费钱包", description: "充值、订阅、订单、推广和流水。" },
  keys: { title: "API 密钥", description: "按路由、模型、IP 和过期时间控制的访问凭证。" },
  oauth: { title: "自带账号池", description: "上传并管理自己的上游账号和授权任务。" },
  security: { title: "个人设置", description: "邮箱、安全状态和消费限制。" },
  usage: { title: "用量", description: "按密钥、模型、状态和成本查看请求日志。" },
  models: { title: "模型", description: "可用模型目录、能力和价格。" },
  playground: { title: "Playground", description: "用自己的 API Key 发起网关测试请求。" },
  public: { title: "公共信息", description: "公开价格、渠道状态、排行榜和公告。" },
  docs: { title: "文档", description: "基础地址、认证方式和 API 示例。" },
};

export function App() {
  const [workspace, setWorkspace] = useState<Workspace>("auth");
  const [user, setUser] = useState<RelayUser | null>(null);
  const [view, setView] = useState<View>("dashboard");
  const [apiOffline, setApiOffline] = useState(false);

  async function loadMe() {
    if (adminApi.token) {
      try {
        const nextUser = await adminApi.request<RelayUser>("/api/admin/v1/me");
        api.clearToken();
        setUser(nextUser);
        setWorkspace("admin");
        setView("dashboard");
        return;
      } catch {
        adminApi.clearToken();
      }
    }

    if (api.token) {
      try {
        const nextUser = await api.request<RelayUser>("/api/portal/v1/me");
        adminApi.clearToken();
        setUser(nextUser);
        setWorkspace("portal");
        setView("dashboard");
        return;
      } catch {
        api.clearToken();
      }
    }
    setUser(null);
    setWorkspace("auth");
  }

  useEffect(() => {
    async function bootstrap() {
      try {
        const status = await publicRequest<{ initialized: boolean }>("/api/setup/status");
        setApiOffline(false);
        if (!status.initialized) {
          api.clearToken();
          adminApi.clearToken();
          setUser(null);
          setWorkspace("admin");
          return;
        }
        await loadMe();
      } catch {
        setApiOffline(true);
        await loadMe();
      }
    }
    void bootstrap();
  }, []);

  async function logout() {
    if (workspace === "admin") {
      await adminApi.request("/api/admin/v1/auth/logout", { method: "POST" }).catch(() => undefined);
      adminApi.clearToken();
    } else {
      await api.request("/api/portal/v1/auth/logout", { method: "POST" }).catch(() => undefined);
      api.clearToken();
    }
    setUser(null);
    setWorkspace("auth");
  }

  if (workspace === "admin") {
    return (
      <AdminConsole
        user={user}
        onAuthed={(nextUser) => {
          api.clearToken();
          setUser(nextUser);
          setWorkspace("admin");
        }}
        onLogout={logout}
      />
    );
  }

  if (workspace === "public") {
    const publicView = view === "docs" ? "docs" : "public";
    const meta = viewDetails[publicView];
    const publicNavGroups: NavGroup<View>[] = [
      {
        label: "公开信息",
        items: [
          { id: "public", icon: Globe2, label: "价格与状态" },
          { id: "docs", icon: BookOpen, label: "API 文档" },
        ],
      },
    ];

    return (
      <ProductShell
        title={meta.title}
        description={meta.description}
        crumb={meta.title}
        brandLabel="Elucid Relay"
        brandSubtitle="公共信息"
        navGroups={publicNavGroups}
        activeId={publicView}
        onSelect={setView}
        statusLabel="公开访问"
        footer={(
          <button className="workspace-button" onClick={() => { setWorkspace("auth"); setView("dashboard"); }}>
            <KeyRound size={16} /> 返回登录
          </button>
        )}
      >
        {publicView === "public" && <PublicInfoView />}
        {publicView === "docs" && <DocsView />}
      </ProductShell>
    );
  }

  if (!user) {
    return (
      <AuthScreen
        onAuthed={(next, workspace, token) => {
          if (workspace === "admin") {
            api.clearToken();
            adminApi.setToken(token);
          } else {
            adminApi.clearToken();
            api.setToken(token);
          }
          // Sync to zustand store
          useAuthStore.getState().setSession(token, "", workspace, next);
          setUser(next);
          setWorkspace(workspace);
          setView("dashboard");
        }}
        onPublic={() => {
          setUser(null);
          setWorkspace("public");
          setView("public");
        }}
        apiOffline={apiOffline}
      />
    );
  }

  const navGroups: NavGroup<View>[] = [
    {
      label: "工作台",
      items: [
        { id: "dashboard", icon: Gauge, label: "总览" },
        { id: "keys", icon: KeyRound, label: "API 密钥" },
        { id: "billing", icon: CreditCard, label: "计费钱包" },
        { id: "usage", icon: ListChecks, label: "用量日志" },
        { id: "models", icon: Database, label: "模型与价格" },
      ],
    },
    {
      label: "接入",
      items: [
        { id: "oauth", icon: Link, label: "账号接入" },
        { id: "playground", icon: PlayCircle, label: "Playground" },
        { id: "docs", icon: BookOpen, label: "文档" },
      ],
    },
    {
      label: "账户",
      items: [
        { id: "security", icon: ShieldCheck, label: "个人设置" },
      ],
    },
  ];
  const meta = viewDetails[view];

  return (
    <ProductShell
      title={meta.title}
      description={meta.description}
      crumb={meta.title}
      brandLabel="Elucid Relay"
      brandSubtitle="用户门户"
      navGroups={navGroups}
      activeId={view}
      onSelect={setView}
      statusLabel="服务正常"
      footer={(
        <>
          <div className="user-chip">
            <span>{initials(user.email)}</span>
            <div>
              <strong>{user.email}</strong>
              <small>{roleLabel(user.user_type)}</small>
            </div>
          </div>
          <button className="ghost" onClick={logout}>
            <LogOut size={18} /> 退出登录
          </button>
        </>
      )}
    >
      {view === "dashboard" && <Dashboard user={user} onNavigate={setView} />}
      {view === "billing" && <BillingView />}
      {view === "keys" && <KeysPage />}
      {view === "oauth" && <OAuthView />}
      {view === "security" && <SecurityView user={user} />}
      {view === "usage" && <UsageView />}
      {view === "models" && <ModelsView />}
      {view === "playground" && <PlaygroundView onOpenKeys={() => setView("keys")} />}
      {view === "docs" && <DocsView />}
    </ProductShell>
  );
}

function initials(value: string) {
  return value.slice(0, 2).toUpperCase();
}

function roleLabel(userType: RelayUser["user_type"]) {
  if (userType === "platform_owner") return "平台所有者";
  if (userType === "operator") return "操作员";
  return "普通用户";
}
