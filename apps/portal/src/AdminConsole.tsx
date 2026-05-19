import { useEffect, useState } from "react";
import { BookOpen, Gauge, KeyRound, LogOut, WalletCards } from "lucide-react";
import type { RelayUser } from "@elucid-relay/contracts";
import { AdminSetup, AdminUnavailable, SetupOffline } from "./admin/AdminAccessScreens";
import { OverviewDashboard } from "./admin/OverviewDashboard";
import {
  AdminAudit,
  AdminBilling,
  AdminContent,
  AdminControls,
  AdminGroups,
  AdminModels,
  AdminOAuth,
  AdminOps,
  AdminPool,
  AdminProxies,
  AdminRedeem,
  AdminRiskControls,
  AdminUpstream,
  AdminUsage,
  AdminUsers,
} from "./admin/features/AdminFeatureViews";
import { adminDetails, adminNavGroups, adminRoleLabel, adminSelfView, operatorViews, selfAccountViews } from "./admin/navigation";
import type { AdminTarget, AdminView } from "./admin/types";
import { publicRequest } from "./api";
import { ProductShell } from "./components/ProductShell";
import { BillingView, Dashboard, DocsView, KeysView, ModelsView, OAuthView, PlaygroundView, SecurityView, UsageView } from "./portal/PortalViews";

export function AdminConsole({ user, onAuthed, onLogout }: { user: RelayUser | null; onAuthed: (user: RelayUser) => void; onLogout: () => void }) {
  const [target, setTarget] = useState<AdminTarget>({ view: "overview" });
  const [setupInitialized, setSetupInitialized] = useState<boolean | null>(null);

  useEffect(() => {
    void publicRequest<{ initialized: boolean }>("/api/setup/status").then((status) => setSetupInitialized(status.initialized)).catch(() => setSetupInitialized(null));
  }, []);

  const role = user?.user_type === "platform_owner" ? "platform_owner" : "operator";
  const allowedViews = role === "platform_owner" ? null : new Set<AdminView>([...operatorViews, ...selfAccountViews]);
  const navGroups = adminNavGroups(role);
  const view = target.view;

  useEffect(() => {
    if (allowedViews && !allowedViews.has(view)) setTarget({ view: "overview" });
  }, [allowedViews, view]);

  if (setupInitialized === null) return <SetupOffline />;
  if (setupInitialized === false && !user) return <AdminSetup onAuthed={(nextUser) => { setSetupInitialized(true); onAuthed(nextUser); }} />;
  if (!user) return <AdminUnavailable />;
  if (allowedViews && !allowedViews.has(view)) return null;

  const meta = adminDetails[view];

  return (
    <ProductShell
      title={meta.title}
      description={meta.description}
      crumb={meta.title}
      brandLabel="Elucid Relay"
      brandSubtitle="管理后台"
      navGroups={navGroups}
      activeId={view}
      onSelect={(nextView) => setTarget({ view: nextView })}
      statusLabel={`控制平面 · ${runtimeStatusLabel(meta.status ?? "operational")}`}
      statusTone={meta.status === "degraded" ? "warning" : "ok"}
      contentVariant="wide"
      footer={(
        <>
          <div className="user-chip">
            <span>{initials(user.email)}</span>
            <div>
              <strong>{user.email}</strong>
              <small>{adminRoleLabel(role)}</small>
            </div>
          </div>
          <div className="admin-self-actions" aria-label="我的账户快捷入口">
            <button className={view === "my_dashboard" ? "active" : ""} onClick={() => setTarget({ view: "my_dashboard" })}><Gauge size={14} /> 总览</button>
            <button className={view === "my_keys" ? "active" : ""} onClick={() => setTarget({ view: "my_keys" })}><KeyRound size={14} /> 密钥</button>
            <button className={view === "my_billing" ? "active" : ""} onClick={() => setTarget({ view: "my_billing" })}><WalletCards size={14} /> 钱包</button>
            <button className={view === "my_docs" ? "active" : ""} onClick={() => setTarget({ view: "my_docs" })}><BookOpen size={14} /> 文档</button>
          </div>
          <button className="ghost" onClick={onLogout}><LogOut size={18} /> 退出登录</button>
        </>
      )}
    >
      {view === "overview" && <OverviewDashboard onNavigate={setTarget} />}
      {view === "ops" && <AdminOps role={role} onNavigate={setTarget} />}
      {view === "users" && <AdminUsers role={role} />}
      {view === "redeem" && <AdminRedeem />}
      {view === "models" && <AdminModels />}
      {view === "pool" && <AdminPool requestedTab={target.tab} />}
      {view === "upstream" && <AdminUpstream requestedTab={target.tab} />}
      {view === "proxies" && <AdminProxies />}
      {view === "oauth" && <AdminOAuth />}
      {view === "billing" && <AdminBilling requestedTab={target.tab} />}
      {view === "controls" && <AdminControls />}
      {view === "usage" && <AdminUsage role={role} />}
      {view === "content" && <AdminContent />}
      {view === "groups" && <AdminGroups />}
      {view === "risk" && <AdminRiskControls />}
      {view === "audit" && <AdminAudit />}
      {view === "my_dashboard" && <Dashboard user={user} onNavigate={(nextView) => setTarget({ view: adminSelfView(nextView) })} />}
      {view === "my_keys" && <KeysView />}
      {view === "my_billing" && <BillingView />}
      {view === "my_usage" && <UsageView />}
      {view === "my_models" && <ModelsView />}
      {view === "my_oauth" && <OAuthView />}
      {view === "my_playground" && <PlaygroundView onOpenKeys={() => setTarget({ view: "my_keys" })} />}
      {view === "my_docs" && <DocsView />}
      {view === "my_security" && <SecurityView user={user} />}
    </ProductShell>
  );
}

function initials(value: string) {
  return value.slice(0, 2).toUpperCase();
}

function runtimeStatusLabel(status: "operational" | "degraded") {
  return status === "degraded" ? "降级" : "正常";
}
