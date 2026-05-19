import { useEffect, useState, type ReactNode } from "react";
import { Bell, Database, Link, Network, RefreshCcw, ShieldCheck, Ticket } from "lucide-react";
import { BillingCommandCenter } from "../BillingCommandCenter";
import { PaymentSettingsPanel } from "../PaymentSettingsPanel";
import type { AdminRole, AdminTarget, BillingTab, PaymentMethodRoute, PaymentProvider, PaymentSettings, PoolTab, UpstreamTab } from "../types";
import { adminApi } from "../../adminApi";
import { API_BASE } from "../../api";
import { DataTable, Metric } from "../../components/DataTable";
import { ActionModal, PageNotice, SectionHeader, SectionTabs } from "../../components/Primitives";

const providerTypes = [
  "openai_compatible",
  "openai",
  "anthropic_compatible",
  "anthropic",
  "github_copilot",
  "gemini",
  "gemini_openai_compatible",
  "gemini_cli",
  "antigravity",
  "kiro",
  "windsurf_codeium",
  "codex_compatible",
  "cli_openai_compatible",
  "claude_compatible",
  "gemini_compatible",
];

const poolAdvancedTabs = ["cockpit", "quota", "health", "quality", "events", "wakeup", "route", "platforms"] as const;
type PoolAdvancedTab = typeof poolAdvancedTabs[number];
type PoolPrimaryTab = "overview" | "accounts" | "groups" | "import" | "advanced";

function isPoolAdvancedTab(tab: PoolTab): tab is PoolAdvancedTab {
  return (poolAdvancedTabs as readonly PoolTab[]).includes(tab);
}

const paramOperationOptions = [
  { value: "set", label: "设置字段" },
  { value: "delete", label: "删除字段" },
  { value: "copy", label: "复制字段" },
  { value: "move", label: "移动字段" },
  { value: "append", label: "追加内容" },
  { value: "prepend", label: "前置内容" },
  { value: "replace", label: "替换内容" },
  { value: "regex_replace", label: "正则替换" },
  { value: "set_header", label: "设置请求头" },
  { value: "delete_header", label: "删除请求头" },
  { value: "copy_header", label: "复制请求头" },
  { value: "move_header", label: "移动请求头" },
  { value: "pass_headers", label: "透传请求头" },
];

function FormField({ label, hint, wide, children }: { label: string; hint?: string; wide?: boolean; children: ReactNode }) {
  return (
    <label className={`form-field${wide ? " wide" : ""}`}>
      <span>{label}</span>
      {children}
      {hint && <small>{hint}</small>}
    </label>
  );
}

export function AdminUsers({ role }: { role: AdminRole }) {
  type UserTab = "directory" | "wallet" | "keys" | "ledger";
  const canMutateUsers = role === "platform_owner";
  const [users, setUsers] = useState<any[]>([]);
  const [selected, setSelected] = useState("");
  const [tab, setTab] = useState<UserTab>("directory");
  const [amount, setAmount] = useState("10.00");
  const [entryType, setEntryType] = useState("credit");
  const [wallet, setWallet] = useState<any>(null);
  const [ledger, setLedger] = useState<any[]>([]);
  const [keys, setKeys] = useState<any[]>([]);
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [editDisplayName, setEditDisplayName] = useState("");
  const [editUserType, setEditUserType] = useState("personal_user");
  const [editStatus, setEditStatus] = useState("active");
  const [createEmail, setCreateEmail] = useState("");
  const [createDisplayName, setCreateDisplayName] = useState("");
  const [createPassword, setCreatePassword] = useState("");
  const [createUserType, setCreateUserType] = useState("personal_user");
  const [createStatus, setCreateStatus] = useState("active");
  const [temporaryPassword, setTemporaryPassword] = useState("");
  const [resetToken, setResetToken] = useState("");
  const [userModal, setUserModal] = useState<null | "create" | "profile" | "wallet_adjust">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const params = new URLSearchParams({ limit: "200" });
    if (status) params.set("status", status);
    if (query) params.set("q", query);
    setUsers(await adminApi.request<any[]>(`/api/admin/v1/users?${params.toString()}`));
    if (selected) {
      const detail = await adminApi.request<any>(`/api/admin/v1/users/${selected}`);
      const [nextLedger, nextKeys] = await Promise.all([
        detail.wallet ? adminApi.request<any[]>(`/api/admin/v1/users/${selected}/wallet/ledger?limit=50`) : Promise.resolve([]),
        adminApi.request<any[]>(`/api/admin/v1/users/${selected}/api-keys?limit=50`),
      ]);
      setWallet(detail.wallet ?? null);
      setLedger(nextLedger);
      setKeys(nextKeys);
    } else {
      setWallet(null);
      setLedger([]);
      setKeys([]);
    }
  }

  useEffect(() => { void load(); }, [selected]);

  function prepareUser(row: any) {
    setSelected(row.id);
    setEditDisplayName(row.display_name ?? "");
    setEditUserType(row.user_type ?? "personal_user");
    setEditStatus(row.status ?? "active");
    setResetToken("");
    setMessage("");
    setError("");
  }

  function selectUser(row: any) {
    prepareUser(row);
    setUserModal("profile");
  }

  async function adjust() {
    setError("");
    setMessage("");
    try {
      await adminApi.request(`/api/admin/v1/users/${selected}/wallet/adjustments`, { method: "POST", body: JSON.stringify({ entry_type: entryType, amount, reason: "admin" }) });
      setMessage("钱包已调整");
      await load();
      setUserModal(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function patchUser(id: string, body: Record<string, unknown>) {
    setError("");
    setMessage("");
    try {
      await adminApi.request(`/api/admin/v1/users/${id}`, { method: "PATCH", body: JSON.stringify(body) });
      setMessage("用户已更新");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function saveSelectedUser() {
    if (!selected) return;
    await patchUser(selected, { display_name: editDisplayName, user_type: editUserType, status: editStatus });
    setUserModal(null);
  }

  async function issuePasswordReset() {
    if (!selected) return;
    setError("");
    setMessage("");
    try {
      const data = await adminApi.request<any>(`/api/admin/v1/users/${selected}/password-reset`, { method: "POST" });
      setResetToken(data.reset_token ?? "");
      setMessage("重置令牌已生成");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function createUser() {
    setError("");
    setMessage("");
    setTemporaryPassword("");
    try {
      const data = await adminApi.request<any>("/api/admin/v1/users", {
        method: "POST",
        body: JSON.stringify({ email: createEmail, display_name: createDisplayName, password: createPassword, user_type: createUserType, status: createStatus }),
      });
      setTemporaryPassword(data.temporary_password ?? "");
      setMessage("用户已创建");
      setCreateEmail("");
      setCreateDisplayName("");
      setCreatePassword("");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  return (
    <div className="stack">
      <PageNotice error={error} message={message} />
      <SectionTabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "directory", label: "用户列表", count: users.length },
          { id: "wallet", label: "钱包" },
          { id: "keys", label: "API 密钥", count: keys.length },
          { id: "ledger", label: "钱包流水", count: ledger.length },
        ]}
      />
      {tab === "directory" && (
        <section className="panel">
          <SectionHeader title="用户列表" description="搜索用户、切换状态和进入用户明细。" action={canMutateUsers ? <button onClick={() => setUserModal("create")}>创建用户</button> : undefined} />
          <div className="row filters">
            <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索" />
            <select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">任意状态</option><option value="active">启用</option><option value="disabled">停用</option><option value="pending">待处理</option></select>
            <button onClick={load}>筛选</button>
          </div>
          <DataTable rows={users} columns={["email", "display_name", "user_type", "status", "api_key_count", "total_usage_cost", "created_at"]} action={(row) => <div className="actions"><button onClick={() => selectUser(row)}>资料</button>{canMutateUsers && <button onClick={() => patchUser(row.id, { status: row.status === "active" ? "disabled" : "active" })}>{row.status === "active" ? "停用" : "启用"}</button>}{canMutateUsers && row.user_type === "personal_user" && <button onClick={() => patchUser(row.id, { user_type: "operator" })}>设为操作员</button>}{canMutateUsers && row.user_type === "operator" && <button onClick={() => patchUser(row.id, { user_type: "personal_user" })}>设为普通用户</button>}</div>} />
        </section>
      )}
      {tab === "wallet" && (
        <section className="panel">
          <SectionHeader title="钱包" description="查看选中用户钱包余额。" action={canMutateUsers ? <button onClick={() => setUserModal("wallet_adjust")} disabled={!selected}>调整钱包</button> : undefined} />
          <div className="form-grid single">
            <select value={selected} onChange={(event) => setSelected(event.target.value)}><option value="">选择用户</option>{users.map((user) => <option key={user.id} value={user.id}>{user.email}</option>)}</select>
          </div>
          <DataTable rows={wallet ? [wallet] : []} columns={["balance", "reserved_balance", "currency", "status", "updated_at"]} />
        </section>
      )}
      {tab === "keys" && (
        <section className="panel">
          <SectionHeader title="用户 API 密钥" description="当前选中用户的密钥状态和模型范围。" />
          <DataTable rows={keys} columns={["name", "display_prefix", "status", "model_scope", "ip_allowlist", "last_used_at"]} />
        </section>
      )}
      {tab === "ledger" && (
        <section className="panel">
          <SectionHeader title="钱包流水" description="当前选中用户的最近 50 条钱包流水。" />
          <DataTable rows={ledger} columns={["entry_type", "amount", "balance_after", "reserved_after", "reference_type", "created_at"]} />
        </section>
      )}
      <ActionModal
        open={userModal === "create"}
        title="创建用户"
        description="留空密码时系统生成一次性临时密码。"
        size="md"
        onClose={() => setUserModal(null)}
        footer={<><button onClick={() => setUserModal(null)}>关闭</button><button className="primary" onClick={createUser} disabled={!createEmail}>创建</button></>}
      >
        <div className="form-grid">
          <input value={createEmail} onChange={(event) => setCreateEmail(event.target.value)} placeholder="邮箱" autoComplete="email" />
          <input value={createDisplayName} onChange={(event) => setCreateDisplayName(event.target.value)} placeholder="显示名称" />
          <input value={createPassword} onChange={(event) => setCreatePassword(event.target.value)} placeholder="初始密码，留空自动生成" type="password" autoComplete="new-password" />
          <select value={createUserType} onChange={(event) => setCreateUserType(event.target.value)}><option value="personal_user">普通用户</option><option value="operator">操作员</option></select>
          <select value={createStatus} onChange={(event) => setCreateStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option><option value="pending">待处理</option></select>
        </div>
        {temporaryPassword && <pre>{temporaryPassword}</pre>}
      </ActionModal>
      <ActionModal
        open={userModal === "profile"}
        title="用户资料"
        size="md"
        onClose={() => setUserModal(null)}
        footer={<><button onClick={() => setUserModal(null)}>关闭</button>{canMutateUsers && <button className="primary" onClick={saveSelectedUser} disabled={!selected}>保存用户</button>}</>}
      >
        <div className="form-grid">
          <select value={selected} onChange={(event) => { const row = users.find((item) => item.id === event.target.value); if (row) prepareUser(row); else setSelected(""); }}><option value="">选择用户</option>{users.map((user) => <option key={user.id} value={user.id}>{user.email}</option>)}</select>
          <input value={editDisplayName} onChange={(event) => setEditDisplayName(event.target.value)} placeholder="显示名称" disabled={!selected} />
          <select value={editUserType} onChange={(event) => setEditUserType(event.target.value)} disabled={!selected || !canMutateUsers}><option value="personal_user">普通用户</option><option value="operator">操作员</option><option value="platform_owner">平台所有者</option></select>
          <select value={editStatus} onChange={(event) => setEditStatus(event.target.value)} disabled={!selected || !canMutateUsers}><option value="active">启用</option><option value="disabled">停用</option><option value="pending">待处理</option></select>
          <button onClick={issuePasswordReset} disabled={!selected}>生成重置令牌</button>
        </div>
        {resetToken && <pre>{resetToken}</pre>}
      </ActionModal>
      <ActionModal
        open={userModal === "wallet_adjust"}
        title="钱包调整"
        size="sm"
        onClose={() => setUserModal(null)}
        footer={<><button onClick={() => setUserModal(null)}>取消</button><button className="primary" onClick={adjust} disabled={!selected}>应用</button></>}
      >
        <div className="form-grid single">
          <select value={selected} onChange={(event) => setSelected(event.target.value)}><option value="">选择用户</option>{users.map((user) => <option key={user.id} value={user.id}>{user.email}</option>)}</select>
          <select value={entryType} onChange={(event) => setEntryType(event.target.value)}><option value="credit">入账</option><option value="debit">扣款</option><option value="adjustment">调整</option></select>
          <input value={amount} onChange={(event) => setAmount(event.target.value)} />
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminRedeem() {
  const [codes, setCodes] = useState<any[]>([]);
  const [redeemModal, setRedeemModal] = useState<null | "create" | "edit" | "claims">(null);
  const [grantValue, setGrantValue] = useState("5.00");
  const [count, setCount] = useState("1");
  const [maxClaims, setMaxClaims] = useState("1");
  const [expiresAt, setExpiresAt] = useState("");
  const [status, setStatus] = useState("");
  const [created, setCreated] = useState<any[]>([]);
  const [selectedCode, setSelectedCode] = useState("");
  const [claims, setClaims] = useState<any[]>([]);
  const [editCodeId, setEditCodeId] = useState("");
  const [editCodeStatus, setEditCodeStatus] = useState("active");
  const [editCodeMaxClaims, setEditCodeMaxClaims] = useState("1");
  const [editCodeExpiresAt, setEditCodeExpiresAt] = useState("");

  async function load() {
    const params = new URLSearchParams({ limit: "100" });
    if (status) params.set("status", status);
    setCodes(await adminApi.request<any[]>(`/api/admin/v1/redeem-codes?${params.toString()}`));
  }

  useEffect(() => { void load(); }, []);

  async function create() {
    const data = await adminApi.request<{ codes: any[] }>("/api/admin/v1/redeem-codes", { method: "POST", body: JSON.stringify({ grant_value: grantValue, count: Number(count), max_claims: Number(maxClaims), expires_at: expiresAt }) });
    setCreated(data.codes);
    await load();
  }

  async function patchCode(id: string, body: Record<string, unknown>) {
    await adminApi.request(`/api/admin/v1/redeem-codes/${id}`, { method: "PATCH", body: JSON.stringify(body) });
    await load();
  }

  async function saveCode() {
    await patchCode(editCodeId, { status: editCodeStatus, max_claims: Number(editCodeMaxClaims), expires_at: editCodeExpiresAt });
    setRedeemModal(null);
  }

  async function loadClaims(id: string) {
    setSelectedCode(id);
    setClaims(id ? await adminApi.request<any[]>(`/api/admin/v1/redeem-codes/${id}/claims`) : []);
    setRedeemModal("claims");
  }

  function editCode(row: any) {
    setEditCodeId(row.id);
    setEditCodeStatus(row.status);
    setEditCodeMaxClaims(String(row.max_claims ?? 1));
    setEditCodeExpiresAt(row.expires_at ?? "");
    setRedeemModal("edit");
  }

  return (
    <div className="stack">
      <section className="panel">
        <SectionHeader
          title="兑换码"
          description="筛选、查看和停用兑换码。生成、编辑和领取记录在弹窗中处理。"
          action={<><button onClick={() => setRedeemModal("create")}><Ticket size={15} /> 新建兑换码</button><button onClick={load}>刷新</button></>}
        />
        <div className="row filters">
          <select value={status} onChange={(event) => setStatus(event.target.value)}>
            <option value="">任意状态</option>
            <option value="active">启用</option>
            <option value="disabled">停用</option>
            <option value="expired">已过期</option>
          </select>
          <button onClick={load}>筛选</button>
        </div>
        <DataTable
          rows={codes}
          columns={["display_prefix", "grant_value", "max_claims", "claim_count", "expires_at", "status", "created_at"]}
          action={(row) => (
            <div className="actions">
              <button onClick={() => loadClaims(row.id)}>领取记录</button>
              <button onClick={() => editCode(row)}>编辑</button>
              <button onClick={() => patchCode(row.id, { status: row.status === "active" ? "disabled" : "active" })}>{row.status === "active" ? "停用" : "启用"}</button>
            </div>
          )}
        />
      </section>
      <ActionModal
        open={redeemModal === "create"}
        title="新建兑换码"
        size="md"
        onClose={() => setRedeemModal(null)}
        footer={<><button onClick={() => setRedeemModal(null)}>关闭</button><button className="primary" onClick={create}>生成</button></>}
      >
        <div className="form-grid">
          <input value={grantValue} onChange={(event) => setGrantValue(event.target.value)} placeholder="赠送 USD" />
          <input value={count} onChange={(event) => setCount(event.target.value)} placeholder="数量" />
          <input value={maxClaims} onChange={(event) => setMaxClaims(event.target.value)} placeholder="最大领取次数" />
          <input value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} placeholder="过期时间 RFC3339" />
        </div>
        {created.length > 0 && <pre>{created.map((item) => item.code).join("\n")}</pre>}
      </ActionModal>
      <ActionModal
        open={redeemModal === "edit"}
        title="编辑兑换码"
        size="md"
        onClose={() => setRedeemModal(null)}
        footer={<><button onClick={() => setRedeemModal(null)}>取消</button><button className="primary" onClick={saveCode} disabled={!editCodeId}>保存</button></>}
      >
        <div className="form-grid">
          <input value={editCodeId} onChange={(event) => setEditCodeId(event.target.value)} placeholder="兑换码 ID" />
          <select value={editCodeStatus} onChange={(event) => setEditCodeStatus(event.target.value)}>
            <option value="active">启用</option>
            <option value="disabled">停用</option>
            <option value="expired">已过期</option>
          </select>
          <input value={editCodeMaxClaims} onChange={(event) => setEditCodeMaxClaims(event.target.value)} placeholder="最大领取次数" />
          <input value={editCodeExpiresAt} onChange={(event) => setEditCodeExpiresAt(event.target.value)} placeholder="过期时间 RFC3339，留空清除" />
        </div>
      </ActionModal>
      <ActionModal
        open={redeemModal === "claims"}
        title="领取记录"
        size="lg"
        onClose={() => setRedeemModal(null)}
        footer={<button onClick={() => setRedeemModal(null)}>关闭</button>}
      >
        {selectedCode && <p className="subtle">{selectedCode}</p>}
        <DataTable rows={claims} columns={["email", "wallet_ledger_id", "claimed_at"]} />
      </ActionModal>
    </div>
  );
}

export function AdminModels() {
  const [models, setModels] = useState<any[]>([]);
  const [conflicts, setConflicts] = useState<any[]>([]);
  const [missingModels, setMissingModels] = useState<any[]>([]);
  const [modelName, setModelName] = useState("");
  const [capabilities, setCapabilities] = useState("chat,responses,embeddings");
  const [aliases, setAliases] = useState("");
  const [inputPrice, setInputPrice] = useState("0");
  const [outputPrice, setOutputPrice] = useState("0");
  const [requestPrice, setRequestPrice] = useState("0");
  const [minCharge, setMinCharge] = useState("0");
  const [billingMode, setBillingMode] = useState("standard");
  const [billingExpr, setBillingExpr] = useState("");
  const [cacheReadPrice, setCacheReadPrice] = useState("0");
  const [cacheWritePrice, setCacheWritePrice] = useState("0");
  const [imageUnitPrice, setImageUnitPrice] = useState("0");
  const [audioSecondPrice, setAudioSecondPrice] = useState("0");
  const [modelDescription, setModelDescription] = useState("");
  const [modelIcon, setModelIcon] = useState("");
  const [modelTags, setModelTags] = useState("");
  const [modelVendor, setModelVendor] = useState("");
  const [modelPricingVersion, setModelPricingVersion] = useState("");
  const [modelSupportedEndpoints, setModelSupportedEndpoints] = useState("");
  const [modelMetadata, setModelMetadata] = useState("{}");
  const [editModelName, setEditModelName] = useState("");
  const [editDisplayName, setEditDisplayName] = useState("");
  const [editProviderHint, setEditProviderHint] = useState("");
  const [editCapabilities, setEditCapabilities] = useState("");
  const [editAliases, setEditAliases] = useState("");
  const [editInputPrice, setEditInputPrice] = useState("0");
  const [editOutputPrice, setEditOutputPrice] = useState("0");
  const [editRequestPrice, setEditRequestPrice] = useState("0");
  const [editMinCharge, setEditMinCharge] = useState("0");
  const [editBillingMode, setEditBillingMode] = useState("standard");
  const [editBillingExpr, setEditBillingExpr] = useState("");
  const [editCacheReadPrice, setEditCacheReadPrice] = useState("0");
  const [editCacheWritePrice, setEditCacheWritePrice] = useState("0");
  const [editImageUnitPrice, setEditImageUnitPrice] = useState("0");
  const [editAudioSecondPrice, setEditAudioSecondPrice] = useState("0");
  const [editModelDescription, setEditModelDescription] = useState("");
  const [editModelIcon, setEditModelIcon] = useState("");
  const [editModelTags, setEditModelTags] = useState("");
  const [editModelVendor, setEditModelVendor] = useState("");
  const [editModelPricingVersion, setEditModelPricingVersion] = useState("");
  const [editModelSupportedEndpoints, setEditModelSupportedEndpoints] = useState("");
  const [editPublicVisible, setEditPublicVisible] = useState(true);
  const [editStatus, setEditStatus] = useState("active");
  const [editModelMetadata, setEditModelMetadata] = useState("{}");
  const [batchNames, setBatchNames] = useState("");
  const [batchStatus, setBatchStatus] = useState("");
  const [batchVisible, setBatchVisible] = useState("");
  const [modelModal, setModelModal] = useState<null | "create" | "edit" | "batch">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const [nextModels, nextConflicts, nextMissingModels] = await Promise.all([
      adminApi.request<any[]>("/api/admin/v1/models"),
      adminApi.request<any[]>("/api/admin/v1/models/conflicts"),
      adminApi.request<any[]>("/api/admin/v1/models/missing"),
    ]);
    setModels(nextModels);
    setConflicts(nextConflicts);
    setMissingModels(nextMissingModels);
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  async function runAction(action: () => Promise<void>, success = "") {
    setError("");
    setMessage("");
    try {
      await action();
      if (success) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function create() {
    await adminApi.request("/api/admin/v1/models", {
      method: "POST",
      body: JSON.stringify({
        model_name: modelName,
        display_name: modelName,
        aliases: splitCSV(aliases),
        endpoint_capabilities: splitCSV(capabilities),
        input_usd_per_1k: inputPrice,
        output_usd_per_1k: outputPrice,
        request_usd: requestPrice,
        min_charge_usd: minCharge,
        billing_mode: billingMode,
        billing_expr: billingExpr,
        cache_read_usd_per_1k: cacheReadPrice,
        cache_write_usd_per_1k: cacheWritePrice,
        image_usd_per_unit: imageUnitPrice,
        audio_usd_per_second: audioSecondPrice,
        public_visible: true,
        metadata: mergeModelMetadataFields(parseJSONObject(modelMetadata), {
          description: modelDescription,
          icon: modelIcon,
          tags: splitCSV(modelTags),
          vendor: modelVendor,
          pricing_version: modelPricingVersion,
          supported_endpoint_types: splitCSV(modelSupportedEndpoints),
        }),
      }),
    });
    setModelName("");
    setAliases("");
    setModelDescription("");
    setModelIcon("");
    setModelTags("");
    setModelVendor("");
    setModelPricingVersion("");
    setModelSupportedEndpoints("");
    setModelMetadata("{}");
    await load();
    setModelModal(null);
  }

  async function patchModel(row: any, nextStatus = row.status) {
    await adminApi.request(`/api/admin/v1/models/${encodeURIComponent(row.model_name)}`, { method: "PATCH", body: JSON.stringify({ aliases: row.aliases ?? [], endpoint_capabilities: row.endpoint_capabilities ?? [], input_usd_per_1k: row.pricing?.input_usd_per_1k ?? "0", output_usd_per_1k: row.pricing?.output_usd_per_1k ?? "0", request_usd: row.pricing?.request_usd ?? "0", min_charge_usd: row.pricing?.min_charge_usd ?? "0", billing_mode: row.pricing?.billing_mode ?? "standard", billing_expr: row.pricing?.billing_expr ?? "", cache_read_usd_per_1k: row.pricing?.cache_read_usd_per_1k ?? "0", cache_write_usd_per_1k: row.pricing?.cache_write_usd_per_1k ?? "0", image_usd_per_unit: row.pricing?.image_usd_per_unit ?? "0", audio_usd_per_second: row.pricing?.audio_usd_per_second ?? "0", public_visible: row.public_visible, status: nextStatus }) });
    await load();
  }

  function selectModel(row: any) {
    setEditModelName(row.model_name ?? "");
    setEditDisplayName(row.display_name ?? row.model_name ?? "");
    setEditProviderHint(row.provider_hint ?? "");
    setEditCapabilities((row.endpoint_capabilities ?? []).join(","));
    setEditAliases((row.aliases ?? []).join(","));
    setEditInputPrice(row.pricing?.input_usd_per_1k ?? "0");
    setEditOutputPrice(row.pricing?.output_usd_per_1k ?? "0");
    setEditRequestPrice(row.pricing?.request_usd ?? "0");
    setEditMinCharge(row.pricing?.min_charge_usd ?? "0");
    setEditBillingMode(row.pricing?.billing_mode ?? "standard");
    setEditBillingExpr(row.pricing?.billing_expr ?? "");
    setEditCacheReadPrice(row.pricing?.cache_read_usd_per_1k ?? "0");
    setEditCacheWritePrice(row.pricing?.cache_write_usd_per_1k ?? "0");
    setEditImageUnitPrice(row.pricing?.image_usd_per_unit ?? "0");
    setEditAudioSecondPrice(row.pricing?.audio_usd_per_second ?? "0");
    setEditModelDescription(row.description ?? metadataStringField(row.metadata, "description"));
    setEditModelIcon(row.icon ?? metadataStringField(row.metadata, "icon"));
    setEditModelTags((row.tags ?? metadataStringArrayField(row.metadata, "tags")).join(","));
    setEditModelVendor(row.vendor ?? metadataStringField(row.metadata, "vendor"));
    setEditModelPricingVersion(row.pricing_version ?? metadataStringField(row.metadata, "pricing_version"));
    setEditModelSupportedEndpoints((row.supported_endpoint_types ?? metadataStringArrayField(row.metadata, "supported_endpoint_types")).join(","));
    setEditPublicVisible(row.public_visible !== false);
    setEditStatus(row.status ?? "active");
    setEditModelMetadata(jsonText(row.metadata));
    setModelModal("edit");
  }

  async function saveModel() {
    await adminApi.request(`/api/admin/v1/models/${encodeURIComponent(editModelName)}`, {
      method: "PATCH",
      body: JSON.stringify({
        display_name: editDisplayName,
        provider_hint: editProviderHint,
        aliases: splitCSV(editAliases),
        endpoint_capabilities: splitCSV(editCapabilities),
        input_usd_per_1k: editInputPrice,
        output_usd_per_1k: editOutputPrice,
        request_usd: editRequestPrice,
        min_charge_usd: editMinCharge,
        billing_mode: editBillingMode,
        billing_expr: editBillingExpr,
        cache_read_usd_per_1k: editCacheReadPrice,
        cache_write_usd_per_1k: editCacheWritePrice,
        image_usd_per_unit: editImageUnitPrice,
        audio_usd_per_second: editAudioSecondPrice,
        public_visible: editPublicVisible,
        status: editStatus,
        metadata: mergeModelMetadataFields(parseJSONObject(editModelMetadata), {
          description: editModelDescription,
          icon: editModelIcon,
          tags: splitCSV(editModelTags),
          vendor: editModelVendor,
          pricing_version: editModelPricingVersion,
          supported_endpoint_types: splitCSV(editModelSupportedEndpoints),
        }),
      }),
    });
    await load();
    setModelModal(null);
  }

  async function batchUpdate() {
    const body: Record<string, unknown> = { model_names: splitCSV(batchNames) };
    if (batchStatus) body.status = batchStatus;
    if (batchVisible) body.public_visible = batchVisible === "true";
    await adminApi.request("/api/admin/v1/models/batch", { method: "POST", body: JSON.stringify(body) });
    await load();
    setModelModal(null);
  }

  async function syncFromChannels() {
    const result = await adminApi.request<any>("/api/admin/v1/models/sync-from-channels", { method: "POST" });
    await load();
    setMessage(`已同步 ${result.created?.length ?? 0} 个模型，待入库候选已刷新`);
  }

  const activeModels = models.filter((row) => row.status === "active").length;
  const visibleModels = models.filter((row) => row.public_visible !== false).length;

  return (
    <div className="stack">
      <div className="grid three">
        <Metric label="模型总数" value={String(models.length)} />
        <Metric label="启用模型" value={String(activeModels)} />
        <Metric label="公开模型" value={String(visibleModels)} />
        <Metric label="冲突项" value={String(conflicts.length)} />
        <Metric label="待入库模型" value={String(missingModels.length)} />
      </div>
      {(error || message) && <section className="panel">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</section>}
      <section className="panel">
        <SectionHeader
          title="模型工具"
          action={<div className="actions"><button onClick={() => setModelModal("create")}><Database size={15} /> 新建模型</button><button onClick={() => setModelModal("batch")}>批量更新</button><button onClick={() => runAction(syncFromChannels)}>从通道同步模型</button><button onClick={() => runAction(load)}>刷新冲突检查</button></div>}
        />
        <DataTable rows={conflicts} columns={["conflict_type", "alias", "model_name", "detail"]} />
        <DataTable rows={missingModels} columns={["model_name", "endpoint_capabilities", "channel_count", "account_count", "providers"]} />
      </section>
      <section className="panel">
        <h2>模型</h2>
        <DataTable rows={models} columns={["model_name", "display_name", "provider_hint", "vendor", "description", "tags", "endpoint_capabilities", "supported_endpoint_types", "pricing_version", "pricing", "active_channel_count", "active_account_count", "providers", "health", "metadata", "public_visible", "status"]} action={(row) => <div className="actions"><button onClick={() => selectModel(row)}>编辑</button><button onClick={() => runAction(() => patchModel(row, row.status === "active" ? "disabled" : "active"), "状态已更新")}>{row.status === "active" ? "停用" : "启用"}</button></div>} />
      </section>
      <ActionModal
        open={modelModal === "create"}
        title="新建模型"
        size="xl"
        onClose={() => setModelModal(null)}
        footer={<><button onClick={() => setModelModal(null)}>取消</button><button className="primary" onClick={() => runAction(create, "模型已创建")} disabled={!modelName}>创建</button></>}
      >
        <div className="form-grid">
          <input value={modelName} onChange={(event) => setModelName(event.target.value)} placeholder="模型名称" />
          <input value={aliases} onChange={(event) => setAliases(event.target.value)} placeholder="别名" />
          <input value={capabilities} onChange={(event) => setCapabilities(event.target.value)} placeholder="能力，逗号分隔" />
          <input value={inputPrice} onChange={(event) => setInputPrice(event.target.value)} placeholder="输入价格/1K" />
          <input value={outputPrice} onChange={(event) => setOutputPrice(event.target.value)} placeholder="输出价格/1K" />
          <input value={requestPrice} onChange={(event) => setRequestPrice(event.target.value)} placeholder="单次请求价格" />
          <input value={minCharge} onChange={(event) => setMinCharge(event.target.value)} placeholder="最低计费" />
          <select value={billingMode} onChange={(event) => setBillingMode(event.target.value)}>
            <option value="standard">标准计费</option>
            <option value="tiered_expr">表达式计费</option>
          </select>
          <input value={billingExpr} onChange={(event) => setBillingExpr(event.target.value)} placeholder="计费表达式" />
          <input value={cacheReadPrice} onChange={(event) => setCacheReadPrice(event.target.value)} placeholder="缓存读价格/1K" />
          <input value={cacheWritePrice} onChange={(event) => setCacheWritePrice(event.target.value)} placeholder="缓存写价格/1K" />
          <input value={imageUnitPrice} onChange={(event) => setImageUnitPrice(event.target.value)} placeholder="图片单价" />
          <input value={audioSecondPrice} onChange={(event) => setAudioSecondPrice(event.target.value)} placeholder="音频每秒价格" />
          <input value={modelDescription} onChange={(event) => setModelDescription(event.target.value)} placeholder="模型描述" />
          <input value={modelIcon} onChange={(event) => setModelIcon(event.target.value)} placeholder="图标 URL 或标识" />
          <input value={modelTags} onChange={(event) => setModelTags(event.target.value)} placeholder="标签，逗号分隔" />
          <input value={modelVendor} onChange={(event) => setModelVendor(event.target.value)} placeholder="厂商" />
          <input value={modelPricingVersion} onChange={(event) => setModelPricingVersion(event.target.value)} placeholder="价格版本" />
          <input value={modelSupportedEndpoints} onChange={(event) => setModelSupportedEndpoints(event.target.value)} placeholder="支持端点，逗号分隔" />
          <textarea value={modelMetadata} onChange={(event) => setModelMetadata(event.target.value)} placeholder="模型元数据 JSON" rows={4} />
        </div>
      </ActionModal>
      <ActionModal
        open={modelModal === "edit"}
        title="编辑模型"
        size="xl"
        onClose={() => setModelModal(null)}
        footer={<><button onClick={() => setModelModal(null)}>取消</button><button className="primary" onClick={() => runAction(saveModel, "模型已保存")} disabled={!editModelName}>保存</button></>}
      >
        <div className="form-grid">
          <input value={editModelName} onChange={(event) => setEditModelName(event.target.value)} placeholder="模型名称" />
          <input value={editDisplayName} onChange={(event) => setEditDisplayName(event.target.value)} placeholder="显示名称" />
          <input value={editProviderHint} onChange={(event) => setEditProviderHint(event.target.value)} placeholder="供应商提示" />
          <input value={editAliases} onChange={(event) => setEditAliases(event.target.value)} placeholder="别名，逗号分隔" />
          <input value={editCapabilities} onChange={(event) => setEditCapabilities(event.target.value)} placeholder="能力，逗号分隔" />
          <input value={editInputPrice} onChange={(event) => setEditInputPrice(event.target.value)} placeholder="输入价格/1K" />
          <input value={editOutputPrice} onChange={(event) => setEditOutputPrice(event.target.value)} placeholder="输出价格/1K" />
          <input value={editRequestPrice} onChange={(event) => setEditRequestPrice(event.target.value)} placeholder="单次请求价格" />
          <input value={editMinCharge} onChange={(event) => setEditMinCharge(event.target.value)} placeholder="最低计费" />
          <select value={editBillingMode} onChange={(event) => setEditBillingMode(event.target.value)}>
            <option value="standard">标准计费</option>
            <option value="tiered_expr">表达式计费</option>
          </select>
          <input value={editBillingExpr} onChange={(event) => setEditBillingExpr(event.target.value)} placeholder="计费表达式" />
          <input value={editCacheReadPrice} onChange={(event) => setEditCacheReadPrice(event.target.value)} placeholder="缓存读价格/1K" />
          <input value={editCacheWritePrice} onChange={(event) => setEditCacheWritePrice(event.target.value)} placeholder="缓存写价格/1K" />
          <input value={editImageUnitPrice} onChange={(event) => setEditImageUnitPrice(event.target.value)} placeholder="图片单价" />
          <input value={editAudioSecondPrice} onChange={(event) => setEditAudioSecondPrice(event.target.value)} placeholder="音频每秒价格" />
          <input value={editModelDescription} onChange={(event) => setEditModelDescription(event.target.value)} placeholder="模型描述" />
          <input value={editModelIcon} onChange={(event) => setEditModelIcon(event.target.value)} placeholder="图标 URL 或标识" />
          <input value={editModelTags} onChange={(event) => setEditModelTags(event.target.value)} placeholder="标签，逗号分隔" />
          <input value={editModelVendor} onChange={(event) => setEditModelVendor(event.target.value)} placeholder="厂商" />
          <input value={editModelPricingVersion} onChange={(event) => setEditModelPricingVersion(event.target.value)} placeholder="价格版本" />
          <input value={editModelSupportedEndpoints} onChange={(event) => setEditModelSupportedEndpoints(event.target.value)} placeholder="支持端点，逗号分隔" />
          <select value={editPublicVisible ? "true" : "false"} onChange={(event) => setEditPublicVisible(event.target.value === "true")}><option value="true">公开</option><option value="false">隐藏</option></select>
          <select value={editStatus} onChange={(event) => setEditStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option></select>
          <textarea value={editModelMetadata} onChange={(event) => setEditModelMetadata(event.target.value)} placeholder="模型元数据 JSON" rows={4} />
        </div>
      </ActionModal>
      <ActionModal
        open={modelModal === "batch"}
        title="批量更新模型"
        size="md"
        onClose={() => setModelModal(null)}
        footer={<><button onClick={() => setModelModal(null)}>取消</button><button className="primary" onClick={() => runAction(batchUpdate, "批量更新完成")} disabled={!batchNames.trim()}>批量保存</button></>}
      >
        <div className="form-grid">
          <textarea value={batchNames} onChange={(event) => setBatchNames(event.target.value)} placeholder="模型名称，逗号分隔" rows={3} />
          <select value={batchStatus} onChange={(event) => setBatchStatus(event.target.value)}>
            <option value="">不改状态</option>
            <option value="active">启用</option>
            <option value="disabled">停用</option>
          </select>
          <select value={batchVisible} onChange={(event) => setBatchVisible(event.target.value)}>
            <option value="">不改公开状态</option>
            <option value="true">公开</option>
            <option value="false">隐藏</option>
          </select>
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminPool({ requestedTab }: { requestedTab?: string }) {
  const [tab, setTab] = useState<PoolTab>("overview");
  const [providers, setProviders] = useState<any[]>([]);
  const [channels, setChannels] = useState<any[]>([]);
  const [accounts, setAccounts] = useState<any[]>([]);
  const [proxies, setProxies] = useState<any[]>([]);
  const [quotaWindows, setQuotaWindows] = useState<any[]>([]);
  const [quotaSnapshots, setQuotaSnapshots] = useState<any[]>([]);
  const [quotaRefreshJobs, setQuotaRefreshJobs] = useState<any[]>([]);
  const [accountQuality, setAccountQuality] = useState<any[]>([]);
  const [strategyEvents, setStrategyEvents] = useState<any[]>([]);
  const [wakeupJobs, setWakeupJobs] = useState<any[]>([]);
  const [platformConfigs, setPlatformConfigs] = useState<any[]>([]);
  const [poolGroups, setPoolGroups] = useState<any[]>([]);
  const [importTemplates, setImportTemplates] = useState<any[]>([]);
  const [channelTests, setChannelTests] = useState<any[]>([]);
  const [users, setUsers] = useState<any[]>([]);
  const [models, setModels] = useState<any[]>([]);
  const [providerId, setProviderId] = useState("");
  const [channelId, setChannelId] = useState("");
  const [proxyId, setProxyId] = useState("");
  const [routingMode, setRoutingMode] = useState("pool");
  const [ownerUserId, setOwnerUserId] = useState("");
  const [authMode, setAuthMode] = useState("api_key");
  const [accountName, setAccountName] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [accountMetadata, setAccountMetadata] = useState("{}");
  const [accountPoolGroup, setAccountPoolGroup] = useState("");
  const [accountRouteTags, setAccountRouteTags] = useState("");
  const [priority, setPriority] = useState("100");
  const [maxConcurrency, setMaxConcurrency] = useState("10");
  const [quotaAccountId, setQuotaAccountId] = useState("");
  const [quotaType, setQuotaType] = useState("requests");
  const [quotaRemaining, setQuotaRemaining] = useState("100");
  const [quotaLimit, setQuotaLimit] = useState("100");
  const [quotaResetAt, setQuotaResetAt] = useState("");
  const [cockpitProvider, setCockpitProvider] = useState("");
  const [routeModel, setRouteModel] = useState("");
  const [routeEndpoint, setRouteEndpoint] = useState("chat");
  const [routeRoutingMode, setRouteRoutingMode] = useState("pool");
  const [routeOwnerUserId, setRouteOwnerUserId] = useState("");
  const [routeApiKeyId, setRouteApiKeyId] = useState("");
  const [routeAffinityKey, setRouteAffinityKey] = useState("");
  const [routeTags, setRouteTags] = useState("");
  const [route, setRoute] = useState<any>(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [editAccountId, setEditAccountId] = useState("");
  const [editAccountName, setEditAccountName] = useState("");
  const [editAccountStatus, setEditAccountStatus] = useState("active");
  const [editAccountRoutingMode, setEditAccountRoutingMode] = useState("pool");
  const [editAccountOwnerUserId, setEditAccountOwnerUserId] = useState("");
  const [editAccountChannelId, setEditAccountChannelId] = useState("");
  const [editAccountProxyId, setEditAccountProxyId] = useState("");
  const [editAccountPriority, setEditAccountPriority] = useState("100");
  const [editAccountMaxConcurrency, setEditAccountMaxConcurrency] = useState("10");
  const [editAccountAPIKey, setEditAccountAPIKey] = useState("");
  const [editAccountMetadata, setEditAccountMetadata] = useState("{}");
  const [editAccountPoolGroup, setEditAccountPoolGroup] = useState("");
  const [editAccountRouteTags, setEditAccountRouteTags] = useState("");
  const [accountQuery, setAccountQuery] = useState("");
  const [accountStatusFilter, setAccountStatusFilter] = useState("");
  const [accountProviderFilter, setAccountProviderFilter] = useState("");
  const [selectedAccountIds, setSelectedAccountIds] = useState<string[]>([]);
  const [batchGroup, setBatchGroup] = useState("");
  const [batchTags, setBatchTags] = useState("");
  const [batchMetadata, setBatchMetadata] = useState("{}");
  const [healthTargetPath, setHealthTargetPath] = useState("/models");
  const [healthModel, setHealthModel] = useState("");
  const [healthEndpoint, setHealthEndpoint] = useState("chat");
  const [qualityApplyActions, setQualityApplyActions] = useState(false);
  const [qualityIsolationThreshold, setQualityIsolationThreshold] = useState("40");
  const [qualityWatchThreshold, setQualityWatchThreshold] = useState("70");
  const [wakeupTargetPath, setWakeupTargetPath] = useState("/models");
  const [wakeupModel, setWakeupModel] = useState("");
  const [wakeupEndpoint, setWakeupEndpoint] = useState("chat");
  const [wakeupScheduledFor, setWakeupScheduledFor] = useState("");
  const [platformProviderType, setPlatformProviderType] = useState("");
  const [platformDisplayName, setPlatformDisplayName] = useState("");
  const [platformStatus, setPlatformStatus] = useState("active");
  const [platformHealthEnabled, setPlatformHealthEnabled] = useState(true);
  const [platformQuotaRefreshEnabled, setPlatformQuotaRefreshEnabled] = useState(true);
  const [platformWakeupEnabled, setPlatformWakeupEnabled] = useState(true);
  const [platformHealthInterval, setPlatformHealthInterval] = useState("300");
  const [platformQuotaRefreshInterval, setPlatformQuotaRefreshInterval] = useState("900");
  const [platformWakeupInterval, setPlatformWakeupInterval] = useState("300");
  const [platformQuotaThreshold, setPlatformQuotaThreshold] = useState("20");
  const [platformMaxFailures, setPlatformMaxFailures] = useState("5");
  const [platformMetadata, setPlatformMetadata] = useState("{}");
  const [importTemplate, setImportTemplate] = useState("generic");
  const [importProviderId, setImportProviderId] = useState("");
  const [importChannelId, setImportChannelId] = useState("");
  const [importPoolGroup, setImportPoolGroup] = useState("");
  const [importRouteTags, setImportRouteTags] = useState("");
  const [importDefaultMetadata, setImportDefaultMetadata] = useState("{}");
  const [importText, setImportText] = useState("");
  const [quickImportKeys, setQuickImportKeys] = useState("");
  const [quickImportNamePrefix, setQuickImportNamePrefix] = useState("");
  const [quickImportRoutingMode, setQuickImportRoutingMode] = useState("pool");
  const [quickImportOwnerUserId, setQuickImportOwnerUserId] = useState("");
  const [quickImportPriority, setQuickImportPriority] = useState("100");
  const [quickImportMaxConcurrency, setQuickImportMaxConcurrency] = useState("10");
  const [importPreview, setImportPreview] = useState<any>(null);
  const [exportText, setExportText] = useState("");
  const [poolModal, setPoolModal] = useState<null | "add" | "edit" | "batch" | "quick_import" | "advanced_import">(null);

  async function load() {
    const [nextProviders, nextChannels, nextAccounts, nextProxies, nextQuotaWindows, nextQuotaSnapshots, nextQuotaRefreshJobs, nextAccountQuality, nextStrategyEvents, nextWakeupJobs, nextPlatformConfigs, nextPoolGroups, nextImportTemplates, nextChannelTests, nextUsers, nextModels] = await Promise.all([
      adminApi.request<any[]>("/api/admin/v1/providers"),
      adminApi.request<any[]>("/api/admin/v1/channels"),
      adminApi.request<any[]>("/api/admin/v1/accounts"),
      adminApi.request<any[]>("/api/admin/v1/proxies"),
      adminApi.request<any[]>("/api/admin/v1/account-quota-windows"),
      adminApi.request<any[]>("/api/admin/v1/account-quota-snapshots?limit=80"),
      adminApi.request<any[]>("/api/admin/v1/account-quota-refresh-jobs?limit=40"),
      adminApi.request<any[]>("/api/admin/v1/account-quality?limit=200"),
      adminApi.request<any[]>("/api/admin/v1/account-pool-strategy-events?limit=80"),
      adminApi.request<any[]>("/api/admin/v1/account-wakeup-jobs?limit=80"),
      adminApi.request<any[]>("/api/admin/v1/account-platform-configs"),
      adminApi.request<any[]>("/api/admin/v1/account-pool-groups"),
      adminApi.request<any[]>("/api/admin/v1/account-import-templates"),
      adminApi.request<any[]>("/api/admin/v1/channel-tests?limit=80"),
      adminApi.request<any[]>("/api/admin/v1/users?limit=200"),
      adminApi.request<any[]>("/api/admin/v1/models"),
    ]);
    setProviders(nextProviders);
    setChannels(nextChannels);
    setAccounts(nextAccounts);
    setProxies(nextProxies);
    setQuotaWindows(nextQuotaWindows);
    setQuotaSnapshots(nextQuotaSnapshots);
    setQuotaRefreshJobs(nextQuotaRefreshJobs);
    setAccountQuality(nextAccountQuality);
    setStrategyEvents(nextStrategyEvents);
    setWakeupJobs(nextWakeupJobs);
    setPlatformConfigs(nextPlatformConfigs);
    setPoolGroups(nextPoolGroups);
    setImportTemplates(nextImportTemplates);
    setChannelTests(nextChannelTests);
    setUsers(nextUsers);
    setModels(nextModels);
  }

  useEffect(() => {
    void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"));
  }, []);

  useEffect(() => {
    if (!requestedTab || !["overview", "accounts", "groups", "cockpit", "batch", "add", "edit", "quota", "health", "quality", "events", "wakeup", "platforms", "import", "route"].includes(requestedTab)) return;
    if (["add", "edit", "batch"].includes(requestedTab)) {
      setTab("accounts");
      setPoolModal(requestedTab as "add" | "edit" | "batch");
      return;
    }
    setTab(requestedTab as PoolTab);
  }, [requestedTab]);

  const providerNames = new Map(providers.map((item) => [item.id, item.name]));
  const channelNames = new Map(channels.map((item) => [item.id, item.name]));
  const userNames = new Map(users.map((item) => [item.id, item.email]));
  const accountNames = new Map(accounts.map((item) => [item.id, item.name]));
  const poolRows = accounts.map((account) => {
    const runtime = account.runtime ?? {};
    const auth = account.auth ?? {};
    const metadata = account.metadata && typeof account.metadata === "object" && !Array.isArray(account.metadata) ? account.metadata : {};
    const poolGroupList = Array.isArray(account.pool_groups) ? account.pool_groups : [];
    const quotaSummary = Array.isArray(account.quota_summary) ? account.quota_summary : [];
    const lastQuota = account.last_quota_refresh ?? {};
    const routeTags = Array.isArray(metadata.route_tags) ? metadata.route_tags.map((item: unknown) => String(item)) : [];
    const poolGroupNames = poolGroupList.map((item: any) => item?.name).filter(Boolean);
    return {
      ...account,
      provider_name: providerNames.get(account.provider_id) ?? account.provider_id,
      channel_name: account.channel_id ? channelNames.get(account.channel_id) ?? account.channel_id : "",
      proxy_name: account.proxy_id ? proxies.find((item) => item.id === account.proxy_id)?.name ?? account.proxy_id : "",
      owner: account.owner_user_id ? userNames.get(account.owner_user_id) ?? account.owner_user_id : "共享池",
      pool_group: poolGroupNames.length ? poolGroupNames.join(", ") : (typeof metadata.pool_group === "string" && metadata.pool_group ? metadata.pool_group : "未分组"),
      route_tags: routeTags,
      quota_windows: quotaSummary.length,
      last_quota_status: lastQuota.status || "",
      last_quota_error: lastQuota.error_message || "",
      last_quota_at: lastQuota.created_at,
      auth_status: auth.auth_status || "api_key",
      auth_expires_at: auth.expires_at,
      concurrency: `${runtime.active_requests ?? 0}/${account.max_concurrency ?? 0}`,
      active_requests: runtime.active_requests ?? 0,
      circuit_state: runtime.circuit_state ?? "",
      runtime_last_error: runtime.last_error ?? "",
    };
  });
  const filteredPoolRows = poolRows.filter((row) => {
    const query = accountQuery.trim().toLowerCase();
    const matchesQuery = !query || [row.name, row.provider_name, row.channel_name, row.owner, row.pool_group, row.route_tags.join(","), row.auth_status, row.circuit_state].some((value) => String(value ?? "").toLowerCase().includes(query));
    const matchesStatus = !accountStatusFilter || row.status === accountStatusFilter;
    const matchesProvider = !accountProviderFilter || row.provider_id === accountProviderFilter;
    return matchesQuery && matchesStatus && matchesProvider;
  });
  const selectedRows = poolRows.filter((row) => selectedAccountIds.includes(row.id));
  const activeShared = accounts.filter((account) => account.routing_mode === "pool" && account.status === "active").length;
  const abnormal = accounts.filter((account) => account.status !== "active" || ["failed", "revoked", "disabled", "reauth_required"].includes(account.auth?.auth_status)).length;
  const activeRequests = accounts.reduce((sum, account) => sum + Number(account.runtime?.active_requests ?? 0), 0);
  const quotaRows = quotaWindows.map((row) => ({ ...row, account_name: accountNames.get(row.account_id) ?? row.account_id }));
  const quotaSnapshotRows = quotaSnapshots.map((row) => ({ ...row, account_name: accountNames.get(row.account_id) ?? row.account_name ?? row.account_id }));
  const quotaJobRows = quotaRefreshJobs.map((row) => ({ ...row, account_name: accountNames.get(row.account_id) ?? row.account_name ?? row.account_id }));
  const qualityRows = accountQuality.map((row) => ({ ...row, account_name: row.account_name || accountNames.get(row.account_id) || row.account_id }));
  const strategyEventRows = strategyEvents.map((row) => ({ ...row, account_name: row.account_name || accountNames.get(row.account_id) || row.account_id }));
  const wakeupJobRows = wakeupJobs.map((row) => ({ ...row, account_name: row.account_name || accountNames.get(row.account_id) || row.account_id || "自动候选" }));
  const platformConfigByType = new Map(platformConfigs.map((item) => [item.provider_type, item]));
  const platformRows = platformConfigs.map((row) => ({ ...row, display_name: row.display_name || providerTypeLabel(row.provider_type) }));
  const platformTypeOptions = Array.from(new Set([...providerTypes, ...platformConfigs.map((item) => String(item.provider_type || ""))].filter(Boolean))).sort();
  const abnormalRows = poolRows.filter((row) => row.status !== "active" || ["failed", "revoked", "disabled", "reauth_required"].includes(row.auth_status) || row.circuit_state === "open" || row.runtime_last_error).slice(0, 8);
  const quotaPressureAccounts = quotaRows.filter((row) => Number(row.remaining ?? 0) <= Number(row.metadata?.limit ?? row.remaining ?? 0) * 0.2);
  const quotaPressureRows = quotaPressureAccounts.slice(0, 8);
  const accountTestRows = channelTests
    .filter((row) => row.account_id)
    .map((row) => ({ ...row, account_name: accountNames.get(row.account_id) ?? row.account_id }))
    .slice(0, 20);
  const providerCockpitRows = providers.map((provider) => {
    const providerAccounts = poolRows.filter((row) => row.provider_id === provider.id);
    const providerChannels = channels.filter((row) => row.provider_id === provider.id);
    const platformConfig = platformConfigByType.get(provider.provider_type) ?? {};
    const active = providerAccounts.filter((row) => row.status === "active").length;
    const failedQuota = providerAccounts.filter((row) => row.last_quota_status === "failed").length;
    const unsupportedQuota = providerAccounts.filter((row) => row.last_quota_status === "unsupported").length;
    return {
      id: provider.id,
      provider_name: provider.name,
      provider_type: provider.provider_type,
      status: provider.status,
      account_count: providerAccounts.length,
      active_accounts: active,
      channel_count: providerChannels.length,
      failed_quota_refresh: failedQuota,
      unsupported_quota_refresh: unsupportedQuota,
      health_enabled: platformConfig.health_enabled ?? true,
      quota_refresh_enabled: platformConfig.quota_refresh_enabled ?? true,
      wakeup_enabled: platformConfig.wakeup_enabled ?? true,
    };
  });
  const cockpitRows = cockpitProvider ? poolRows.filter((row) => row.provider_id === cockpitProvider) : poolRows;
  const candidateRows = Array.isArray(route?.candidates) ? route.candidates : [];
  const importPreviewRows = Array.isArray(importPreview?.items) ? importPreview.items : [];
  const importPreviewSummary = importPreview?.summary ?? {};
  const selectedImportTemplate = importTemplates.find((item) => item.id === importTemplate);
  const poolPrimaryTab: PoolPrimaryTab = tab === "overview"
    ? "overview"
    : ["accounts", "add", "edit", "batch"].includes(tab)
      ? "accounts"
      : tab === "groups"
        ? "groups"
        : tab === "import"
          ? "import"
          : "advanced";
  const advancedPoolTab: PoolAdvancedTab = isPoolAdvancedTab(tab) ? tab : "quota";

  async function createAccount() {
    setError("");
    setMessage("");
    const selectedChannel = channels.find((item) => item.id === channelId);
    const accountProviderId = providerId || selectedChannel?.provider_id || "";
    try {
      await adminApi.request("/api/admin/v1/accounts", {
        method: "POST",
        body: JSON.stringify({
          provider_id: accountProviderId,
          channel_id: channelId,
          proxy_id: proxyId,
          routing_mode: routingMode,
          owner_user_id: routingMode === "byo" ? ownerUserId : "",
          auth_mode: authMode,
          name: accountName,
          api_key: apiKey,
          priority: Number(priority) || 100,
          max_concurrency: Number(maxConcurrency) || 10,
          metadata: mergeAccountMetadataFields(parseJSONObject(accountMetadata), accountPoolGroup, splitCSV(accountRouteTags)),
        }),
      });
      setApiKey("");
      setAccountName("");
      setAccountMetadata("{}");
      setAccountPoolGroup("");
      setAccountRouteTags("");
      setMessage("账号已加入号池");
      await load();
      setPoolModal(null);
      setTab("accounts");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function patchAccount(id: string, body: Record<string, unknown>) {
    setError("");
    setMessage("");
    try {
      await adminApi.request(`/api/admin/v1/accounts/${id}`, { method: "PATCH", body: JSON.stringify(body) });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  function selectAccount(row: any) {
    setEditAccountId(row.id);
    setEditAccountName(row.name ?? "");
    setEditAccountStatus(row.status ?? "active");
    setEditAccountRoutingMode(row.routing_mode ?? "pool");
    setEditAccountOwnerUserId(row.owner_user_id ?? "");
    setEditAccountChannelId(row.channel_id ?? "");
    setEditAccountProxyId(row.proxy_id ?? "");
    setEditAccountPriority(String(row.priority ?? 100));
    setEditAccountMaxConcurrency(String(row.max_concurrency ?? 10));
    setEditAccountAPIKey("");
    setEditAccountMetadata(jsonText(row.metadata));
    setEditAccountPoolGroup(metadataStringField(row.metadata, "pool_group"));
    setEditAccountRouteTags(metadataStringArrayField(row.metadata, "route_tags").join(","));
    setMessage("已选择账号");
    setPoolModal("edit");
  }

  async function saveAccount() {
    const body: Record<string, unknown> = {
      name: editAccountName,
      status: editAccountStatus,
      routing_mode: editAccountRoutingMode,
      owner_user_id: editAccountRoutingMode === "byo" ? editAccountOwnerUserId : "",
      channel_id: editAccountChannelId,
      proxy_id: editAccountProxyId,
      priority: Number(editAccountPriority) || 100,
      max_concurrency: Number(editAccountMaxConcurrency) || 10,
      metadata: mergeAccountMetadataFields(parseJSONObject(editAccountMetadata), editAccountPoolGroup, splitCSV(editAccountRouteTags)),
    };
    if (editAccountAPIKey.trim()) body.api_key = editAccountAPIKey;
    await patchAccount(editAccountId, body);
    setEditAccountAPIKey("");
    setPoolModal(null);
  }

  async function createQuotaWindow() {
    setError("");
    setMessage("");
    try {
      await adminApi.request("/api/admin/v1/account-quota-windows", {
        method: "POST",
        body: JSON.stringify({
          account_id: quotaAccountId,
          window_type: quotaType,
          reset_at: quotaResetAt,
          remaining: quotaRemaining,
          metadata: quotaLimit ? { limit: quotaLimit } : {},
        }),
      });
      setMessage("配额窗口已保存");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function patchQuotaWindow(id: string, remaining: string) {
    setError("");
    setMessage("");
    try {
      await adminApi.request(`/api/admin/v1/account-quota-windows/${id}`, { method: "PATCH", body: JSON.stringify({ remaining }) });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function explainRoute() {
    setError("");
    const query = new URLSearchParams({ model: routeModel, endpoint: routeEndpoint, routing_mode: routeRoutingMode });
    if (routeOwnerUserId) query.set("user_id", routeOwnerUserId);
    if (routeApiKeyId) query.set("api_key_id", routeApiKeyId);
    if (routeAffinityKey) query.set("affinity_key", routeAffinityKey);
    if (routeTags) query.set("route_tags", routeTags);
    try {
      setRoute(await adminApi.request(`/api/admin/v1/runtime/route-explain?${query.toString()}`));
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  function toggleSelected(id: string) {
    setSelectedAccountIds((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
  }

  function selectVisibleAccounts() {
    const ids = filteredPoolRows.map((row) => row.id);
    setSelectedAccountIds((current) => {
      const merged = new Set([...current, ...ids]);
      return Array.from(merged);
    });
  }

  async function batchAccounts(action: string, body: Record<string, unknown> = {}) {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<{ updated: number; action: string }>("/api/admin/v1/accounts/batch", {
        method: "POST",
        body: JSON.stringify({ account_ids: selectedAccountIds, action, ...body }),
      });
      setMessage(`批量操作完成：${result.updated}`);
      await load();
      setPoolModal(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function runSelectedHealthCheck() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<{ results: any[] }>("/api/admin/v1/accounts/health-check", {
        method: "POST",
        body: JSON.stringify({ account_ids: selectedAccountIds, target_path: healthTargetPath, model_name: healthModel, endpoint: healthEndpoint }),
      });
      setMessage(`检测完成：${result.results.length}`);
      await load();
      setTab("health");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function runAllQuotaRefresh() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>("/api/admin/v1/accounts/quota-refresh", {
        method: "POST",
        body: JSON.stringify({}),
      });
      setMessage(`额度刷新完成：成功 ${result.success_count ?? 0}，失败 ${result.failed_count ?? 0}，不支持 ${result.unsupported_count ?? 0}`);
      await load();
      setTab("quota");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function refreshSelectedQuota() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>("/api/admin/v1/accounts/quota-refresh", {
        method: "POST",
        body: JSON.stringify({ account_ids: selectedAccountIds }),
      });
      setMessage(`额度刷新完成：成功 ${result.success_count ?? 0}，失败 ${result.failed_count ?? 0}，不支持 ${result.unsupported_count ?? 0}`);
      await load();
      setTab("quota");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function refreshAccountQuota(accountId: string) {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>(`/api/admin/v1/accounts/${accountId}/quota-refresh`, { method: "POST", body: JSON.stringify({}) });
      setMessage(`额度刷新完成：成功 ${result.success_count ?? 0}，失败 ${result.failed_count ?? 0}，不支持 ${result.unsupported_count ?? 0}`);
      await load();
      setTab("quota");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function recomputeQuality(selectedOnly = false) {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>("/api/admin/v1/account-quality/recompute", {
        method: "POST",
        body: JSON.stringify({
          account_ids: selectedOnly ? selectedAccountIds : [],
          apply_actions: qualityApplyActions,
          isolation_threshold: Number(qualityIsolationThreshold) || 40,
          watch_threshold: Number(qualityWatchThreshold) || 70,
        }),
      });
      setMessage(`质量评分完成：总数 ${result.total_count ?? 0}，观察 ${result.watch_count ?? 0}，隔离 ${result.isolate_count ?? 0}`);
      await load();
      setTab("quality");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function accountQualityAction(accountId: string, action: "isolate" | "cooldown" | "restore") {
    setError("");
    setMessage("");
    try {
      await adminApi.request(`/api/admin/v1/accounts/${accountId}/quality-action`, {
        method: "POST",
        body: JSON.stringify({ action, reason: "admin quality action" }),
      });
      setMessage("账号质量处置已完成");
      await load();
      setTab("quality");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  function wakeupScheduledForValue() {
    const trimmed = wakeupScheduledFor.trim();
    if (!trimmed) return "";
    const parsed = new Date(trimmed);
    return Number.isNaN(parsed.getTime()) ? trimmed : parsed.toISOString();
  }

  async function createWakeupJob(runNow: boolean, useAutoCandidates = false) {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>("/api/admin/v1/account-wakeup-jobs", {
        method: "POST",
        body: JSON.stringify({
          account_ids: useAutoCandidates ? [] : selectedAccountIds,
          target_path: wakeupTargetPath,
          model_name: wakeupModel,
          endpoint: wakeupEndpoint,
          scheduled_for: runNow ? "" : wakeupScheduledForValue(),
          run_now: runNow,
        }),
      });
      setMessage(`唤醒任务${runNow ? "执行" : "创建"}完成：成功 ${result.success_count ?? 0}，失败 ${result.failed_count ?? 0}，总数 ${result.total_count ?? 0}`);
      await load();
      setTab("wakeup");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function runWakeupJob(jobId: string) {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>(`/api/admin/v1/account-wakeup-jobs/${jobId}/run`, { method: "POST", body: JSON.stringify({}) });
      setMessage(`唤醒任务已运行：成功 ${result.success_count ?? 0}，失败 ${result.failed_count ?? 0}`);
      await load();
      setTab("wakeup");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  function selectPlatformConfig(row: any) {
    setPlatformProviderType(row.provider_type ?? "");
    setPlatformDisplayName(row.display_name ?? providerTypeLabel(row.provider_type ?? ""));
    setPlatformStatus(row.status ?? "active");
    setPlatformHealthEnabled(row.health_enabled ?? true);
    setPlatformQuotaRefreshEnabled(row.quota_refresh_enabled ?? true);
    setPlatformWakeupEnabled(row.wakeup_enabled ?? true);
    setPlatformHealthInterval(String(row.health_interval_seconds ?? 300));
    setPlatformQuotaRefreshInterval(String(row.quota_refresh_interval_seconds ?? 900));
    setPlatformWakeupInterval(String(row.wakeup_interval_seconds ?? 300));
    setPlatformQuotaThreshold(String(row.quota_low_threshold_percent ?? 20));
    setPlatformMaxFailures(String(row.max_failure_count ?? 5));
    setPlatformMetadata(jsonText(row.metadata));
    setMessage("已选择平台配置");
  }

  async function savePlatformConfig() {
    setError("");
    setMessage("");
    try {
      const providerType = platformProviderType.trim();
      if (!providerType) throw new Error("请选择平台类型。");
      const toNumber = (value: string, fallback: number) => {
        const trimmed = value.trim();
        if (!trimmed) return fallback;
        const next = Number(trimmed);
        return Number.isFinite(next) ? next : fallback;
      };
      await adminApi.request(`/api/admin/v1/account-platform-configs/${encodeURIComponent(providerType)}`, {
        method: "PUT",
        body: JSON.stringify({
          display_name: platformDisplayName || providerTypeLabel(providerType),
          status: platformStatus,
          health_enabled: platformHealthEnabled,
          quota_refresh_enabled: platformQuotaRefreshEnabled,
          wakeup_enabled: platformWakeupEnabled,
          health_interval_seconds: toNumber(platformHealthInterval, 300),
          quota_refresh_interval_seconds: toNumber(platformQuotaRefreshInterval, 900),
          wakeup_interval_seconds: toNumber(platformWakeupInterval, 300),
          quota_low_threshold_percent: toNumber(platformQuotaThreshold, 20),
          max_failure_count: toNumber(platformMaxFailures, 5),
          metadata: parseJSONObject(platformMetadata),
        }),
      });
      setMessage("平台配置已保存");
      await load();
      setTab("platforms");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function exportAccounts() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>("/api/admin/v1/accounts/export?limit=2000");
      setExportText(JSON.stringify(result, null, 2));
      setMessage("账号导出已生成，密钥不会导出。");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  function parseImportItems() {
    const parsed = JSON.parse(importText) as unknown;
    const items = Array.isArray(parsed) ? parsed : (parsed && typeof parsed === "object" && "items" in parsed ? (parsed as { items: unknown }).items : null);
    if (!Array.isArray(items)) throw new Error("导入 JSON 必须是数组或包含 items 数组。");
    return items;
  }

  function importRequestBody() {
    return {
      template: importTemplate,
      provider_id: importProviderId,
      channel_id: importChannelId,
      pool_group: importPoolGroup,
      route_tags: splitCSV(importRouteTags),
      default_metadata: parseJSONObject(importDefaultMetadata),
      items: parseImportItems(),
    };
  }

  function applyImportTemplateSample() {
    const sample = selectedImportTemplate?.sample_item ?? { name: "account-1", api_key: "sk-..." };
    setImportText(JSON.stringify([sample], null, 2));
    setImportPreview(null);
  }

  async function previewImportAccounts() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<any>("/api/admin/v1/accounts/import-preview", {
        method: "POST",
        body: JSON.stringify(importRequestBody()),
      });
      setImportPreview(result);
      setMessage(`预览完成：有效 ${result.summary?.valid_count ?? 0}，无效 ${result.summary?.invalid_count ?? 0}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function importAccounts() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<{ imported: number }>("/api/admin/v1/accounts/import", {
        method: "POST",
        body: JSON.stringify(importRequestBody()),
      });
      setImportText("");
      setImportPreview(null);
      setMessage(`已导入账号：${result.imported}`);
      await load();
      setPoolModal(null);
      setTab("accounts");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function importPlainKeys() {
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<{ imported: number }>("/api/admin/v1/accounts/import-keys", {
        method: "POST",
        body: JSON.stringify({
          template: importTemplate,
          provider_id: importProviderId,
          channel_id: importChannelId,
          routing_mode: quickImportRoutingMode,
          owner_user_id: quickImportRoutingMode === "byo" ? quickImportOwnerUserId : "",
          pool_group: importPoolGroup,
          route_tags: splitCSV(importRouteTags),
          default_metadata: parseJSONObject(importDefaultMetadata),
          text: quickImportKeys,
          name_prefix: quickImportNamePrefix,
          priority: Number(quickImportPriority) || 0,
          max_concurrency: Number(quickImportMaxConcurrency) || 0,
        }),
      });
      setQuickImportKeys("");
      setMessage(`已导入账号：${result.imported}`);
      await load();
      setPoolModal(null);
      setTab("accounts");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  return (
    <div className="stack">
      <div className="grid three">
        <Metric label="账号总数" value={String(accounts.length)} />
        <Metric label="共享池可用" value={String(activeShared)} />
        <Metric label="异常账号" value={String(abnormal)} />
        <Metric label="当前并发" value={String(activeRequests)} />
        <Metric label="低额度窗口" value={String(quotaPressureAccounts.length)} />
      </div>
      <SectionTabs
        active={poolPrimaryTab}
        onChange={(nextTab) => setTab(nextTab === "advanced" ? advancedPoolTab : nextTab)}
        tabs={[
          { id: "overview", label: "看板" },
          { id: "accounts", label: "账号", count: filteredPoolRows.length },
          { id: "groups", label: "分组", count: poolGroups.length },
          { id: "import", label: "导入导出" },
          { id: "advanced", label: "高级" },
        ]}
      />
      <PageNotice error={error} message={message} />

      {tab === "overview" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader
              title="异常账号"
              action={<button onClick={() => setTab("accounts")}>查看账号</button>}
            />
            <DataTable rows={abnormalRows} columns={["name", "provider_name", "channel_name", "routing_mode", "owner", "status", "auth_status", "circuit_state", "runtime_last_error"]} />
          </section>
          <section className="panel">
            <SectionHeader
              title="低额度窗口"
              action={<button onClick={() => setTab("quota")}>管理配额</button>}
            />
            <DataTable rows={quotaPressureRows} columns={["account_name", "window_type", "remaining", "reset_at", "metadata"]} />
          </section>
          <section className="panel">
            <SectionHeader
              title="分组概况"
              action={<button onClick={() => setTab("groups")}>管理分组</button>}
            />
            <DataTable rows={poolGroups.slice(0, 8)} columns={["group_name", "account_count", "active_accounts", "abnormal_accounts", "default_route_tags"]} />
          </section>
          <section className="panel">
            <SectionHeader
              title="最近策略事件"
              action={<button onClick={() => setTab("events")}>查看事件</button>}
            />
            <DataTable rows={strategyEventRows.slice(0, 8)} columns={["account_name", "event_type", "action", "previous_status", "next_status", "decision", "quality_score", "reason", "created_at"]} />
          </section>
        </div>
      )}

      {tab === "accounts" && (
        <section className="panel">
          <SectionHeader
            title="账号池"
            action={<button onClick={() => setPoolModal("add")}>加入账号</button>}
          />
          <div className="form-grid">
            <input value={accountQuery} onChange={(event) => setAccountQuery(event.target.value)} placeholder="搜索账号、通道、归属、状态" />
            <select value={accountProviderFilter} onChange={(event) => setAccountProviderFilter(event.target.value)}>
              <option value="">全部供应商</option>
              {providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
            <select value={accountStatusFilter} onChange={(event) => setAccountStatusFilter(event.target.value)}>
              <option value="">全部状态</option>
              <option value="active">启用</option>
              <option value="disabled">停用</option>
              <option value="cooldown">冷却</option>
              <option value="exhausted">已耗尽</option>
            </select>
          </div>
          <div className="actions panel-actions">
            <button onClick={selectVisibleAccounts} disabled={!filteredPoolRows.length}>选择当前结果</button>
            {selectedAccountIds.length > 0 && <button onClick={() => setSelectedAccountIds([])}>清空选择</button>}
            {selectedAccountIds.length > 0 && <button onClick={() => setPoolModal("batch")}>批量操作（{selectedAccountIds.length}）</button>}
          </div>
          <DataTable rows={filteredPoolRows} columns={["name", "provider_name", "channel_name", "pool_group", "routing_mode", "owner", "status", "auth_status", "concurrency", "circuit_state", "last_quota_status", "runtime_last_error"]} action={(row) => <div className="actions"><button onClick={() => toggleSelected(row.id)}>{selectedAccountIds.includes(row.id) ? "取消" : "选择"}</button><button onClick={() => selectAccount(row)}>编辑</button><button onClick={() => patchAccount(row.id, { status: row.status === "active" ? "disabled" : "active" })}>{row.status === "active" ? "停用" : "启用"}</button><button onClick={() => patchAccount(row.id, { status: "cooldown" })}>冷却</button></div>} />
        </section>
      )}

      {tab === "groups" && (
        <section className="panel">
          <SectionHeader
            title="号池分组"
            action={<button onClick={() => setPoolModal("batch")} disabled={!selectedAccountIds.length}>批量维护</button>}
          />
          <DataTable rows={poolGroups} columns={["group_name", "status", "priority", "account_count", "active_accounts", "abnormal_accounts", "default_route_tags"]} />
        </section>
      )}

      {poolPrimaryTab === "advanced" && (
        <SectionTabs
          active={advancedPoolTab}
          onChange={setTab}
          tabs={[
            { id: "cockpit", label: "运营概况", count: providers.length },
            { id: "quota", label: "配额", count: quotaPressureAccounts.length },
            { id: "health", label: "健康检测" },
            { id: "quality", label: "质量", count: qualityRows.length },
            { id: "events", label: "策略事件", count: strategyEventRows.length },
            { id: "wakeup", label: "唤醒", count: wakeupJobRows.length },
            { id: "route", label: "路由诊断" },
            { id: "platforms", label: "平台策略", count: platformRows.length },
          ]}
        />
      )}

      {tab === "cockpit" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader
              title="运营概况"
              action={<button onClick={runAllQuotaRefresh} disabled={!accounts.length}>刷新全部额度</button>}
            />
            <div className="form-grid">
              <select value={cockpitProvider} onChange={(event) => setCockpitProvider(event.target.value)}>
                <option value="">全部供应商</option>
                {providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
              <button onClick={() => setTab("accounts")}>进入账号列表</button>
              <button onClick={() => setTab("health")}>查看健康检测</button>
              <button onClick={() => setTab("platforms")}>平台配置</button>
            </div>
            <DataTable rows={providerCockpitRows} columns={["provider_name", "provider_type", "status", "account_count", "active_accounts", "channel_count", "health_enabled", "quota_refresh_enabled", "wakeup_enabled", "failed_quota_refresh", "unsupported_quota_refresh"]} />
          </section>
          <section className="panel">
            <SectionHeader title="账号运行视图" />
            <DataTable rows={cockpitRows} columns={["name", "provider_name", "channel_name", "pool_group", "routing_mode", "owner", "status", "auth_status", "circuit_state", "concurrency", "quota_windows", "last_quota_status", "last_quota_error", "last_quota_at"]} action={(row) => <div className="actions"><button onClick={() => selectAccount(row)}>编辑</button><button onClick={() => refreshAccountQuota(row.id)}>刷新额度</button></div>} />
          </section>
        </div>
      )}

      {tab === "quota" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader
              title="配额窗口"
              description="维护账号请求数、tokens 或平台额度窗口；刷新结果会写入快照和窗口。"
              action={<div className="actions"><button onClick={runAllQuotaRefresh} disabled={!accounts.length}>刷新全部</button><button onClick={refreshSelectedQuota} disabled={!selectedAccountIds.length}>刷新已选</button></div>}
            />
            <div className="form-grid">
              <select value={quotaAccountId} onChange={(event) => setQuotaAccountId(event.target.value)}>
                <option value="">账号</option>
                {accounts.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
              <input value={quotaType} onChange={(event) => setQuotaType(event.target.value)} placeholder="窗口类型" />
              <input value={quotaRemaining} onChange={(event) => setQuotaRemaining(event.target.value)} placeholder="剩余" />
              <input value={quotaLimit} onChange={(event) => setQuotaLimit(event.target.value)} placeholder="总额度" />
              <input value={quotaResetAt} onChange={(event) => setQuotaResetAt(event.target.value)} placeholder="重置时间 RFC3339" />
              <button onClick={createQuotaWindow} disabled={!quotaAccountId}>保存窗口</button>
              <button onClick={() => quotaAccountId && refreshAccountQuota(quotaAccountId)} disabled={!quotaAccountId}>刷新该账号</button>
            </div>
            <DataTable rows={quotaRows} columns={["account_name", "window_type", "remaining", "reset_at", "metadata", "created_at"]} action={(row) => <div className="actions"><button onClick={() => patchQuotaWindow(row.id, "0")}>清零</button><button onClick={() => patchQuotaWindow(row.id, quotaLimit || "100")}>补满</button></div>} />
          </section>
          <section className="panel">
            <SectionHeader title="刷新快照" description="记录每次从账号元数据或 quota_endpoint 拉取到的额度结果， unsupported 不会伪造成额度。" />
            <DataTable rows={quotaSnapshotRows} columns={["account_name", "window_type", "remaining", "limit_value", "reset_at", "status", "source", "error_message", "created_at"]} />
          </section>
          <section className="panel">
            <SectionHeader title="刷新任务" description="展示手动或后台额度刷新任务的执行结果。" />
            <DataTable rows={quotaJobRows} columns={["account_name", "trigger_type", "status", "total_count", "success_count", "failed_count", "unsupported_count", "error_message", "created_at", "finished_at"]} />
          </section>
        </div>
      )}

      {tab === "health" && (
        <section className="panel">
          <SectionHeader title="账号健康检测" description="对已选择账号发起上游检测，结果记录到检测历史。" />
          <div className="form-grid">
            <input value={healthTargetPath} onChange={(event) => setHealthTargetPath(event.target.value)} placeholder="检测路径，例如 /models" />
            <input value={healthModel} onChange={(event) => setHealthModel(event.target.value)} placeholder="模型，可选" list="admin-pool-model-list" />
            <input value={healthEndpoint} onChange={(event) => setHealthEndpoint(event.target.value)} placeholder="端点" />
            <button onClick={runSelectedHealthCheck} disabled={!selectedAccountIds.length}>检测已选择账号</button>
          </div>
          <DataTable rows={accountTestRows} columns={["account_name", "channel_id", "model_name", "endpoint", "status", "latency_ms", "upstream_status", "error_message", "tested_at"]} />
        </section>
      )}

      {tab === "quality" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader
              title="账号质量评分"
              description="综合健康检测、运行失败、熔断、额度和延迟生成评分；自动隔离默认只在勾选后执行。"
              action={<button onClick={() => void load()}>刷新</button>}
            />
            <div className="grid three">
              <Metric label="已评分账号" value={String(qualityRows.length)} />
              <Metric label="观察" value={String(qualityRows.filter((row) => row.decision === "watch").length)} />
              <Metric label="隔离建议" value={String(qualityRows.filter((row) => row.decision === "isolate").length)} />
            </div>
            <div className="form-grid">
              <input value={qualityIsolationThreshold} onChange={(event) => setQualityIsolationThreshold(event.target.value)} placeholder="隔离阈值，默认 40" />
              <input value={qualityWatchThreshold} onChange={(event) => setQualityWatchThreshold(event.target.value)} placeholder="观察阈值，默认 70" />
              <select value={String(qualityApplyActions)} onChange={(event) => setQualityApplyActions(event.target.value === "true")}>
                <option value="false">仅评分</option>
                <option value="true">评分并隔离</option>
              </select>
              <button onClick={() => recomputeQuality(false)}>重新评分全部</button>
              <button onClick={() => recomputeQuality(true)} disabled={!selectedAccountIds.length}>重新评分已选</button>
            </div>
          </section>
          <section className="panel">
            <SectionHeader title="质量结果" description="低分账号可手动隔离、冷却或恢复，隔离会从路由中移除账号。" />
            <DataTable
              rows={qualityRows}
              columns={["account_name", "provider_name", "channel_name", "account_status", "quality_score", "quality_status", "decision", "availability_score", "latency_score", "quota_score", "error_score", "reason_summary", "created_at"]}
              action={(row) => (
                <div className="actions">
                  <button onClick={() => accountQualityAction(row.account_id, "isolate")}>隔离</button>
                  <button onClick={() => accountQualityAction(row.account_id, "cooldown")}>冷却</button>
                  <button onClick={() => accountQualityAction(row.account_id, "restore")}>恢复</button>
                </div>
              )}
            />
          </section>
        </div>
      )}

      {tab === "events" && (
        <section className="panel">
          <SectionHeader
            title="策略事件"
            description="展示号池策略和管理员处置造成的账号状态变化，用于解释自动隔离、冷却和恢复。"
            action={<button onClick={() => void load()}>刷新</button>}
          />
          <DataTable
            rows={strategyEventRows}
            columns={["account_name", "provider_name", "channel_name", "event_type", "action", "previous_status", "next_status", "decision", "quality_score", "reason", "actor_user_id", "created_at"]}
          />
        </section>
      )}

      {tab === "wakeup" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader
              title="唤醒任务"
              description="对冷却或耗尽账号发起真实上游探测，成功后恢复账号状态并清理运行错误。"
              action={<button onClick={() => void load()}>刷新</button>}
            />
            <div className="grid three">
              <Metric label="已选择账号" value={String(selectedAccountIds.length)} />
              <Metric label="待运行任务" value={String(wakeupJobRows.filter((row) => row.status === "pending").length)} />
              <Metric label="失败任务" value={String(wakeupJobRows.filter((row) => row.status === "failed").length)} />
            </div>
            <div className="form-grid">
              <input value={wakeupTargetPath} onChange={(event) => setWakeupTargetPath(event.target.value)} placeholder="探测路径，例如 /models" />
              <input value={wakeupModel} onChange={(event) => setWakeupModel(event.target.value)} placeholder="模型，可选" list="admin-pool-model-list" />
              <input value={wakeupEndpoint} onChange={(event) => setWakeupEndpoint(event.target.value)} placeholder="端点" />
              <input type="datetime-local" value={wakeupScheduledFor} onChange={(event) => setWakeupScheduledFor(event.target.value)} />
              <button onClick={() => createWakeupJob(true)} disabled={!selectedAccountIds.length}>立即唤醒已选</button>
              <button onClick={() => createWakeupJob(true, true)}>立即唤醒自动候选</button>
              <button onClick={() => createWakeupJob(false)} disabled={!wakeupScheduledFor.trim()}>创建定时任务</button>
            </div>
          </section>
          <section className="panel">
            <SectionHeader title="唤醒记录" description="记录手动和后台唤醒任务的账号数、成功数、失败数和上游返回结果。" />
            <DataTable
              rows={wakeupJobRows}
              columns={["account_name", "trigger_type", "status", "total_count", "success_count", "failed_count", "skipped_count", "target_path", "model_name", "endpoint", "scheduled_for", "error_message", "created_at", "finished_at"]}
              action={(row) => <div className="actions">{["pending", "failed"].includes(row.status) && <button onClick={() => runWakeupJob(row.id)}>运行</button>}</div>}
            />
          </section>
        </div>
      )}

      {tab === "platforms" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader
              title="平台号池配置"
              description="按供应商类型控制健康检测、额度刷新和唤醒任务，后台 worker 会读取这些开关。"
              action={<button onClick={() => void load()}>刷新</button>}
            />
            <div className="form-grid">
              <select
                value={platformProviderType}
                onChange={(event) => {
                  const nextType = event.target.value;
                  const existing = platformRows.find((row) => row.provider_type === nextType);
                  if (existing) {
                    selectPlatformConfig(existing);
                  } else {
                    setPlatformProviderType(nextType);
                    setPlatformDisplayName(providerTypeLabel(nextType));
                    setPlatformStatus("active");
                    setPlatformHealthEnabled(true);
                    setPlatformQuotaRefreshEnabled(true);
                    setPlatformWakeupEnabled(true);
                    setPlatformHealthInterval("300");
                    setPlatformQuotaRefreshInterval("900");
                    setPlatformWakeupInterval("300");
                    setPlatformQuotaThreshold("20");
                    setPlatformMaxFailures("5");
                    setPlatformMetadata("{}");
                  }
                }}
              >
                <option value="">平台类型</option>
                {platformTypeOptions.map((item) => <option key={item} value={item}>{providerTypeLabel(item)}</option>)}
              </select>
              <input value={platformDisplayName} onChange={(event) => setPlatformDisplayName(event.target.value)} placeholder="显示名称" />
              <select value={platformStatus} onChange={(event) => setPlatformStatus(event.target.value)}>
                <option value="active">启用</option>
                <option value="disabled">停用</option>
              </select>
              <select value={String(platformHealthEnabled)} onChange={(event) => setPlatformHealthEnabled(event.target.value === "true")}>
                <option value="true">健康检测启用</option>
                <option value="false">健康检测停用</option>
              </select>
              <select value={String(platformQuotaRefreshEnabled)} onChange={(event) => setPlatformQuotaRefreshEnabled(event.target.value === "true")}>
                <option value="true">额度刷新启用</option>
                <option value="false">额度刷新停用</option>
              </select>
              <select value={String(platformWakeupEnabled)} onChange={(event) => setPlatformWakeupEnabled(event.target.value === "true")}>
                <option value="true">唤醒启用</option>
                <option value="false">唤醒停用</option>
              </select>
              <input value={platformHealthInterval} onChange={(event) => setPlatformHealthInterval(event.target.value)} placeholder="健康检测间隔秒" />
              <input value={platformQuotaRefreshInterval} onChange={(event) => setPlatformQuotaRefreshInterval(event.target.value)} placeholder="额度刷新间隔秒" />
              <input value={platformWakeupInterval} onChange={(event) => setPlatformWakeupInterval(event.target.value)} placeholder="唤醒间隔秒" />
              <input value={platformQuotaThreshold} onChange={(event) => setPlatformQuotaThreshold(event.target.value)} placeholder="低额度阈值 %" />
              <input value={platformMaxFailures} onChange={(event) => setPlatformMaxFailures(event.target.value)} placeholder="最大失败次数" />
              <textarea value={platformMetadata} onChange={(event) => setPlatformMetadata(event.target.value)} placeholder="平台配置元数据 JSON" rows={4} />
              <button onClick={savePlatformConfig} disabled={!platformProviderType}>保存配置</button>
            </div>
          </section>
          <section className="panel">
            <SectionHeader title="平台配置列表" description="展示每类平台的号池开关、调度参数和账号聚合数量。" />
            <DataTable
              rows={platformRows}
              columns={["display_name", "provider_type", "status", "provider_count", "channel_count", "account_count", "active_accounts", "abnormal_accounts", "health_enabled", "quota_refresh_enabled", "wakeup_enabled", "health_interval_seconds", "quota_refresh_interval_seconds", "wakeup_interval_seconds", "quota_low_threshold_percent", "max_failure_count", "updated_at"]}
              action={(row) => <button onClick={() => selectPlatformConfig(row)}>编辑</button>}
            />
          </section>
        </div>
      )}

      {tab === "import" && (
        <section className="panel">
          <SectionHeader
            title="导入导出"
            action={<div className="actions"><button onClick={() => setPoolModal("quick_import")}>快速粘贴密钥</button><button onClick={() => setPoolModal("advanced_import")}>JSON 导入/导出</button></div>}
          />
          <div className="grid three">
            <Metric label="可导出账号" value={String(accounts.length)} />
            <Metric label="导入模板" value={String(importTemplates.length)} />
            <Metric label="预览有效" value={String(importPreviewSummary.valid_count ?? 0)} />
          </div>
          {selectedImportTemplate && (
            <div className="subtle">
              {selectedImportTemplate.provider_type ? providerTypeLabel(selectedImportTemplate.provider_type) : "按选择供应商"} · {selectedImportTemplate.auth_mode || "api_key"} · {selectedImportTemplate.credential_hint}
            </div>
          )}
        </section>
      )}

      {tab === "route" && (
        <section className="panel">
          <SectionHeader title="路由解释" description="用当前模型、端点和路由模式预览候选账号与排除原因。" />
          <div className="form-grid">
            <input value={routeModel} onChange={(event) => setRouteModel(event.target.value)} placeholder="模型" list="admin-pool-model-list" />
            <datalist id="admin-pool-model-list">{models.map((item) => <option key={item.model_name} value={item.model_name} />)}</datalist>
            <input value={routeEndpoint} onChange={(event) => setRouteEndpoint(event.target.value)} placeholder="端点" />
            <select value={routeRoutingMode} onChange={(event) => setRouteRoutingMode(event.target.value)}>
              <option value="pool">共享池</option>
              <option value="byo">自带账号</option>
            </select>
            <select value={routeOwnerUserId} onChange={(event) => setRouteOwnerUserId(event.target.value)} disabled={routeRoutingMode !== "byo"}>
              <option value="">所属用户</option>
              {users.map((item) => <option key={item.id} value={item.id}>{item.email}</option>)}
            </select>
            <input value={routeApiKeyId} onChange={(event) => setRouteApiKeyId(event.target.value)} placeholder="API 密钥 ID" />
            <input value={routeAffinityKey} onChange={(event) => setRouteAffinityKey(event.target.value)} placeholder="会话亲和键" />
            <input value={routeTags} onChange={(event) => setRouteTags(event.target.value)} placeholder="路由标签，逗号分隔" />
            <button onClick={explainRoute} disabled={!routeModel || !routeEndpoint || (routeRoutingMode === "byo" && !routeOwnerUserId)}>解释</button>
          </div>
          {route && <div className="grid three"><Metric label="可用性" value={route.available ? "可用" : "不可用"} /><Metric label="命中账号" value={route.selected?.account_name ?? "-"} /><Metric label="候选数" value={String(candidateRows.length)} /></div>}
          <DataTable rows={candidateRows} columns={["account_name", "provider_name", "routing_mode", "owner_user_id", "auth_status", "eligible", "excluded_reasons", "active_requests", "max_concurrency", "headroom_score", "failure_penalty", "quota_reset_at"]} />
        </section>
      )}

      <ActionModal
        open={poolModal === "add"}
        title="加入账号"
        size="lg"
        onClose={() => setPoolModal(null)}
        footer={<><button onClick={() => setPoolModal(null)}>取消</button><button className="primary" onClick={createAccount} disabled={!channelId || !(providerId || channels.find((item) => item.id === channelId)?.provider_id) || !accountName || !apiKey || (routingMode === "byo" && !ownerUserId)}>加入</button></>}
      >
        <div className="form-grid">
          <FormField label="通道" hint="这个账号会通过哪个上游通道发请求。">
            <select value={channelId} onChange={(event) => { const nextChannelId = event.target.value; setChannelId(nextChannelId); const channel = channels.find((item) => item.id === nextChannelId); if (channel?.provider_id) setProviderId(channel.provider_id); }}>
              <option value="">请选择通道</option>
              {channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </FormField>
          <FormField label="供应商" hint="选择通道后会自动带出，一般不用手动改。">
            <select value={providerId} onChange={(event) => setProviderId(event.target.value)}>
              <option value="">请选择供应商</option>
              {providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </FormField>
          <FormField label="账号类型" hint="共享池给平台用户共用；自带账号只给所属用户用。">
            <select value={routingMode} onChange={(event) => setRoutingMode(event.target.value)}>
              <option value="pool">共享池</option>
              <option value="byo">自带账号</option>
            </select>
          </FormField>
          <FormField label="所属用户" hint="只有自带账号需要选择。">
            <select value={ownerUserId} onChange={(event) => setOwnerUserId(event.target.value)} disabled={routingMode !== "byo"}>
              <option value="">请选择用户</option>
              {users.map((item) => <option key={item.id} value={item.id}>{item.email}</option>)}
            </select>
          </FormField>
          <FormField label="账号名称" hint="后台识别用，例如 OpenAI key 01。">
            <input value={accountName} onChange={(event) => setAccountName(event.target.value)} placeholder="OpenAI key 01" />
          </FormField>
          <FormField label="上游密钥" hint="粘贴上游 API Key；OAuth 账号走 OAuth 页面授权。">
            <input value={apiKey} onChange={(event) => setApiKey(event.target.value)} placeholder="sk-..." type="password" />
          </FormField>
          <FormField label="号池分组" hint="可选。用于 premium、free、backup 等分组调度。">
            <select value={accountPoolGroup} onChange={(event) => setAccountPoolGroup(event.target.value)}>
              <option value="">不指定分组</option>
              {poolGroups.map((item) => <option key={item.id ?? item.group_name} value={item.group_name}>{item.group_name}</option>)}
            </select>
          </FormField>
          <FormField label="路由标签" hint="可选，多个用逗号分隔，例如 premium,fast。">
            <input value={accountRouteTags} onChange={(event) => setAccountRouteTags(event.target.value)} placeholder="premium,fast" />
          </FormField>
          <details className="advanced-details wide">
            <summary>高级配置</summary>
            <div className="form-grid">
              <FormField label="代理" hint="不需要代理就保持不使用。">
                <select value={proxyId} onChange={(event) => setProxyId(event.target.value)}>
                  <option value="">不使用代理</option>
                  {proxies.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
                </select>
              </FormField>
              <FormField label="认证模式"><select value={authMode} onChange={(event) => setAuthMode(event.target.value)}><option value="api_key">API Key</option><option value="oauth">OAuth</option></select></FormField>
              <FormField label="优先级" hint="数字越小越优先；不确定就用 100。"><input value={priority} onChange={(event) => setPriority(event.target.value)} inputMode="numeric" placeholder="100" /></FormField>
              <FormField label="最大并发" hint="这个账号同时承载的请求数。"><input value={maxConcurrency} onChange={(event) => setMaxConcurrency(event.target.value)} inputMode="numeric" placeholder="10" /></FormField>
            </div>
          </details>
        </div>
      </ActionModal>

      <ActionModal
        open={poolModal === "edit"}
        title="编辑账号"
        size="lg"
        onClose={() => setPoolModal(null)}
        footer={<><button onClick={() => setPoolModal(null)}>取消</button><button className="primary" onClick={saveAccount} disabled={!editAccountId}>保存账号</button></>}
      >
        <div className="form-grid">
          <FormField label="账号 ID"><input value={editAccountId} onChange={(event) => setEditAccountId(event.target.value)} disabled /></FormField>
          <FormField label="账号名称"><input value={editAccountName} onChange={(event) => setEditAccountName(event.target.value)} placeholder="OpenAI key 01" /></FormField>
          <FormField label="状态">
            <select value={editAccountStatus} onChange={(event) => setEditAccountStatus(event.target.value)}>
              <option value="active">启用</option>
              <option value="disabled">停用</option>
              <option value="cooldown">冷却</option>
              <option value="exhausted">已耗尽</option>
            </select>
          </FormField>
          <FormField label="账号类型">
            <select value={editAccountRoutingMode} onChange={(event) => setEditAccountRoutingMode(event.target.value)}>
              <option value="pool">共享池</option>
              <option value="byo">自带账号</option>
            </select>
          </FormField>
          <FormField label="所属用户" hint="只有自带账号需要选择。">
            <select value={editAccountOwnerUserId} onChange={(event) => setEditAccountOwnerUserId(event.target.value)} disabled={editAccountRoutingMode !== "byo"}>
              <option value="">请选择用户</option>
              {users.map((item) => <option key={item.id} value={item.id}>{item.email}</option>)}
            </select>
          </FormField>
          <FormField label="通道">
            <select value={editAccountChannelId} onChange={(event) => setEditAccountChannelId(event.target.value)}>
              <option value="">请选择通道</option>
              {channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </FormField>
          <FormField label="号池分组">
            <select value={editAccountPoolGroup} onChange={(event) => setEditAccountPoolGroup(event.target.value)}>
              <option value="">不指定分组</option>
              {poolGroups.map((item) => <option key={item.id ?? item.group_name} value={item.group_name}>{item.group_name}</option>)}
            </select>
          </FormField>
          <FormField label="路由标签" hint="多个用逗号分隔。">
            <input value={editAccountRouteTags} onChange={(event) => setEditAccountRouteTags(event.target.value)} placeholder="premium,fast" />
          </FormField>
          <details className="advanced-details wide">
            <summary>高级配置</summary>
            <div className="form-grid">
              <FormField label="代理">
                <select value={editAccountProxyId} onChange={(event) => setEditAccountProxyId(event.target.value)}>
                  <option value="">不使用代理</option>
                  {proxies.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
                </select>
              </FormField>
              <FormField label="优先级"><input value={editAccountPriority} onChange={(event) => setEditAccountPriority(event.target.value)} inputMode="numeric" placeholder="100" /></FormField>
              <FormField label="最大并发"><input value={editAccountMaxConcurrency} onChange={(event) => setEditAccountMaxConcurrency(event.target.value)} inputMode="numeric" placeholder="10" /></FormField>
              <FormField label="新密钥" hint="留空表示不修改。"><input value={editAccountAPIKey} onChange={(event) => setEditAccountAPIKey(event.target.value)} placeholder="sk-..." type="password" /></FormField>
            </div>
          </details>
        </div>
      </ActionModal>

      <ActionModal
        open={poolModal === "batch"}
        title="批量操作"
        description={`已选择 ${selectedAccountIds.length} 个账号`}
        size="lg"
        onClose={() => setPoolModal(null)}
      >
        <div className="stack">
          <div className="grid three">
            <Metric label="已选择" value={String(selectedAccountIds.length)} />
            <Metric label="共享池" value={String(selectedRows.filter((row) => row.routing_mode === "pool").length)} />
            <Metric label="异常" value={String(selectedRows.filter((row) => row.status !== "active" || row.runtime_last_error).length)} />
          </div>
          <div className="actions panel-actions">
            <button onClick={() => batchAccounts("enable")} disabled={!selectedAccountIds.length}>启用</button>
            <button onClick={() => batchAccounts("disable")} disabled={!selectedAccountIds.length}>停用</button>
            <button onClick={() => batchAccounts("cooldown")} disabled={!selectedAccountIds.length}>冷却</button>
            <button onClick={() => batchAccounts("wakeup")} disabled={!selectedAccountIds.length}>解除冷却</button>
            <button onClick={() => createWakeupJob(true)} disabled={!selectedAccountIds.length}>执行唤醒任务</button>
            <button onClick={runSelectedHealthCheck} disabled={!selectedAccountIds.length}>健康检测</button>
            <button onClick={refreshSelectedQuota} disabled={!selectedAccountIds.length}>额度刷新</button>
            <button onClick={() => recomputeQuality(true)} disabled={!selectedAccountIds.length}>质量评分</button>
          </div>
          <div className="form-grid">
            <input value={batchGroup} onChange={(event) => setBatchGroup(event.target.value)} placeholder="分组名称" />
            <input value={batchTags} onChange={(event) => setBatchTags(event.target.value)} placeholder="路由标签，逗号分隔" />
            <button onClick={() => batchAccounts("set_group", { group: batchGroup })} disabled={!selectedAccountIds.length || !batchGroup.trim()}>设置分组</button>
            <button onClick={() => batchAccounts("set_tags", { tags: splitCSV(batchTags) })} disabled={!selectedAccountIds.length}>设置标签</button>
            <textarea value={batchMetadata} onChange={(event) => setBatchMetadata(event.target.value)} placeholder="元数据补丁 JSON" rows={4} />
            <button onClick={() => batchAccounts("patch_metadata", { metadata: parseJSONObject(batchMetadata) })} disabled={!selectedAccountIds.length}>合并元数据</button>
          </div>
          <DataTable rows={selectedRows} columns={["name", "provider_name", "channel_name", "pool_group", "route_tags", "status", "auth_status", "concurrency", "last_quota_status", "runtime_last_error"]} />
        </div>
      </ActionModal>

      <ActionModal
        open={poolModal === "quick_import"}
        title="快速粘贴密钥"
        size="lg"
        onClose={() => setPoolModal(null)}
        footer={<><button onClick={() => setPoolModal(null)}>取消</button><button className="primary" onClick={importPlainKeys} disabled={!quickImportKeys.trim()}>导入密钥</button></>}
      >
        <div className="stack">
          <div className="form-grid">
            <select value={importTemplate} onChange={(event) => { setImportTemplate(event.target.value); setImportPreview(null); }}>
              {importTemplates.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
            </select>
            <select value={importProviderId} onChange={(event) => setImportProviderId(event.target.value)}>
              <option value="">默认供应商</option>
              {providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
            <select value={importChannelId} onChange={(event) => { const nextChannelId = event.target.value; setImportChannelId(nextChannelId); const channel = channels.find((item) => item.id === nextChannelId); if (channel?.provider_id) setImportProviderId(channel.provider_id); }}>
              <option value="">默认通道</option>
              {channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
            <input value={importPoolGroup} onChange={(event) => setImportPoolGroup(event.target.value)} placeholder="默认分组" />
            <input value={importRouteTags} onChange={(event) => setImportRouteTags(event.target.value)} placeholder="默认标签，逗号分隔" />
            <input value={quickImportNamePrefix} onChange={(event) => setQuickImportNamePrefix(event.target.value)} placeholder="账号名前缀" />
            <select value={quickImportRoutingMode} onChange={(event) => setQuickImportRoutingMode(event.target.value)}>
              <option value="pool">共享池</option>
              <option value="byo">BYO</option>
            </select>
            <input value={quickImportOwnerUserId} onChange={(event) => setQuickImportOwnerUserId(event.target.value)} placeholder="BYO 用户 ID" disabled={quickImportRoutingMode !== "byo"} />
            <input value={quickImportPriority} onChange={(event) => setQuickImportPriority(event.target.value)} placeholder="优先级" />
            <input value={quickImportMaxConcurrency} onChange={(event) => setQuickImportMaxConcurrency(event.target.value)} placeholder="最大并发" />
          </div>
          <textarea value={quickImportKeys} onChange={(event) => setQuickImportKeys(event.target.value)} placeholder="sk-...\nsk-..." rows={8} />
        </div>
      </ActionModal>

      <ActionModal
        open={poolModal === "advanced_import"}
        title="JSON 导入导出"
        size="xl"
        onClose={() => setPoolModal(null)}
      >
        <div className="stack">
          <div className="form-grid">
            <select value={importTemplate} onChange={(event) => { setImportTemplate(event.target.value); setImportPreview(null); }}>
              {importTemplates.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
            </select>
            <select value={importProviderId} onChange={(event) => setImportProviderId(event.target.value)}>
              <option value="">默认供应商</option>
              {providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
            <select value={importChannelId} onChange={(event) => { const nextChannelId = event.target.value; setImportChannelId(nextChannelId); const channel = channels.find((item) => item.id === nextChannelId); if (channel?.provider_id) setImportProviderId(channel.provider_id); }}>
              <option value="">默认通道</option>
              {channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
            <input value={importPoolGroup} onChange={(event) => setImportPoolGroup(event.target.value)} placeholder="默认分组" />
            <input value={importRouteTags} onChange={(event) => setImportRouteTags(event.target.value)} placeholder="默认标签，逗号分隔" />
            <textarea value={importDefaultMetadata} onChange={(event) => setImportDefaultMetadata(event.target.value)} placeholder="默认元数据 JSON" rows={3} />
          </div>
          <div className="form-grid relaxed">
            <div className="stack compact">
              <div className="actions">
                <button onClick={exportAccounts}>生成导出 JSON</button>
              </div>
              <textarea value={exportText} onChange={(event) => setExportText(event.target.value)} placeholder="导出结果" rows={12} />
            </div>
            <div className="stack compact">
              <div className="actions">
                <button onClick={applyImportTemplateSample}>填入模板示例</button>
                <button onClick={previewImportAccounts} disabled={!importText.trim()}>预览导入</button>
                <button className="primary" onClick={importAccounts} disabled={!importText.trim() || Number(importPreviewSummary.invalid_count ?? 0) > 0}>提交导入</button>
              </div>
              <textarea value={importText} onChange={(event) => setImportText(event.target.value)} placeholder='[{"provider_id":"...","channel_id":"...","name":"...","api_key":"...","metadata":{"pool_group":"premium","route_tags":["premium"]}}]' rows={12} />
            </div>
          </div>
          <div className="grid three">
            <Metric label="预览总数" value={String(importPreviewSummary.total_count ?? 0)} />
            <Metric label="有效" value={String(importPreviewSummary.valid_count ?? 0)} />
            <Metric label="无效" value={String(importPreviewSummary.invalid_count ?? 0)} />
          </div>
          <DataTable rows={importPreviewRows} columns={["index", "template_label", "name", "provider_id", "channel_id", "routing_mode", "auth_mode", "credential_present", "has_auth_state", "valid", "errors", "warnings", "metadata"]} />
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminUpstream({ requestedTab }: { requestedTab?: string }) {
  const [tab, setTab] = useState<UpstreamTab>("channels");
  const [providers, setProviders] = useState<any[]>([]);
  const [providerClients, setProviderClients] = useState<any[]>([]);
  const [channels, setChannels] = useState<any[]>([]);
  const [accounts, setAccounts] = useState<any[]>([]);
  const [channelTests, setChannelTests] = useState<any[]>([]);
  const [modelSyncJobs, setModelSyncJobs] = useState<any[]>([]);
  const [models, setModels] = useState<any[]>([]);
  const [users, setUsers] = useState<any[]>([]);
  const [routingMode, setRoutingMode] = useState("pool");
  const [ownerUserId, setOwnerUserId] = useState("");
  const [authMode, setAuthMode] = useState("api_key");
  const [proxies, setProxies] = useState<any[]>([]);
  const [quotaWindows, setQuotaWindows] = useState<any[]>([]);
  const [providerName, setProviderName] = useState("OpenAI 兼容");
  const [providerType, setProviderType] = useState("openai_compatible");
  const [providerStatus, setProviderStatus] = useState("active");
  const [providerMetadata, setProviderMetadata] = useState("{}");
  const [providerId, setProviderId] = useState("");
  const [providerClientId, setProviderClientId] = useState("");
  const [channelId, setChannelId] = useState("");
  const [proxyId, setProxyId] = useState("");
  const [channelName, setChannelName] = useState("");
  const [baseUrl, setBaseUrl] = useState("https://api.openai.com");
  const [channelStatus, setChannelStatus] = useState("active");
  const [channelMetadata, setChannelMetadata] = useState("{}");
  const [channelPriority, setChannelPriority] = useState("100");
  const [channelWeight, setChannelWeight] = useState("1");
  const [channelTimeout, setChannelTimeout] = useState("120");
  const [accountName, setAccountName] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [upstreamAccountMetadata, setUpstreamAccountMetadata] = useState("{}");
  const [clientName, setClientName] = useState("");
  const [clientType, setClientType] = useState("api_key");
  const [clientStatus, setClientStatus] = useState("active");
  const [clientSecret, setClientSecret] = useState("");
  const [clientMetadata, setClientMetadata] = useState("{}");
  const [quotaAccountId, setQuotaAccountId] = useState("");
  const [quotaType, setQuotaType] = useState("requests");
  const [quotaRemaining, setQuotaRemaining] = useState("100");
  const [quotaMetadata, setQuotaMetadata] = useState("{}");
  const [abilityModel, setAbilityModel] = useState("");
  const [abilityEndpoint, setAbilityEndpoint] = useState("chat");
  const [upstreamModel, setUpstreamModel] = useState("");
  const [abilityPriority, setAbilityPriority] = useState("100");
  const [abilityWeight, setAbilityWeight] = useState("1");
  const [retryPriority, setRetryPriority] = useState("100");
  const [abilityTransform, setAbilityTransform] = useState(`{"mode":"native","lossless":true}`);
  const [route, setRoute] = useState<any>(null);
  const [testTargetPath, setTestTargetPath] = useState("/models");
  const [testAccountId, setTestAccountId] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [statusMapFrom, setStatusMapFrom] = useState("529");
  const [statusMapTo, setStatusMapTo] = useState("429");
  const [retryableStatuses, setRetryableStatuses] = useState("408,429,500,502,503,504");
  const [nonRetryableStatuses, setNonRetryableStatuses] = useState("400,401,403");
  const [circuitStatus, setCircuitStatus] = useState("429");
  const [circuitSeconds, setCircuitSeconds] = useState("600");
  const [headerName, setHeaderName] = useState("X-Trace");
  const [headerValue, setHeaderValue] = useState("{request_header:X-Trace-Id}");
  const [passHeaders, setPassHeaders] = useState("X-Request-Id,traceparent,tracestate");
  const [systemPromptMode, setSystemPromptMode] = useState("if_absent");
  const [systemPromptText, setSystemPromptText] = useState("");
  const [paramOp, setParamOp] = useState("set");
  const [paramPath, setParamPath] = useState("stream_options.include_usage");
  const [paramFrom, setParamFrom] = useState("");
  const [paramTo, setParamTo] = useState("");
  const [paramValue, setParamValue] = useState("true");
  const [previewBody, setPreviewBody] = useState(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`);
  const [previewHeaders, setPreviewHeaders] = useState(`{"X-Trace-Id":"trace-1"}`);
  const [previewResult, setPreviewResult] = useState<any>(null);
  const [upstreamModal, setUpstreamModal] = useState<null | "channel" | "provider" | "client" | "sync" | "test">(null);

  async function load() {
    setProviders(await adminApi.request<any[]>("/api/admin/v1/providers"));
    setProviderClients(await adminApi.request<any[]>("/api/admin/v1/provider-clients"));
    setChannels(await adminApi.request<any[]>("/api/admin/v1/channels"));
    setAccounts(await adminApi.request<any[]>("/api/admin/v1/accounts"));
    setProxies(await adminApi.request<any[]>("/api/admin/v1/proxies"));
    setQuotaWindows(await adminApi.request<any[]>("/api/admin/v1/account-quota-windows"));
    setUsers(await adminApi.request<any[]>("/api/admin/v1/users?limit=200"));
    setModels(await adminApi.request<any[]>("/api/admin/v1/models"));
    setChannelTests(await adminApi.request<any[]>("/api/admin/v1/channel-tests?limit=80"));
    setModelSyncJobs(await adminApi.request<any[]>("/api/admin/v1/model-sync-jobs?limit=80"));
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  useEffect(() => {
    if (requestedTab && ["channels", "providers", "clients", "sync", "tests"].includes(requestedTab)) {
      setTab(requestedTab as UpstreamTab);
    }
  }, [requestedTab]);

  async function runAction(action: () => Promise<void | boolean>, success = "") {
    setError("");
    setMessage("");
    try {
      const changed = await action();
      if (success && changed !== false) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function createProvider() {
    const data = await adminApi.request<{ id: string }>("/api/admin/v1/providers", { method: "POST", body: JSON.stringify({ name: providerName, provider_type: providerType, status: providerStatus, metadata: parseJSONObject(providerMetadata) }) });
    setProviderId(data.id);
    await load();
    setUpstreamModal(null);
  }

  async function saveProvider() {
    await adminApi.request(`/api/admin/v1/providers/${providerId}`, { method: "PATCH", body: JSON.stringify({ name: providerName, provider_type: providerType, status: providerStatus, metadata: parseJSONObject(providerMetadata) }) });
    await load();
    setUpstreamModal(null);
  }

  async function createProviderClient() {
    await adminApi.request("/api/admin/v1/provider-clients", { method: "POST", body: JSON.stringify({ provider_id: providerId, name: clientName, client_type: clientType, status: clientStatus, secret: clientSecret, metadata: parseJSONObject(clientMetadata) }) });
    setClientSecret("");
    await load();
    setUpstreamModal(null);
  }

  async function saveProviderClient() {
    const body: Record<string, unknown> = { name: clientName, client_type: clientType, status: clientStatus, metadata: parseJSONObject(clientMetadata) };
    if (clientSecret.trim()) body.secret = clientSecret;
    await adminApi.request(`/api/admin/v1/provider-clients/${providerClientId}`, { method: "PATCH", body: JSON.stringify(body) });
    setClientSecret("");
    await load();
    setUpstreamModal(null);
  }

  async function createChannel() {
    const data = await adminApi.request<{ id: string }>("/api/admin/v1/channels", { method: "POST", body: JSON.stringify({ provider_id: providerId, proxy_id: proxyId, name: channelName, base_url: baseUrl, status: channelStatus, priority: Number(channelPriority), weight: Number(channelWeight), timeout_seconds: Number(channelTimeout), metadata: parseJSONObject(channelMetadata), abilities: [{ model_name: abilityModel, endpoint: abilityEndpoint, upstream_model: upstreamModel || abilityModel, transform_capability: parseJSONObject(abilityTransform), priority: Number(abilityPriority), weight: Number(abilityWeight), retry_priority: Number(retryPriority) }] }) });
    setChannelId(data.id);
    await load();
    setUpstreamModal(null);
  }

  async function saveChannel() {
    await adminApi.request(`/api/admin/v1/channels/${channelId}`, { method: "PATCH", body: JSON.stringify({ name: channelName, base_url: baseUrl, status: channelStatus, priority: Number(channelPriority), weight: Number(channelWeight), timeout_seconds: Number(channelTimeout), proxy_id: proxyId, metadata: parseJSONObject(channelMetadata), abilities: [{ model_name: abilityModel, endpoint: abilityEndpoint, upstream_model: upstreamModel || abilityModel, transform_capability: parseJSONObject(abilityTransform), priority: Number(abilityPriority), weight: Number(abilityWeight), retry_priority: Number(retryPriority) }] }) });
    await load();
    setUpstreamModal(null);
  }

  async function createAccount() {
    const accountProviderId = providerId || channels.find((item) => item.id === channelId)?.provider_id || "";
    await adminApi.request("/api/admin/v1/accounts", { method: "POST", body: JSON.stringify({ provider_id: accountProviderId, channel_id: channelId, proxy_id: proxyId, name: accountName, api_key: apiKey, routing_mode: routingMode, owner_user_id: routingMode === "byo" ? ownerUserId : "", auth_mode: authMode, metadata: parseJSONObject(upstreamAccountMetadata) }) });
    setApiKey("");
    await load();
    setUpstreamModal(null);
  }

  async function createQuotaWindow() {
    await adminApi.request("/api/admin/v1/account-quota-windows", { method: "POST", body: JSON.stringify({ account_id: quotaAccountId, window_type: quotaType, remaining: quotaRemaining, metadata: parseJSONObject(quotaMetadata) }) });
    await load();
    setUpstreamModal(null);
  }

  async function explain() {
    const query = new URLSearchParams({ model: abilityModel, endpoint: abilityEndpoint, routing_mode: routingMode });
    if (ownerUserId) query.set("user_id", ownerUserId);
    setRoute(await adminApi.request(`/api/admin/v1/runtime/route-explain?${query.toString()}`));
    setUpstreamModal(null);
  }

  async function patch(path: string, id: string, body: Record<string, unknown>) {
    await adminApi.request(`${path}/${id}`, { method: "PATCH", body: JSON.stringify(body) });
    await load();
  }

  function selectProvider(row: any) {
    setProviderId(row.id);
    setProviderName(row.name ?? "");
    setProviderType(row.type_or_url ?? "openai_compatible");
    setProviderStatus(row.status ?? "active");
    setProviderMetadata(jsonText(row.metadata));
    setUpstreamModal("provider");
  }

  function selectProviderClient(row: any) {
    setProviderClientId(row.id);
    setProviderId(row.provider_id ?? "");
    setClientName(row.name ?? "");
    setClientType(row.client_type ?? "api_key");
    setClientStatus(row.status ?? "active");
    setClientMetadata(jsonText(row.metadata));
    setClientSecret("");
    setUpstreamModal("client");
  }

  function selectChannel(row: any) {
    const ability = firstAbility(row);
    setChannelId(row.id);
    setProviderId(row.provider_id ?? "");
    setProxyId(row.proxy_id ?? "");
    setChannelName(row.name ?? "");
    setBaseUrl(row.base_url ?? "");
    setChannelStatus(row.status ?? "active");
    setChannelPriority(String(row.priority ?? 100));
    setChannelWeight(String(row.weight ?? 1));
    setChannelTimeout(String(row.timeout_seconds ?? 120));
    setChannelMetadata(jsonText(row.metadata));
    setAbilityModel(ability?.model_name ?? "");
    setAbilityEndpoint(ability?.endpoint ?? "chat");
    setUpstreamModel(ability?.upstream_model ?? "");
    setAbilityPriority(String(ability?.priority ?? 100));
    setAbilityWeight(String(ability?.weight ?? 1));
    setRetryPriority(String(ability?.retry_priority ?? 100));
    setAbilityTransform(jsonText(ability?.transform_capability ?? { mode: "native", lossless: true }));
    setUpstreamModal("channel");
  }

  function selectChannelForTest(nextChannelId: string) {
    setChannelId(nextChannelId);
    const channel = channels.find((item) => item.id === nextChannelId);
    const ability = firstAbility(channel);
    setAbilityModel(ability?.model_name ?? "");
    setAbilityEndpoint(ability?.endpoint ?? "chat");
    const account = accounts.find((item) => item.channel_id === nextChannelId && item.status === "active")
      ?? accounts.find((item) => item.channel_id === nextChannelId);
    setTestAccountId(account?.id ?? "");
  }

  function openChannelTest() {
    selectChannelForTest(channelId || channels[0]?.id || "");
    setUpstreamModal("test");
  }

  async function runChannelTest() {
    if (!testAccountId) throw new Error("请选择一个绑定到该通道的上游账号再检测。");
    await adminApi.request("/api/admin/v1/channel-tests", {
      method: "POST",
      body: JSON.stringify({ channel_id: channelId, account_id: testAccountId, model_name: abilityModel, endpoint: abilityEndpoint, target_path: testTargetPath }),
    });
    await load();
    setUpstreamModal(null);
  }

  async function syncChannelModels(targetChannelId = channelId) {
    const result = await adminApi.request<any>(`/api/admin/v1/channels/${targetChannelId}/model-sync`, { method: "POST" });
    setMessage(`同步完成：发现 ${result.discovered_count ?? 0} 个，更新 ${result.updated_count ?? 0} 个`);
    await load();
    setUpstreamModal(null);
  }

  async function syncActiveChannelModels() {
    const result = await adminApi.request<any>("/api/admin/v1/model-sync-jobs", { method: "POST", body: JSON.stringify({}) });
    setMessage(`批量同步完成：成功 ${result.success_count ?? 0}，失败 ${result.failed_count ?? 0}，发现 ${result.discovered_count ?? 0} 个，更新 ${result.updated_count ?? 0} 个`);
    await load();
    setUpstreamModal(null);
  }

  function mergeChannelMetadataPatch(patch: Record<string, unknown>) {
    setError("");
    try {
      const next = deepMergePlainObjects(parseJSONObject(channelMetadata), patch);
      setChannelMetadata(jsonText(next));
    } catch (err) {
      setError(err instanceof Error ? err.message : "元数据 JSON 无效。");
    }
  }

  function addStatusMapping() {
    if (!statusMapFrom.trim() || !statusMapTo.trim()) return;
    mergeChannelMetadataPatch({ status_code_mapping: { [statusMapFrom.trim()]: Number(statusMapTo) } });
  }

  function applyRetryConfig() {
    const retry: Record<string, unknown> = {};
    const retryable = splitCSV(retryableStatuses).map(Number).filter(Number.isFinite);
    const nonRetryable = splitCSV(nonRetryableStatuses).map(Number).filter(Number.isFinite);
    if (retryable.length) retry.retryable_statuses = retryable;
    if (nonRetryable.length) retry.non_retryable_statuses = nonRetryable;
    mergeChannelMetadataPatch({ retry });
  }

  function addCircuitStatusRule() {
    if (!circuitStatus.trim() || !circuitSeconds.trim()) return;
    mergeChannelMetadataPatch({ circuit_breaker: { status_open_seconds: { [circuitStatus.trim()]: Number(circuitSeconds) } } });
  }

  function addHeaderSetRule() {
    if (!headerName.trim()) return;
    mergeChannelMetadataPatch({ request_headers: { set: { [headerName.trim()]: headerValue } } });
  }

  function applyHeaderPassRule() {
    const pass = splitCSV(passHeaders);
    if (!pass.length) return;
    mergeChannelMetadataPatch({ request_headers: { pass } });
  }

  function applySystemPromptRule() {
    if (!systemPromptText.trim()) return;
    mergeChannelMetadataPatch({ system_prompt: { mode: systemPromptMode, text: systemPromptText } });
  }

  function addParamOperation() {
    try {
      const operation: Record<string, unknown> = { op: paramOp };
      if (["copy", "move", "copy_header", "move_header"].includes(paramOp)) {
        operation.from = paramFrom;
        operation.to = paramTo;
      } else if (paramOp === "pass_headers") {
        operation.value = splitCSV(paramValue || paramPath);
      } else {
        operation.path = paramPath;
        if (!["delete", "delete_header"].includes(paramOp)) operation.value = parseLooseJSONValue(paramValue);
      }
      if (paramOp === "regex_replace" || paramOp === "replace") {
        operation.from = paramFrom;
        operation.to = paramTo;
      }
      const metadata = parseJSONObject(channelMetadata);
      const paramOverride = metadata.param_override && typeof metadata.param_override === "object" && !Array.isArray(metadata.param_override) ? metadata.param_override as Record<string, unknown> : {};
      const operations = Array.isArray(paramOverride.operations) ? paramOverride.operations : [];
      mergeChannelMetadataPatch({ param_override: { operations: [...operations, operation] } });
    } catch (err) {
      setError(err instanceof Error ? err.message : "元数据 JSON 无效。");
    }
  }

  async function previewReverseProxyConfig() {
    const result = await adminApi.request<any>("/api/admin/v1/reverse-proxy/param-override-preview", {
      method: "POST",
      body: JSON.stringify({
        provider_type: providerType,
        upstream_model: upstreamModel || abilityModel,
        content_type: "application/json",
        channel_metadata: parseJSONObject(channelMetadata),
        body: parseLooseJSONValue(previewBody),
        headers: parseJSONObject(previewHeaders),
      }),
    });
    setPreviewResult(result);
  }

  function openNewProvider() {
    setProviderId("");
    setProviderName("OpenAI 兼容");
    setProviderType("openai_compatible");
    setProviderStatus("active");
    setProviderMetadata("{}");
    setUpstreamModal("provider");
  }

  function openNewProviderClient() {
    setProviderClientId("");
    setClientName("");
    setClientType("api_key");
    setClientStatus("active");
    setClientSecret("");
    setClientMetadata("{}");
    setUpstreamModal("client");
  }

  function openNewChannel() {
    setChannelId("");
    setChannelName("");
    setBaseUrl("https://api.openai.com");
    setChannelStatus("active");
    setChannelPriority("100");
    setChannelWeight("1");
    setChannelTimeout("120");
    setChannelMetadata("{}");
    setAbilityModel("");
    setAbilityEndpoint("chat");
    setUpstreamModel("");
    setAbilityPriority("100");
    setAbilityWeight("1");
    setRetryPriority("100");
    setAbilityTransform(`{"mode":"native","lossless":true}`);
    setPreviewResult(null);
    setUpstreamModal("channel");
  }

  const providerNames = new Map(providers.map((item) => [item.id, item.name]));
  const proxyNames = new Map(proxies.map((item) => [item.id, item.name]));
  const channelRows = channels.map((row) => {
    const ability = firstAbility(row);
    return {
      ...row,
      provider_name: providerNames.get(row.provider_id) ?? row.provider_id,
      proxy_name: row.proxy_id ? proxyNames.get(row.proxy_id) ?? row.proxy_id : "不使用代理",
      model_name: ability?.model_name ?? "-",
      upstream_model: ability?.upstream_model ?? ability?.model_name ?? "-",
      endpoint: ability?.endpoint ?? "-",
    };
  });
  const providerClientRows = providerClients.map((row) => ({
    ...row,
    provider_name: providerNames.get(row.provider_id) ?? row.provider_id,
  }));

  return (
    <div className="stack">
      <PageNotice error={error} message={message} />
      <SectionTabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "channels", label: "通道", count: channels.length },
          { id: "providers", label: "供应商", count: providers.length },
          { id: "clients", label: "客户端", count: providerClients.length },
          { id: "sync", label: "模型同步", count: modelSyncJobs.length },
          { id: "tests", label: "检测", count: channelTests.length },
        ]}
      />
      {tab === "channels" && (
        <section className="panel">
          <SectionHeader title="通道" description="一个通道就是一个可访问的上游 API 地址，可选代理，并声明它能跑的模型。" action={<button onClick={openNewChannel}>新建通道</button>} />
          <DataTable rows={channelRows} columns={["name", "provider_name", "base_url", "proxy_name", "status", "priority", "weight", "timeout_seconds", "model_name", "upstream_model", "endpoint"]} action={(row) => <div className="actions"><button onClick={() => selectChannel(row)}>编辑</button><button onClick={() => { setChannelId(row.id); void runAction(() => syncChannelModels(row.id)); }}>同步模型</button><button onClick={() => runAction(() => patch("/api/admin/v1/channels", row.id, { status: row.status === "active" ? "disabled" : "active" }), "状态已更新")}>{row.status === "active" ? "停用" : "启用"}</button></div>} />
        </section>
      )}
      {tab === "providers" && (
        <section className="panel">
          <SectionHeader title="供应商" description="供应商只表示上游平台类型，例如 OpenAI 兼容、Anthropic 或 Gemini。" action={<button onClick={openNewProvider}>新建供应商</button>} />
          <DataTable rows={providers} columns={["name", "type_or_url", "status", "created_at"]} action={(row) => <div className="actions"><button onClick={() => selectProvider(row)}>编辑</button><button onClick={() => runAction(() => patch("/api/admin/v1/providers", row.id, { status: row.status === "active" ? "disabled" : "active" }), "状态已更新")}>{row.status === "active" ? "停用" : "启用"}</button></div>} />
        </section>
      )}
      {tab === "clients" && (
        <section className="panel">
          <SectionHeader title="认证客户端" description="只在需要 OAuth 或独立客户端密钥时配置；普通 API Key 账号在号池里管理。" action={<button onClick={openNewProviderClient}>新建客户端</button>} />
          <DataTable rows={providerClientRows} columns={["provider_name", "name", "client_type", "status", "created_at"]} action={(row) => <div className="actions"><button onClick={() => selectProviderClient(row)}>编辑</button><button onClick={() => runAction(() => patch("/api/admin/v1/provider-clients", row.id, { status: row.status === "active" ? "disabled" : "active" }), "状态已更新")}>{row.status === "active" ? "停用" : "启用"}</button></div>} />
        </section>
      )}
      {tab === "sync" && (
        <section className="panel">
          <SectionHeader title="模型同步任务" description="手动同步通道模型，查看发现数、更新数和失败详情。" action={<div className="actions"><button onClick={() => setUpstreamModal("sync")}>同步模型</button><button onClick={() => runAction(load)}>刷新记录</button></div>} />
          <DataTable rows={modelSyncJobs} columns={["channel_name", "status", "discovered_count", "updated_count", "error_message", "started_at", "finished_at", "created_at"]} />
        </section>
      )}
      {tab === "tests" && (
        <section className="panel">
          <SectionHeader title="渠道检测" description="对通道执行健康检测，查看最近检测结果。" action={<div className="actions"><button onClick={openChannelTest}>检测通道</button><button onClick={() => runAction(load)}>刷新记录</button></div>} />
          <DataTable rows={channelTests} columns={["channel_id", "account_id", "model_name", "endpoint", "status", "latency_ms", "upstream_status", "error_message", "tested_at"]} />
        </section>
      )}
      <ActionModal
        open={upstreamModal === "channel"}
        title={channelId ? "编辑通道" : "新建通道"}
        size="xl"
        onClose={() => setUpstreamModal(null)}
        footer={<><button onClick={() => setUpstreamModal(null)}>取消</button>{channelId ? <button className="primary" onClick={() => runAction(saveChannel, "通道已保存")}>保存</button> : <button className="primary" onClick={() => runAction(createChannel, "通道已创建")} disabled={!providerId}>创建</button>}</>}
      >
        <div className="stack">
          <div className="form-grid">
            <FormField label="所属供应商" hint="先选这个通道属于哪个上游平台。">
              <select value={providerId} onChange={(event) => setProviderId(event.target.value)}><option value="">请选择供应商</option>{providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
            </FormField>
            <FormField label="代理" hint="不需要代理就保持“不使用代理”。">
              <select value={proxyId} onChange={(event) => setProxyId(event.target.value)}><option value="">不使用代理</option>{proxies.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
            </FormField>
            <FormField label="通道名称" hint="后台识别用，例如 OpenAI 官方主通道。">
              <input value={channelName} onChange={(event) => setChannelName(event.target.value)} placeholder="OpenAI 官方主通道" />
            </FormField>
            <FormField label="基础地址" hint="上游 API 的 base URL，不带具体模型路径。">
              <input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.openai.com" />
            </FormField>
            <FormField label="状态" hint="启用后可被路由使用。">
              <select value={channelStatus} onChange={(event) => setChannelStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option><option value="cooldown">冷却</option></select>
            </FormField>
            <FormField label="超时秒数" hint="普通上游建议 60-120 秒。">
              <input value={channelTimeout} onChange={(event) => setChannelTimeout(event.target.value)} inputMode="numeric" placeholder="120" />
            </FormField>
            <FormField label="对外模型名" hint="用户请求时看到和填写的模型名。">
              <input value={abilityModel} onChange={(event) => setAbilityModel(event.target.value)} placeholder="gpt-4o-mini" list="admin-upstream-models" />
            </FormField>
            <FormField label="上游模型名" hint="上游真实模型名；相同可留空。">
              <input value={upstreamModel} onChange={(event) => setUpstreamModel(event.target.value)} placeholder="留空则同对外模型名" />
            </FormField>
            <FormField label="端点类型" hint="决定走聊天、响应、向量等哪类接口。">
              <select value={abilityEndpoint} onChange={(event) => setAbilityEndpoint(event.target.value)}>
                <option value="chat">聊天</option>
                <option value="responses">Responses</option>
                <option value="embeddings">向量</option>
                <option value="images">图片</option>
                <option value="audio">音频</option>
              </select>
            </FormField>
          </div>
          <datalist id="admin-upstream-models">{models.map((item) => <option key={item.model_name ?? item.id} value={item.model_name ?? item.name ?? ""} />)}</datalist>
          <details className="advanced-details">
            <summary>调度参数</summary>
            <div className="form-grid">
              <FormField label="通道优先级" hint="数字越小越优先；不确定就用 100。">
                <input value={channelPriority} onChange={(event) => setChannelPriority(event.target.value)} inputMode="numeric" placeholder="100" />
              </FormField>
              <FormField label="通道权重" hint="同优先级下按权重分配；不确定就用 1。">
                <input value={channelWeight} onChange={(event) => setChannelWeight(event.target.value)} inputMode="numeric" placeholder="1" />
              </FormField>
              <FormField label="重试优先级" hint="重试时的候选顺序；不确定就用 100。">
                <input value={retryPriority} onChange={(event) => setRetryPriority(event.target.value)} inputMode="numeric" placeholder="100" />
              </FormField>
              <FormField label="模型优先级" hint="同通道多模型能力时使用。">
                <input value={abilityPriority} onChange={(event) => setAbilityPriority(event.target.value)} inputMode="numeric" placeholder="100" />
              </FormField>
              <FormField label="模型权重" hint="同优先级下按权重分配。">
                <input value={abilityWeight} onChange={(event) => setAbilityWeight(event.target.value)} inputMode="numeric" placeholder="1" />
              </FormField>
            </div>
          </details>
          <details className="advanced-details">
            <summary>高级反代规则</summary>
            <div className="form-grid relaxed">
              <FormField label="上游状态码"><input value={statusMapFrom} onChange={(event) => setStatusMapFrom(event.target.value)} placeholder="529" /></FormField>
              <FormField label="返回给用户"><input value={statusMapTo} onChange={(event) => setStatusMapTo(event.target.value)} placeholder="429" /></FormField>
              <button onClick={addStatusMapping}>添加状态映射</button>
              <FormField label="可重试状态码" hint="多个用逗号分隔。"><input value={retryableStatuses} onChange={(event) => setRetryableStatuses(event.target.value)} placeholder="408,429,500,502,503,504" /></FormField>
              <FormField label="不可重试状态码" hint="多个用逗号分隔。"><input value={nonRetryableStatuses} onChange={(event) => setNonRetryableStatuses(event.target.value)} placeholder="400,401,403" /></FormField>
              <button onClick={applyRetryConfig}>保存重试策略</button>
              <FormField label="熔断状态码"><input value={circuitStatus} onChange={(event) => setCircuitStatus(event.target.value)} placeholder="429" /></FormField>
              <FormField label="冷却秒数"><input value={circuitSeconds} onChange={(event) => setCircuitSeconds(event.target.value)} inputMode="numeric" placeholder="600" /></FormField>
              <button onClick={addCircuitStatusRule}>添加熔断规则</button>
              <FormField label="请求头名称"><input value={headerName} onChange={(event) => setHeaderName(event.target.value)} placeholder="X-Trace" /></FormField>
              <FormField label="请求头值"><input value={headerValue} onChange={(event) => setHeaderValue(event.target.value)} placeholder="{request_header:X-Trace-Id}" /></FormField>
              <button onClick={addHeaderSetRule}>添加请求头</button>
              <FormField label="透传请求头" hint="多个用逗号分隔。"><input value={passHeaders} onChange={(event) => setPassHeaders(event.target.value)} placeholder="X-Request-Id,traceparent" /></FormField>
              <button onClick={applyHeaderPassRule}>保存透传规则</button>
              <FormField label="System Prompt 模式"><select value={systemPromptMode} onChange={(event) => setSystemPromptMode(event.target.value)}><option value="if_absent">缺失时写入</option><option value="prepend">前置合并</option><option value="replace">替换</option></select></FormField>
              <FormField label="System Prompt 文本"><input value={systemPromptText} onChange={(event) => setSystemPromptText(event.target.value)} placeholder="只在需要统一提示词时填写" /></FormField>
              <button onClick={applySystemPromptRule}>保存提示词规则</button>
              <FormField label="参数动作"><select value={paramOp} onChange={(event) => setParamOp(event.target.value)}>{paramOperationOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></FormField>
              <FormField label="目标字段"><input value={paramPath} onChange={(event) => setParamPath(event.target.value)} placeholder="stream_options.include_usage" /></FormField>
              <FormField label="来源字段"><input value={paramFrom} onChange={(event) => setParamFrom(event.target.value)} placeholder="需要复制或移动时填写" /></FormField>
              <FormField label="目标位置"><input value={paramTo} onChange={(event) => setParamTo(event.target.value)} placeholder="需要复制、移动或替换时填写" /></FormField>
              <FormField label="写入值"><input value={paramValue} onChange={(event) => setParamValue(event.target.value)} placeholder="true、文本或数字" /></FormField>
              <button onClick={addParamOperation}>添加参数规则</button>
            </div>
          </details>
        </div>
      </ActionModal>
      <ActionModal
        open={upstreamModal === "provider"}
        title={providerId ? "编辑供应商" : "新建供应商"}
        size="md"
        onClose={() => setUpstreamModal(null)}
        footer={<><button onClick={() => setUpstreamModal(null)}>取消</button>{providerId ? <button className="primary" onClick={() => runAction(saveProvider, "供应商已保存")}>保存</button> : <button className="primary" onClick={() => runAction(createProvider, "供应商已创建")}>创建</button>}</>}
      >
        <div className="form-grid">
          <FormField label="供应商名称" hint="给后台看的名称，例如 OpenAI 官方、Claude 代理、Gemini 主账号。">
            <input value={providerName} onChange={(event) => setProviderName(event.target.value)} placeholder="OpenAI 官方" />
          </FormField>
          <FormField label="平台类型" hint="决定协议适配方式；OpenAI 兼容适合大多数第三方中转。">
            <select value={providerType} onChange={(event) => setProviderType(event.target.value)}>{providerTypes.map((item) => <option key={item} value={item}>{providerTypeLabel(item)}</option>)}</select>
          </FormField>
          <FormField label="状态" hint="停用后，新通道不会继续使用这个供应商。">
            <select value={providerStatus} onChange={(event) => setProviderStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option></select>
          </FormField>
        </div>
      </ActionModal>
      <ActionModal
        open={upstreamModal === "client"}
        title={providerClientId ? "编辑客户端" : "新建客户端"}
        size="md"
        onClose={() => setUpstreamModal(null)}
        footer={<><button onClick={() => setUpstreamModal(null)}>取消</button>{providerClientId ? <button className="primary" onClick={() => runAction(saveProviderClient, "客户端已保存")} disabled={!clientName}>保存</button> : <button className="primary" onClick={() => runAction(createProviderClient, "客户端已创建")} disabled={!providerId || !clientName}>创建</button>}</>}
      >
        <div className="form-grid">
          <FormField label="所属供应商" hint="这个客户端属于哪个上游平台。">
            <select value={providerId} onChange={(event) => setProviderId(event.target.value)}><option value="">请选择供应商</option>{providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
          </FormField>
          <FormField label="客户端名称" hint="例如 OAuth Web Client、API Key Client。">
            <input value={clientName} onChange={(event) => setClientName(event.target.value)} placeholder="OAuth Web Client" />
          </FormField>
          <FormField label="认证方式" hint="普通 API Key 账号通常不需要新建客户端。">
            <select value={clientType} onChange={(event) => setClientType(event.target.value)}>
              <option value="api_key">API Key</option>
              <option value="oauth">OAuth</option>
              <option value="custom">自定义</option>
            </select>
          </FormField>
          <FormField label="状态">
            <select value={clientStatus} onChange={(event) => setClientStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option></select>
          </FormField>
          <FormField label="客户端密钥" hint="编辑已有客户端时留空表示不修改。">
            <input value={clientSecret} onChange={(event) => setClientSecret(event.target.value)} placeholder="密钥" type="password" />
          </FormField>
        </div>
      </ActionModal>
      <ActionModal
        open={upstreamModal === "sync"}
        title="同步模型"
        size="sm"
        onClose={() => setUpstreamModal(null)}
        footer={<><button onClick={() => setUpstreamModal(null)}>取消</button><button className="primary" onClick={() => runAction(syncChannelModels)} disabled={!channelId}>同步所选</button><button onClick={() => runAction(syncActiveChannelModels)} disabled={!channels.length}>同步全部活动通道</button></>}
      >
        <div className="form-grid single">
          <select value={channelId} onChange={(event) => setChannelId(event.target.value)}><option value="">选择通道</option>{channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
        </div>
      </ActionModal>
      <ActionModal
        open={upstreamModal === "test"}
        title="检测通道"
        size="sm"
        onClose={() => setUpstreamModal(null)}
        footer={<><button onClick={() => setUpstreamModal(null)}>取消</button><button className="primary" onClick={() => runAction(runChannelTest, "检测任务已完成")} disabled={!channelId || !testAccountId}>检测</button></>}
      >
        <div className="form-grid single">
          <FormField label="通道" hint="先选择要检测的上游地址。">
            <select value={channelId} onChange={(event) => selectChannelForTest(event.target.value)}><option value="">选择通道</option>{channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
          </FormField>
          <FormField label="检测账号" hint="检测会使用该账号的上游密钥或 OAuth token。">
            <select value={testAccountId} onChange={(event) => setTestAccountId(event.target.value)} disabled={!channelId}>
              <option value="">选择账号</option>
              {accounts.filter((item) => item.channel_id === channelId).map((item) => <option key={item.id} value={item.id}>{item.name}{item.status !== "active" ? ` (${item.status})` : ""}</option>)}
            </select>
          </FormField>
          <FormField label="模型" hint="默认使用通道声明的第一个模型。">
            <input value={abilityModel} onChange={(event) => setAbilityModel(event.target.value)} placeholder="模型名" />
          </FormField>
          <FormField label="端点">
            <input value={abilityEndpoint} onChange={(event) => setAbilityEndpoint(event.target.value)} placeholder="chat" />
          </FormField>
          <FormField label="检测路径" hint="默认 /models；填写模型和端点时会按供应商适配器生成真实探测请求。">
            <input value={testTargetPath} onChange={(event) => setTestTargetPath(event.target.value)} placeholder="/models" />
          </FormField>
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminProxies() {
  const [proxies, setProxies] = useState<any[]>([]);
  const [accounts, setAccounts] = useState<any[]>([]);
  const [tests, setTests] = useState<any[]>([]);
  const [proxyId, setProxyId] = useState("");
  const [proxyName, setProxyName] = useState("");
  const [proxyUrl, setProxyUrl] = useState("");
  const [proxyStatus, setProxyStatus] = useState("active");
  const [proxyMetadata, setProxyMetadata] = useState("{}");
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [protocolFilter, setProtocolFilter] = useState("");
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [targetUrl, setTargetUrl] = useState("https://example.com");
  const [importText, setImportText] = useState("");
  const [exportText, setExportText] = useState("");
  const [qualityReport, setQualityReport] = useState<any>(null);
  const [proxyModal, setProxyModal] = useState<null | "proxy" | "io" | "quality">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const query = new URLSearchParams({ limit: "500" });
    if (search.trim()) query.set("q", search.trim());
    if (statusFilter) query.set("status", statusFilter);
    if (protocolFilter) query.set("protocol", protocolFilter);
    const [nextProxies, nextAccounts] = await Promise.all([
      adminApi.request<any[]>(`/api/admin/v1/proxies?${query.toString()}`),
      adminApi.request<any[]>("/api/admin/v1/accounts"),
    ]);
    setProxies(nextProxies);
    setAccounts(nextAccounts);
    setTests(await adminApi.request<any[]>("/api/admin/v1/proxy-test-results?limit=80"));
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  async function runAction(action: () => Promise<void | boolean>, success = "") {
    setError("");
    setMessage("");
    try {
      const changed = await action();
      if (success && changed !== false) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function createProxy() {
    const data = await adminApi.request<{ id: string }>("/api/admin/v1/proxies", {
      method: "POST",
      body: JSON.stringify({ name: proxyName, proxy_url: proxyUrl, status: proxyStatus, metadata: parseJSONObject(proxyMetadata) }),
    });
    setProxyId(data.id);
    await load();
    setProxyModal(null);
  }

  async function saveProxy() {
    await adminApi.request(`/api/admin/v1/proxies/${proxyId}`, {
      method: "PATCH",
      body: JSON.stringify({ name: proxyName, proxy_url: proxyUrl, status: proxyStatus, metadata: parseJSONObject(proxyMetadata) }),
    });
    await load();
    setProxyModal(null);
  }

  async function patchProxy(id: string, body: Record<string, unknown>) {
    await adminApi.request(`/api/admin/v1/proxies/${id}`, { method: "PATCH", body: JSON.stringify(body) });
    await load();
  }

  async function deleteProxy(row: any): Promise<boolean> {
    const accountCount = Number(row.account_count ?? 0);
    const channelCount = Number(row.channel_count ?? 0);
    if (accountCount > 0 || channelCount > 0) {
      throw new Error(`该代理仍被 ${accountCount} 个账号、${channelCount} 个通道使用，先解绑再删除。`);
    }
    if (!window.confirm(`确认删除代理「${row.name ?? row.id}」？检测记录会一并删除。`)) return false;
    await adminApi.request(`/api/admin/v1/proxies/${row.id}`, { method: "DELETE" });
    setSelectedIds((current) => current.filter((id) => id !== row.id));
    if (proxyId === row.id) resetForm();
    if (qualityReport?.proxy_id === row.id) setQualityReport(null);
    await load();
    return true;
  }

  async function qualityCheck() {
    const results = await adminApi.request<any[]>("/api/admin/v1/proxies/quality", {
      method: "POST",
      body: JSON.stringify({ proxy_ids: selectedIds, target_url: targetUrl }),
    });
    if (results.length === 1) {
      setQualityReport(results[0]);
      setProxyModal("quality");
    }
    setMessage(`检测完成：${results.length} 个代理`);
    await load();
  }

  async function checkProxy(id: string) {
    const results = await adminApi.request<any[]>("/api/admin/v1/proxies/quality", {
      method: "POST",
      body: JSON.stringify({ proxy_ids: [id], target_url: targetUrl }),
    });
    const result = results[0];
    if (result) {
      setQualityReport(result);
      setProxyModal("quality");
      const location = [result.country, result.city].filter(Boolean).join(" · ");
      const problem = proxyQualityIssueText(result);
      setMessage(`检测完成：${result.score} 分 · ${result.grade}${result.exit_ip ? ` · ${result.exit_ip}${location ? ` · ${location}` : ""}` : ""}${problem ? ` · ${problem}` : ""}`);
    }
    await load();
  }

  async function batchStatus(status: "active" | "disabled") {
    await adminApi.request("/api/admin/v1/proxies/batch", {
      method: "POST",
      body: JSON.stringify({ proxy_ids: selectedIds, status }),
    });
    setSelectedIds([]);
    await load();
  }

  async function exportProxies() {
    const data = await adminApi.request<any>("/api/admin/v1/proxies/export?limit=500");
    setExportText(JSON.stringify(data.items ?? [], null, 2));
  }

  async function importProxies() {
    const parsed = JSON.parse(importText || "[]");
    const items = Array.isArray(parsed) ? parsed : parsed.items;
    await adminApi.request("/api/admin/v1/proxies/import", { method: "POST", body: JSON.stringify({ items }) });
    setImportText("");
    await load();
    setProxyModal(null);
  }

  function selectProxy(row: any) {
    setProxyId(row.id);
    setProxyName(row.name ?? "");
    setProxyUrl(row.proxy_url ?? "");
    setProxyStatus(row.status ?? "active");
    setProxyMetadata(jsonText(row.metadata));
    setMessage("已选择代理");
    setProxyModal("proxy");
  }

  function resetForm() {
    setProxyId("");
    setProxyName("");
    setProxyUrl("");
    setProxyStatus("active");
    setProxyMetadata("{}");
    setMessage("");
    setError("");
    setProxyModal(null);
  }

  function openNewProxy() {
    setProxyId("");
    setProxyName("");
    setProxyUrl("");
    setProxyStatus("active");
    setProxyMetadata("{}");
    setMessage("");
    setError("");
    setProxyModal("proxy");
  }

  function toggleSelected(id: string) {
    setSelectedIds((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
  }

  function openQualityReport(row: any) {
    const report = row.quality_report ?? {};
    setQualityReport({
      ...report,
      proxy_id: row.id,
      proxy_name: row.name,
      score: report.score ?? row.quality_score,
      grade: report.grade ?? row.quality_grade,
      summary: report.summary ?? row.quality_summary ?? "暂无检测详情，请先执行检测。",
      problem: report.problem ?? row.quality_problem ?? row.last_test_error,
      quality_status: report.quality_status ?? row.quality_status,
      exit_ip: report.exit_ip ?? row.exit_ip,
      country: report.country ?? row.country,
      country_code: report.country_code ?? row.country_code,
      region: report.region ?? row.region,
      city: report.city ?? row.city,
      items: Array.isArray(report.items) ? report.items : [],
    });
    setProxyModal("quality");
  }

  const accountCounts = new Map<string, number>();
  for (const account of accounts) {
    if (account.proxy_id) accountCounts.set(account.proxy_id, (accountCounts.get(account.proxy_id) ?? 0) + 1);
  }
  const proxyRows = proxies
    .map((proxy) => ({
      ...proxy,
      proxy_protocol: proxyProtocol(proxy.proxy_url),
      account_count: accountCounts.get(proxy.id) ?? 0,
      quality_problem: proxy.quality_problem || proxyQualityIssueText(proxy.quality_report),
    }))
    .filter((proxy) => {
      if (statusFilter && proxy.status !== statusFilter) return false;
      if (protocolFilter && proxy.proxy_protocol !== protocolFilter) return false;
      const keyword = search.trim().toLowerCase();
      if (!keyword) return true;
      return [proxy.name, proxy.proxy_url, proxy.status, proxy.proxy_protocol].some((value) => String(value ?? "").toLowerCase().includes(keyword));
    });
  const activeCount = proxies.filter((proxy) => proxy.status === "active").length;
  const disabledCount = proxies.filter((proxy) => proxy.status === "disabled").length;
  const inUseCount = Array.from(accountCounts.values()).filter((count) => count > 0).length;
  const failedTests = tests.filter((row) => row.status === "failed").length;
  const qualityProblem = proxyQualityIssueText(qualityReport);
  const qualityItemRows = proxyQualityItemRows(qualityReport);
  const testRows = tests.map((row) => ({
    ...row,
    quality_problem: row.quality_problem || proxyQualityIssueText(row.metadata),
  }));

  return (
    <div className="stack">
      <div className="proxy-metrics">
        <Metric label="代理总数" value={String(proxies.length)} />
        <Metric label="启用代理" value={String(activeCount)} />
        <Metric label="停用代理" value={String(disabledCount)} />
        <Metric label="被账号使用" value={String(inUseCount)} />
        <Metric label="近期失败检测" value={String(failedTests)} />
      </div>
      <PageNotice error={error} message={message} />
      <section className="panel">
        <SectionHeader
          title="代理池"
          description={`${proxyRows.length} 条结果，已选 ${selectedIds.length} 个。`}
          action={<div className="actions"><button onClick={openNewProxy}><Network size={14} />新建代理</button><button onClick={() => setProxyModal("io")}>导入导出</button><button onClick={() => runAction(load)}><RefreshCcw size={14} />刷新</button></div>}
        />
        <div className="proxy-toolbar">
          <div className="proxy-filter-grid">
            <input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索代理名称、地址或状态" />
            <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
              <option value="">任意状态</option>
              <option value="active">启用</option>
              <option value="disabled">停用</option>
            </select>
            <select value={protocolFilter} onChange={(event) => setProtocolFilter(event.target.value)}>
              <option value="">任意协议</option>
              <option value="http">HTTP</option>
              <option value="https">HTTPS</option>
              <option value="socks5">SOCKS5</option>
              <option value="direct">直连</option>
            </select>
            <input value={targetUrl} onChange={(event) => setTargetUrl(event.target.value)} placeholder="检测目标 URL" />
          </div>
          <div className="proxy-batch-actions">
            <span>{selectedIds.length ? `已选择 ${selectedIds.length} 个代理` : "未选择时检测全部启用代理"}</span>
            <button onClick={() => runAction(() => batchStatus("active"), "批量启用完成")} disabled={!selectedIds.length}>批量启用</button>
            <button onClick={() => runAction(() => batchStatus("disabled"), "批量停用完成")} disabled={!selectedIds.length}>批量停用</button>
            <button className="primary" onClick={() => runAction(qualityCheck)}>{selectedIds.length ? "检测选中" : "检测全部启用"}</button>
          </div>
        </div>
        <DataTable rows={proxyRows} columns={["name", "proxy_protocol", "proxy_url", "status", "exit_ip", "country", "quality_score", "quality_grade", "quality_status", "quality_problem", "last_test_latency_ms", "last_test_error", "quality_checked_at"]} action={(row) => (
          <div className="actions proxy-row-actions">
            <label className="proxy-select"><input type="checkbox" checked={selectedIds.includes(row.id)} onChange={() => toggleSelected(row.id)} /><span>选择</span></label>
            <button onClick={() => selectProxy(row)}>编辑</button>
            <button onClick={() => runAction(() => checkProxy(row.id))}>检测</button>
            <button onClick={() => openQualityReport(row)}>详情</button>
            <button onClick={() => runAction(() => patchProxy(row.id, { status: row.status === "active" ? "disabled" : "active" }), "状态已更新")}>{row.status === "active" ? "停用" : "启用"}</button>
            <button className="danger" onClick={() => runAction(() => deleteProxy(row), "代理已删除")}>删除</button>
          </div>
        )} />
      </section>
      <section className="panel">
        <SectionHeader title="检测记录" description="显示最近 80 条代理检测结果。" />
        <DataTable rows={testRows} columns={["proxy_id", "test_type", "target_url", "status", "latency_ms", "upstream_status", "exit_ip", "country", "quality_score", "quality_grade", "quality_status", "quality_problem", "error_message", "tested_at"]} />
      </section>
      <ActionModal
        open={proxyModal === "proxy"}
        title={proxyId ? "编辑代理" : "新建代理"}
        size="lg"
        onClose={() => setProxyModal(null)}
        footer={<><button onClick={() => setProxyModal(null)}>取消</button>{proxyId ? <button className="primary" onClick={() => runAction(saveProxy, "代理已保存")} disabled={!proxyId || !proxyName || !proxyUrl}>保存</button> : <button className="primary" onClick={() => runAction(createProxy, "代理已创建")} disabled={!proxyName || !proxyUrl}>创建</button>}</>}
      >
        <div className="proxy-editor-grid">
          <label className="proxy-field">
            <span>名称</span>
            <input value={proxyName} onChange={(event) => setProxyName(event.target.value)} placeholder="代理名称" />
          </label>
          <label className="proxy-field proxy-field-url">
            <span>代理地址</span>
            <input value={proxyUrl} onChange={(event) => setProxyUrl(event.target.value)} placeholder="http://proxy:8080 或 direct" />
          </label>
          <label className="proxy-field">
            <span>状态</span>
            <select value={proxyStatus} onChange={(event) => setProxyStatus(event.target.value)}>
              <option value="active">启用</option>
              <option value="disabled">停用</option>
            </select>
          </label>
          <label className="proxy-field proxy-field-wide">
            <span>元数据 JSON</span>
            <textarea value={proxyMetadata} onChange={(event) => setProxyMetadata(event.target.value)} placeholder="代理元数据 JSON" rows={5} />
          </label>
        </div>
      </ActionModal>
      <ActionModal
        open={proxyModal === "io"}
        title="代理导入导出"
        size="lg"
        onClose={() => setProxyModal(null)}
        footer={<><button onClick={() => setProxyModal(null)}>关闭</button><button className="primary" onClick={() => runAction(importProxies, "导入完成")} disabled={!importText.trim()}>导入</button><button onClick={() => runAction(exportProxies, "已生成导出 JSON")}>导出</button></>}
      >
        <div className="proxy-io-grid">
          <label className="proxy-field">
            <span>导入内容</span>
            <textarea value={importText} onChange={(event) => setImportText(event.target.value)} placeholder="代理 JSON 数组" rows={8} />
          </label>
          <label className="proxy-field">
            <span>导出结果</span>
            <textarea value={exportText} onChange={(event) => setExportText(event.target.value)} placeholder="导出结果" rows={8} readOnly />
          </label>
        </div>
      </ActionModal>
      <ActionModal
        open={proxyModal === "quality" && Boolean(qualityReport)}
        title="检测详情"
        description={`${qualityReport?.proxy_name ?? qualityReport?.proxy_id ?? "代理"} · ${qualityReport?.summary ?? "暂无摘要"}`}
        size="xl"
        onClose={() => setProxyModal(null)}
      >
        <div className="stack">
          <div className="proxy-quality-summary">
            <div><span>评分</span><strong>{qualityReport?.score ?? qualityReport?.quality_score ?? "-"}</strong><small>等级 {qualityReport?.grade ?? qualityReport?.quality_grade ?? "-"}</small></div>
            <div><span>状态</span><strong>{qualityLabel(qualityReport?.quality_status)}</strong><small>{qualityReport?.summary ?? "-"}</small></div>
            <div><span>出口 IP</span><strong>{qualityReport?.exit_ip ?? "-"}</strong><small>{[qualityReport?.country, qualityReport?.city].filter(Boolean).join(" · ") || "-"}</small></div>
            <div><span>基础延迟</span><strong>{typeof qualityReport?.base_latency_ms === "number" ? `${qualityReport.base_latency_ms}ms` : "-"}</strong><small>{qualityReport?.country_code ?? "-"}</small></div>
          </div>
          {qualityProblem && (
            <div className="proxy-quality-reason">
              <span>异常原因</span>
              <strong>{qualityProblem}</strong>
            </div>
          )}
          {!qualityItemRows.length ? <div className="empty">暂无分项检测明细，点击“检测”后会生成。</div> : <DataTable rows={qualityItemRows} columns={["target", "status", "http_status", "latency_ms", "message", "cf_ray"]} />}
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminOAuth() {
  const [states, setStates] = useState<any[]>([]);
  const [jobs, setJobs] = useState<any[]>([]);
  const [oauthModal, setOauthModal] = useState<null | "create_job" | "job">(null);
  const [selectedJobId, setSelectedJobId] = useState("");
  const [accountId, setAccountId] = useState("");
  const [jobType, setJobType] = useState("refresh");
  const [authMode, setAuthMode] = useState("oauth");
  const [status, setStatus] = useState("");
  const [jobInputs, setJobInputs] = useState<Record<string, string>>({});
  const [jobPriorities, setJobPriorities] = useState<Record<string, string>>({});

  async function load() {
    const jobQuery = new URLSearchParams({ limit: "200" });
    if (status) jobQuery.set("status", status);
    if (accountId) jobQuery.set("account_id", accountId);
    const stateQuery = new URLSearchParams();
    if (accountId) stateQuery.set("account_id", accountId);
    setStates(await adminApi.request<any[]>(`/api/admin/v1/account-auth-states?${stateQuery.toString()}`));
    setJobs(await adminApi.request<any[]>(`/api/admin/v1/oauth/jobs?${jobQuery.toString()}`));
  }

  useEffect(() => { void load(); }, []);

  async function createJob() {
    await adminApi.request("/api/admin/v1/oauth/jobs", { method: "POST", body: JSON.stringify({ account_id: accountId, job_type: jobType, auth_mode: authMode }) });
    await load();
    setOauthModal(null);
  }

  async function patchState(row: any, auth_status: string, queue_job = false) {
    await adminApi.request(`/api/admin/v1/account-auth-states/${row.account_id}`, { method: "PATCH", body: JSON.stringify({ auth_status, queue_job }) });
    await load();
  }

  async function submitJobInput(jobId: string) {
    const authorizationCode = jobInputs[jobId]?.trim();
    if (!authorizationCode) return;
    await adminApi.request(`/api/admin/v1/oauth/jobs/${jobId}/input`, { method: "POST", body: JSON.stringify({ authorization_code: authorizationCode }) });
    setJobInputs((current) => ({ ...current, [jobId]: "" }));
    await load();
  }

  async function patchJob(jobId: string, body: Record<string, unknown>) {
    await adminApi.request(`/api/admin/v1/oauth/jobs/${jobId}`, { method: "PATCH", body: JSON.stringify(body) });
    await load();
  }

  const jobRows = jobs.map((job) => {
    const progress = oauthProgress(job);
    return { ...job, oauth_progress: Object.keys(progress).length ? progress : "" };
  });
  const selectedJob = jobs.find((job) => job.id === selectedJobId);

  function openJob(row: any) {
    setSelectedJobId(row.id);
    setJobPriorities((current) => ({ ...current, [row.id]: current[row.id] ?? String(row.priority ?? "") }));
    setOauthModal("job");
  }

  return (
    <div className="stack">
      <section className="panel">
        <SectionHeader
          title="OAuth 任务"
          description="筛选账号认证状态和授权任务，任务入队与处理在弹窗中完成。"
          action={<><button onClick={() => setOauthModal("create_job")}><Link size={15} /> 任务入队</button><button onClick={load}>刷新</button></>}
        />
        <div className="row filters">
          <input value={accountId} onChange={(event) => setAccountId(event.target.value)} placeholder="账号 ID" />
          <select value={status} onChange={(event) => setStatus(event.target.value)}>
            <option value="">任意任务状态</option>
            <option value="queued">排队中</option>
            <option value="leased">执行中</option>
            <option value="succeeded">成功</option>
            <option value="failed">失败</option>
            <option value="canceled">已取消</option>
          </select>
          <button onClick={load}>筛选</button>
        </div>
      </section>
      <section className="panel"><h2>认证状态</h2><DataTable rows={states} columns={["account_id", "auth_mode", "auth_status", "provider_subject", "expires_at", "refresh_due_at", "last_error"]} action={(row) => <div className="actions"><button onClick={() => patchState(row, "refresh_due", true)}>刷新</button><button onClick={() => patchState(row, "reauth_required", true)}>重新授权</button><button onClick={() => patchState(row, "revoked", true)}>撤销</button></div>} /></section>
      <section className="panel"><h2>任务</h2><DataTable rows={jobRows} columns={["account_id", "job_type", "auth_mode", "status", "priority", "oauth_progress", "attempt_count", "max_attempts", "lease_owner", "last_error", "created_at"]} action={(row) => {
        return (
          <div className="actions">
            <button onClick={() => openJob(row)}>处理</button>
            <button onClick={() => patchJob(row.id, { status: "queued" })}>重新排队</button>
            <button onClick={() => patchJob(row.id, { status: "canceled" })}>取消</button>
          </div>
        );
      }} /></section>
      <ActionModal
        open={oauthModal === "create_job"}
        title="OAuth 任务入队"
        size="sm"
        onClose={() => setOauthModal(null)}
        footer={<><button onClick={() => setOauthModal(null)}>取消</button><button className="primary" onClick={createJob} disabled={!accountId}>入队</button></>}
      >
        <div className="form-grid single">
          <input value={accountId} onChange={(event) => setAccountId(event.target.value)} placeholder="账号 ID" />
          <select value={jobType} onChange={(event) => setJobType(event.target.value)}>
            <option value="onboarding">初始化</option>
            <option value="refresh">刷新</option>
            <option value="reauth">重新授权</option>
            <option value="revoke">撤销</option>
          </select>
          <input value={authMode} onChange={(event) => setAuthMode(event.target.value)} placeholder="认证模式" />
        </div>
      </ActionModal>
      <ActionModal
        open={oauthModal === "job" && Boolean(selectedJob)}
        title="处理 OAuth 任务"
        size="md"
        onClose={() => setOauthModal(null)}
        footer={<><button onClick={() => setOauthModal(null)}>关闭</button>{selectedJob && <button className="primary" onClick={() => patchJob(selectedJob.id, { priority: Number(jobPriorities[selectedJob.id] ?? selectedJob.priority ?? 100) })}>保存优先级</button>}</>}
      >
        {selectedJob && (() => {
          const progress = oauthProgress(selectedJob);
          return (
            <div className="stack">
              <DataTable rows={[selectedJob]} columns={["account_id", "job_type", "auth_mode", "status", "priority", "last_error"]} />
              {progress.authorization_url && <a href={progress.authorization_url} target="_blank" rel="noreferrer">打开授权链接</a>}
              {progress.user_code && <code>{progress.user_code}</code>}
              {progress.input === "authorization_code" && (
                <div className="form-grid single">
                  <input value={jobInputs[selectedJob.id] ?? ""} onChange={(event) => setJobInputs((current) => ({ ...current, [selectedJob.id]: event.target.value }))} placeholder="授权码" />
                  <button onClick={() => submitJobInput(selectedJob.id)} disabled={!jobInputs[selectedJob.id]?.trim()}>提交授权码</button>
                </div>
              )}
              <div className="form-grid single">
                <input value={jobPriorities[selectedJob.id] ?? String(selectedJob.priority ?? "")} onChange={(event) => setJobPriorities((current) => ({ ...current, [selectedJob.id]: event.target.value }))} placeholder="优先级" />
              </div>
            </div>
          );
        })()}
      </ActionModal>
    </div>
  );
}

export function AdminUsage({ role }: { role: AdminRole }) {
  const canCleanup = role === "platform_owner";
  const [rows, setRows] = useState<any[]>([]);
  const [summary, setSummary] = useState<any>(null);
  const [analytics, setAnalytics] = useState<any[]>([]);
  const [usageGroupBy, setUsageGroupBy] = useState("model");
  const [affinityStats, setAffinityStats] = useState<any[]>([]);
  const [model, setModel] = useState("");
  const [requestId, setRequestId] = useState("");
  const [apiKeyId, setApiKeyId] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [status, setStatus] = useState("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");
  const [cleanupBefore, setCleanupBefore] = useState("");
  const [cleanupStatus, setCleanupStatus] = useState("");
  const [affinityCleanupRule, setAffinityCleanupRule] = useState("");
  const [affinityCleanupModel, setAffinityCleanupModel] = useState("");
  const [affinityCleanupEndpoint, setAffinityCleanupEndpoint] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  function usageQuery(limit = "200") {
    const query = new URLSearchParams({ limit: "200" });
    query.set("limit", limit);
    if (requestId) query.set("request_id", requestId);
    if (apiKeyId) query.set("api_key_id", apiKeyId);
    if (model) query.set("model", model);
    if (endpoint) query.set("endpoint", endpoint);
    if (status) query.set("status", status);
    if (dateFrom) query.set("date_from", dateFrom);
    if (dateTo) query.set("date_to", dateTo);
    return query;
  }

  async function load() {
    const query = usageQuery("200");
    const analyticsQuery = new URLSearchParams(query);
    analyticsQuery.set("group_by", usageGroupBy);
    const [nextRows, nextSummary, nextAnalytics, nextAffinityStats] = await Promise.all([
      adminApi.request<any[]>(`/api/admin/v1/usage?${query.toString()}`),
      adminApi.request<any>(`/api/admin/v1/usage/summary?${query.toString()}`),
      adminApi.request<any[]>(`/api/admin/v1/usage/analytics?${analyticsQuery.toString()}`),
      adminApi.request<any[]>("/api/admin/v1/runtime/affinity-stats"),
    ]);
    setRows(nextRows);
    setSummary(nextSummary);
    setAnalytics(nextAnalytics);
    setAffinityStats(nextAffinityStats);
  }

  async function runAction(action: () => Promise<void>, success = "") {
    setError("");
    setMessage("");
    try {
      await action();
      if (success) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function exportCSV() {
    const response = await fetch(`${API_BASE}/api/admin/v1/usage/export?${usageQuery("20000").toString()}`, {
      headers: adminApi.token ? { Authorization: `Bearer ${adminApi.token}` } : {},
    });
    if (!response.ok) throw new Error(await response.text());
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "usage-export.csv";
    link.click();
    URL.revokeObjectURL(url);
  }

  async function cleanup(dryRun: boolean) {
    const result = await adminApi.request<any>("/api/admin/v1/usage/cleanup", {
      method: "POST",
      body: JSON.stringify({ before: cleanupBefore, status: cleanupStatus, dry_run: dryRun }),
    });
    setMessage(dryRun ? `匹配 ${result.matched} 条记录` : `已清理 ${result.deleted} 条记录`);
    await load();
  }

  async function cleanupAffinity() {
    const result = await adminApi.request<any>("/api/admin/v1/runtime/affinity-cleanup", {
      method: "POST",
      body: JSON.stringify({
        rule_name: affinityCleanupRule,
        model_name: affinityCleanupModel,
        endpoint: affinityCleanupEndpoint,
        expired_only: !affinityCleanupRule && !affinityCleanupModel && !affinityCleanupEndpoint,
      }),
    });
    setMessage(`已清理 ${result.deleted ?? 0} 条 affinity 绑定`);
    await load();
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);
  return (
    <div className="stack">
      {summary && (
        <div className="grid three">
          <Metric label="请求数" value={String(summary.total ?? 0)} />
          <Metric label="成功" value={String(summary.success ?? 0)} />
          <Metric label="失败" value={String(summary.failed ?? 0)} />
          <Metric label="拒绝" value={String(summary.rejected ?? 0)} />
          <Metric label="成本 USD" value={String(summary.actual_cost ?? "0")} />
          <Metric label="平均耗时 ms" value={String(summary.avg_duration_ms ?? "0")} />
        </div>
      )}
      {(error || message) && <section className="panel">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</section>}
      <section className="panel">
        <h2>用量</h2>
        <div className="row filters">
          <input value={requestId} onChange={(event) => setRequestId(event.target.value)} placeholder="请求 ID" />
          <input value={apiKeyId} onChange={(event) => setApiKeyId(event.target.value)} placeholder="API Key ID" />
          <input value={model} onChange={(event) => setModel(event.target.value)} placeholder="模型" />
          <input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} placeholder="端点" />
          <input value={dateFrom} onChange={(event) => setDateFrom(event.target.value)} placeholder="开始日期 YYYY-MM-DD" />
          <input value={dateTo} onChange={(event) => setDateTo(event.target.value)} placeholder="结束日期 YYYY-MM-DD" />
          <select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">任意状态</option><option value="success">成功</option><option value="failed">失败</option><option value="rejected">已拒绝</option></select>
          <select value={usageGroupBy} onChange={(event) => setUsageGroupBy(event.target.value)}><option value="model">按模型</option><option value="endpoint">按端点</option><option value="api_key">按密钥</option><option value="channel">按通道</option><option value="account">按账号</option><option value="status">按状态</option><option value="error_code">按错误</option></select>
          <button onClick={() => runAction(load)}>筛选</button>
          <button onClick={() => runAction(exportCSV, "CSV 已导出")}>导出 CSV</button>
        </div>
        <DataTable rows={rows} columns={["request_id", "requested_model", "upstream_model", "endpoint", "input_tokens", "output_tokens", "image_count", "request_count", "actual_cost", "upstream_status", "duration_ms", "usage_source", "status", "created_at"]} />
      </section>
      <section className="panel">
        <h2>用量聚合</h2>
        <DataTable rows={analytics} columns={["dimension", "group_by", "request_count", "success_count", "failed_count", "rejected_count", "input_tokens", "output_tokens", "image_count", "audio_seconds", "actual_cost", "avg_duration_ms", "stream_event_count", "websocket_frame_count"]} />
      </section>
      <section className="panel">
        <h2>Affinity 绑定</h2>
        <div className="form-grid">
          <input value={affinityCleanupRule} onChange={(event) => setAffinityCleanupRule(event.target.value)} placeholder="规则名，可选" />
          <input value={affinityCleanupModel} onChange={(event) => setAffinityCleanupModel(event.target.value)} placeholder="模型，可选" />
          <input value={affinityCleanupEndpoint} onChange={(event) => setAffinityCleanupEndpoint(event.target.value)} placeholder="端点，可选" />
          <button onClick={() => runAction(cleanupAffinity, "Affinity 已清理")}>清理过期/匹配项</button>
        </div>
        <DataTable rows={affinityStats} columns={["rule_name", "model_name", "endpoint", "binding_count", "active_count", "expired_count", "total_hits", "total_misses", "last_seen_at", "last_hit_at", "last_miss_at"]} />
      </section>
      {canCleanup && (
        <section className="panel">
          <h2>清理历史记录</h2>
          <div className="form-grid">
            <input value={cleanupBefore} onChange={(event) => setCleanupBefore(event.target.value)} placeholder="清理早于 YYYY-MM-DD" />
            <select value={cleanupStatus} onChange={(event) => setCleanupStatus(event.target.value)}>
              <option value="">任意状态</option>
              <option value="success">成功</option>
              <option value="failed">失败</option>
              <option value="rejected">已拒绝</option>
            </select>
            <button onClick={() => runAction(() => cleanup(true))} disabled={!cleanupBefore}>预览</button>
            <button onClick={() => runAction(() => cleanup(false))} disabled={!cleanupBefore}>执行清理</button>
          </div>
        </section>
      )}
    </div>
  );
}

export function AdminBilling({ requestedTab }: { requestedTab?: string }) {
  const [tab, setTab] = useState<BillingTab>("overview");
  const [summary, setSummary] = useState<Record<string, string>>({});
  const [plans, setPlans] = useState<any[]>([]);
  const [orders, setOrders] = useState<any[]>([]);
  const [paymentEvents, setPaymentEvents] = useState<any[]>([]);
  const [paymentEventStatus, setPaymentEventStatus] = useState("");
  const [subscriptions, setSubscriptions] = useState<any[]>([]);
  const [paymentSettings, setPaymentSettings] = useState<PaymentSettings | null>(null);
  const [paymentProviders, setPaymentProviders] = useState<PaymentProvider[]>([]);
  const [paymentRoutes, setPaymentRoutes] = useState<PaymentMethodRoute[]>([]);
  const [codes, setCodes] = useState<any[]>([]);
  const [rebates, setRebates] = useState<any[]>([]);
  const [users, setUsers] = useState<any[]>([]);
  const [groups, setGroups] = useState<any[]>([]);
  const [planName, setPlanName] = useState("");
  const [planStatus, setPlanStatus] = useState("draft");
  const [priceUSD, setPriceUSD] = useState("20");
  const [billingPeriod, setBillingPeriod] = useState("month");
  const [walletCreditUSD, setWalletCreditUSD] = useState("20");
  const [planGroupId, setPlanGroupId] = useState("");
  const [stripePriceId, setStripePriceId] = useState("");
  const [planFeatures, setPlanFeatures] = useState("");
  const [affiliateUserId, setAffiliateUserId] = useState("");
  const [affiliateCode, setAffiliateCode] = useState("");
  const [rebateRate, setRebateRate] = useState("0.1");
  const [billingModal, setBillingModal] = useState<null | "plan" | "affiliate">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const eventParams = new URLSearchParams({ limit: "100" });
    if (paymentEventStatus) eventParams.set("status", paymentEventStatus);
    const [nextSummary, nextPlans, nextOrders, nextPaymentEvents, nextSubscriptions, nextPaymentSettings, nextPaymentProviders, nextPaymentRoutes, nextCodes, nextRebates, nextUsers, nextGroups] = await Promise.all([
      adminApi.request<Record<string, string>>("/api/admin/v1/finance/summary"),
      adminApi.request<any[]>("/api/admin/v1/subscription-plans"),
      adminApi.request<any[]>("/api/admin/v1/orders"),
      adminApi.request<any[]>(`/api/admin/v1/payment-events?${eventParams.toString()}`),
      adminApi.request<any[]>("/api/admin/v1/subscriptions"),
      adminApi.request<PaymentSettings>("/api/admin/v1/payment-settings"),
      adminApi.request<PaymentProvider[]>("/api/admin/v1/payment-providers"),
      adminApi.request<PaymentMethodRoute[]>("/api/admin/v1/payment-method-routes"),
      adminApi.request<any[]>("/api/admin/v1/affiliate-codes"),
      adminApi.request<any[]>("/api/admin/v1/affiliate-rebates"),
      adminApi.request<any[]>("/api/admin/v1/users?limit=200"),
      adminApi.request<any[]>("/api/admin/v1/groups"),
    ]);
    setSummary(nextSummary);
    setPlans(nextPlans);
    setOrders(nextOrders);
    setPaymentEvents(nextPaymentEvents);
    setSubscriptions(nextSubscriptions);
    setPaymentSettings(nextPaymentSettings);
    setPaymentProviders(nextPaymentProviders);
    setPaymentRoutes(nextPaymentRoutes);
    setCodes(nextCodes);
    setRebates(nextRebates);
    setUsers(nextUsers);
    setGroups(nextGroups);
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  useEffect(() => {
    if (requestedTab && ["overview", "payments", "providers", "plans", "orders", "events", "subscriptions", "affiliates"].includes(requestedTab)) {
      setTab(requestedTab as BillingTab);
    }
  }, [requestedTab]);

  async function runAction(action: () => Promise<void>, success = "") {
    setError("");
    setMessage("");
    try {
      await action();
      if (success) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function createPlan() {
    await adminApi.request("/api/admin/v1/subscription-plans", {
      method: "POST",
      body: JSON.stringify({
        name: planName,
        status: planStatus,
        price_usd: priceUSD,
        billing_period: billingPeriod,
        wallet_credit_usd: walletCreditUSD,
        group_id: planGroupId,
        stripe_price_id: stripePriceId,
        features: splitCSV(planFeatures),
        metadata: {},
      }),
    });
    setPlanName("");
    setStripePriceId("");
    setPlanFeatures("");
    await load();
    setBillingModal(null);
  }

  async function createAffiliateCode() {
    await adminApi.request("/api/admin/v1/affiliate-codes", {
      method: "POST",
      body: JSON.stringify({ owner_user_id: affiliateUserId, code: affiliateCode, rebate_rate: rebateRate, status: "active", metadata: {} }),
    });
    setAffiliateCode("");
    await load();
    setBillingModal(null);
  }

  async function refundOrder(orderID: string) {
    await adminApi.request(`/api/admin/v1/orders/${orderID}/refund`, { method: "POST" });
    await load();
  }

  async function replayPaymentEvent(eventID: string) {
    await adminApi.request(`/api/admin/v1/payment-events/${eventID}/replay`, { method: "POST" });
    await load();
  }

  async function settleRebate(rebateID: string) {
    await adminApi.request(`/api/admin/v1/affiliate-rebates/${rebateID}/settle`, { method: "POST" });
    await load();
  }

  return (
    <div className="stack">
      <PageNotice error={error} message={message} />
      <BillingCommandCenter
        summary={summary}
        plans={plans}
        orders={orders}
        paymentEvents={paymentEvents}
        subscriptions={subscriptions}
        rebates={rebates}
        paymentSettings={paymentSettings}
        paymentProviders={paymentProviders}
        paymentRoutes={paymentRoutes}
        onSelectTab={setTab}
        onRefresh={() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }}
      />
      <SectionTabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "overview", label: "财务摘要" },
          { id: "payments", label: "支付设置" },
          { id: "providers", label: "服务商" },
          { id: "plans", label: "订阅计划", count: plans.length },
          { id: "orders", label: "订单退款", count: orders.length },
          { id: "events", label: "支付事件", count: paymentEvents.length },
          { id: "subscriptions", label: "订阅", count: subscriptions.length },
          { id: "affiliates", label: "推广返利", count: rebates.length },
        ]}
      />
      {tab === "overview" && (
        <div className="grid three">
          <Metric label="收入 USD" value={String(summary.paid_revenue_usd ?? "0")} />
          <Metric label="退款 USD" value={String(summary.refunded_usd ?? "0")} />
          <Metric label="充值负债 USD" value={String(summary.wallet_liability_usd ?? "0")} />
          <Metric label="订阅 MRR USD" value={String(summary.subscription_mrr_usd ?? "0")} />
          <Metric label="待结算返利 USD" value={String(summary.affiliate_pending_usd ?? "0")} />
          <Metric label="用量成本 USD" value={String(summary.usage_actual_cost_usd ?? "0")} />
          <Metric label="退款阻塞" value={String(summary.refund_blocked_count ?? "0")} />
          <Metric label="有效订阅" value={String(summary.active_subscription_cnt ?? "0")} />
        </div>
      )}
      {tab === "payments" && (
        <PaymentSettingsPanel onSaved={() => load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))} />
      )}
      {tab === "providers" && (
        <PaymentSettingsPanel onSaved={() => load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))} />
      )}
      {tab === "plans" && (
        <section className="panel">
          <SectionHeader title="订阅计划" description="配置售价、周期、赠额、授予分组和 Stripe Price。" action={<button onClick={() => setBillingModal("plan")}>创建计划</button>} />
          <DataTable rows={plans} columns={["name", "status", "price_usd", "billing_period", "wallet_credit_usd", "group_name", "stripe_price_id", "features", "created_at"]} />
        </section>
      )}
      {tab === "orders" && (
        <section className="panel">
          <SectionHeader title="订单与退款" description="处理支付、退款和退款阻塞状态。" />
          <DataTable rows={orders} columns={["order_type", "user_id", "amount_usd", "currency", "status", "payment_provider", "payment_method", "pay_currency", "pay_amount_cents", "fx_rate", "upstream_trade_no", "upstream_transaction_id", "paid_at", "refunded_at", "refund_blocked_reason", "created_at"]} action={(row) => <button onClick={() => runAction(() => refundOrder(row.id), "退款已处理")} disabled={!["paid", "refund_blocked"].includes(row.status)}>{row.status === "refund_blocked" ? "重试退款" : "退款"}</button>} />
        </section>
      )}
      {tab === "events" && (
        <section className="panel">
          <SectionHeader
            title="支付事件"
            description="支付 webhook 的处理状态。失败事件可以在这里重放，重放仍使用后端幂等规则。"
            action={<button onClick={() => runAction(load)}><RefreshCcw size={15} /> 刷新</button>}
          />
          <div className="row filters">
            <select value={paymentEventStatus} onChange={(event) => setPaymentEventStatus(event.target.value)}>
              <option value="">任意状态</option>
              <option value="processed">已处理</option>
              <option value="failed">失败</option>
              <option value="pending">待处理</option>
              <option value="replayed">已重放</option>
            </select>
            <button onClick={() => runAction(load)}>筛选</button>
          </div>
          <DataTable
            rows={paymentEvents}
            columns={["provider", "provider_event_id", "event_type", "order_id", "status", "attempts", "processed_at", "last_attempt_at", "last_error", "processing_error", "created_at"]}
            action={(row) => (
              <button onClick={() => runAction(() => replayPaymentEvent(row.id), "支付事件已重放")} disabled={!["failed", "pending"].includes(row.status)}>
                <RefreshCcw size={15} /> 重放
              </button>
            )}
          />
        </section>
      )}
      {tab === "subscriptions" && (
        <section className="panel">
          <SectionHeader title="订阅" description="跟踪订阅周期、状态和授予分组。" />
          <DataTable rows={subscriptions} columns={["email", "plan_name", "status", "stripe_subscription_id", "granted_group_id", "starts_at", "ends_at", "current_period_start", "current_period_end", "created_at"]} />
        </section>
      )}
      {tab === "affiliates" && (
        <div className="dashboard-list">
          <section className="panel">
            <SectionHeader title="推广码" description="给用户创建邀请码和返利比例。" action={<button onClick={() => setBillingModal("affiliate")}>创建推广码</button>} />
            <DataTable rows={codes} columns={["email", "code", "rebate_rate", "status", "metadata", "created_at"]} />
          </section>
          <section className="panel">
            <SectionHeader title="推广返利" description="审核 pending 返利并结算入钱包。" />
            <DataTable rows={rebates} columns={["code", "owner_user_id", "order_id", "user_id", "amount_usd", "status", "wallet_ledger_id", "settled_at", "created_at"]} action={(row) => <button onClick={() => runAction(() => settleRebate(row.id), "返利已结算")} disabled={row.status !== "pending"}>结算</button>} />
          </section>
        </div>
      )}
      <ActionModal
        open={billingModal === "plan"}
        title="创建订阅计划"
        size="md"
        onClose={() => setBillingModal(null)}
        footer={<><button onClick={() => setBillingModal(null)}>取消</button><button className="primary" onClick={() => runAction(createPlan, "订阅计划已创建")} disabled={!planName}>创建计划</button></>}
      >
        <div className="form-grid">
          <input value={planName} onChange={(event) => setPlanName(event.target.value)} placeholder="计划名称" />
          <select value={planStatus} onChange={(event) => setPlanStatus(event.target.value)}><option value="draft">草稿</option><option value="active">启用</option><option value="archived">归档</option></select>
          <input value={priceUSD} onChange={(event) => setPriceUSD(event.target.value)} placeholder="价格 USD" />
          <select value={billingPeriod} onChange={(event) => setBillingPeriod(event.target.value)}><option value="month">月</option><option value="year">年</option><option value="one_time">一次性</option></select>
          <input value={walletCreditUSD} onChange={(event) => setWalletCreditUSD(event.target.value)} placeholder="赠送余额 USD" />
          <select value={planGroupId} onChange={(event) => setPlanGroupId(event.target.value)}><option value="">不授予分组</option>{groups.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}</select>
          <input value={stripePriceId} onChange={(event) => setStripePriceId(event.target.value)} placeholder="Stripe Price ID，可空" />
          <input value={planFeatures} onChange={(event) => setPlanFeatures(event.target.value)} placeholder="功能点，逗号分隔" />
        </div>
      </ActionModal>
      <ActionModal
        open={billingModal === "affiliate"}
        title="创建推广码"
        size="sm"
        onClose={() => setBillingModal(null)}
        footer={<><button onClick={() => setBillingModal(null)}>取消</button><button className="primary" onClick={() => runAction(createAffiliateCode, "推广码已创建")} disabled={!affiliateUserId || !affiliateCode}>创建推广码</button></>}
      >
        <div className="form-grid single">
          <select value={affiliateUserId} onChange={(event) => setAffiliateUserId(event.target.value)}><option value="">选择推广人</option>{users.map((user) => <option key={user.id} value={user.id}>{user.email}</option>)}</select>
          <input value={affiliateCode} onChange={(event) => setAffiliateCode(event.target.value)} placeholder="邀请码" />
          <input value={rebateRate} onChange={(event) => setRebateRate(event.target.value)} placeholder="返利比例，例如 0.1" />
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminControls() {
  const [limits, setLimits] = useState<any[]>([]);
  const [channels, setChannels] = useState<any[]>([]);
  const [events, setEvents] = useState<any[]>([]);
  const [emailVerificationEnabled, setEmailVerificationEnabled] = useState(false);
  const [smtpHost, setSMTPHost] = useState("");
  const [smtpPort, setSMTPPort] = useState("587");
  const [smtpUsername, setSMTPUsername] = useState("");
  const [smtpPassword, setSMTPPassword] = useState("");
  const [smtpPasswordPresent, setSMTPPasswordPresent] = useState(false);
  const [smtpFrom, setSMTPFrom] = useState("");
  const [smtpTLSMode, setSMTPTLSMode] = useState("starttls");
  const [testEmail, setTestEmail] = useState("");
  const [targetType, setTargetType] = useState("user");
  const [targetId, setTargetId] = useState("");
  const [dailyUSD, setDailyUSD] = useState("");
  const [monthlyUSD, setMonthlyUSD] = useState("");
  const [dailyRequests, setDailyRequests] = useState("");
  const [monthlyRequests, setMonthlyRequests] = useState("");
  const [lowBalance, setLowBalance] = useState("");
  const [channelName, setChannelName] = useState("");
  const [targetUrl, setTargetUrl] = useState("");
  const [minSeverity, setMinSeverity] = useState("warning");
  const [eventTypes, setEventTypes] = useState("");
  const [channelSecret, setChannelSecret] = useState("");
  const [editNotificationId, setEditNotificationId] = useState("");
  const [editNotificationName, setEditNotificationName] = useState("");
  const [editNotificationUrl, setEditNotificationUrl] = useState("");
  const [editNotificationSeverity, setEditNotificationSeverity] = useState("warning");
  const [editNotificationTypes, setEditNotificationTypes] = useState("");
  const [editNotificationStatus, setEditNotificationStatus] = useState("active");
  const [controlsModal, setControlsModal] = useState<null | "limit" | "channel" | "edit_channel">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  async function load() {
    const [nextLimits, nextChannels, nextEvents, emailSettings] = await Promise.all([
      adminApi.request<any[]>("/api/admin/v1/spend-limits"),
      adminApi.request<any[]>("/api/admin/v1/notification-channels"),
      adminApi.request<any[]>("/api/admin/v1/notification-events?limit=100"),
      adminApi.request<any>("/api/admin/v1/email-verification-settings"),
    ]);
    setLimits(nextLimits);
    setChannels(nextChannels);
    setEvents(nextEvents);
    setEmailVerificationEnabled(emailSettings.registration_verification_enabled === true);
    setSMTPHost(emailSettings.smtp?.host ?? "");
    setSMTPPort(String(emailSettings.smtp?.port ?? 587));
    setSMTPUsername(emailSettings.smtp?.username ?? "");
    setSMTPPassword("");
    setSMTPPasswordPresent(emailSettings.smtp?.password_present === true);
    setSMTPFrom(emailSettings.smtp?.from ?? "");
    setSMTPTLSMode(emailSettings.smtp?.tls_mode ?? "starttls");
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  async function runAction(action: () => Promise<void>, success = "") {
    setError("");
    setMessage("");
    try {
      await action();
      if (success) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function saveEmailVerificationSettings() {
    await adminApi.request("/api/admin/v1/email-verification-settings", {
      method: "PUT",
      body: JSON.stringify({
        registration_verification_enabled: emailVerificationEnabled,
        host: smtpHost,
        port: Number(smtpPort) || 587,
        username: smtpUsername,
        password: smtpPassword,
        from: smtpFrom,
        tls_mode: smtpTLSMode,
      }),
    });
    setSMTPPassword("");
    await load();
  }

  async function testEmailVerificationSettings() {
    await adminApi.request("/api/admin/v1/email-verification-settings/test", {
      method: "POST",
      body: JSON.stringify({ email: testEmail }),
    });
  }

  async function saveLimit() {
    await adminApi.request("/api/admin/v1/spend-limits", {
      method: "PUT",
      body: JSON.stringify({
        target_type: targetType,
        target_id: targetId,
        daily_usd_limit: dailyUSD || null,
        monthly_usd_limit: monthlyUSD || null,
        daily_request_limit: dailyRequests ? Number(dailyRequests) : null,
        monthly_request_limit: monthlyRequests ? Number(monthlyRequests) : null,
        low_balance_threshold: targetType === "user" ? lowBalance || null : null,
      }),
    });
    await load();
    setControlsModal(null);
  }

  async function createChannel() {
    const channel = await adminApi.request<any>("/api/admin/v1/notification-channels", {
      method: "POST",
      body: JSON.stringify({
        name: channelName,
        target_url: targetUrl,
        min_severity: minSeverity,
        event_types: splitCSV(eventTypes),
      }),
    });
    setChannelSecret(channel.signing_secret ?? "");
    setTargetUrl("");
    await load();
    setControlsModal(null);
  }

  async function toggleChannel(row: any) {
    const channel = await adminApi.request<any>(`/api/admin/v1/notification-channels/${row.id}`, {
      method: "PATCH",
      body: JSON.stringify({ status: row.status === "active" ? "disabled" : "active" }),
    });
    if (channel.signing_secret) setChannelSecret(channel.signing_secret);
    await load();
    setControlsModal(null);
  }

  async function rotateChannelSecret(row: any) {
    const channel = await adminApi.request<any>(`/api/admin/v1/notification-channels/${row.id}`, {
      method: "PATCH",
      body: JSON.stringify({ rotate_signing_secret: true }),
    });
    setChannelSecret(channel.signing_secret ?? "");
    await load();
  }

  async function testChannel(row: any) {
    await adminApi.request(`/api/admin/v1/notification-channels/${row.id}/test`, { method: "POST" });
    await load();
  }

  async function retryNotificationEvent(row: any) {
    await adminApi.request(`/api/admin/v1/notification-events/${row.id}/retry`, { method: "POST" });
    await load();
  }

  function selectNotificationChannel(row: any) {
    setEditNotificationId(row.id);
    setEditNotificationName(row.name ?? "");
    setEditNotificationUrl("");
    setEditNotificationSeverity(row.min_severity ?? "warning");
    setEditNotificationTypes((row.event_types ?? []).join(","));
    setEditNotificationStatus(row.status ?? "active");
    setControlsModal("edit_channel");
  }

  async function saveNotificationChannel() {
    const body: Record<string, unknown> = {
      name: editNotificationName,
      min_severity: editNotificationSeverity,
      event_types: splitCSV(editNotificationTypes),
      status: editNotificationStatus,
    };
    if (editNotificationUrl.trim()) body.target_url = editNotificationUrl;
    const channel = await adminApi.request<any>(`/api/admin/v1/notification-channels/${editNotificationId}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    });
    if (channel.signing_secret) setChannelSecret(channel.signing_secret);
    await load();
  }

  return (
    <div className="stack">
      <PageNotice error={error} message={message} />
      <div className="metric-grid tight">
        <Metric label="SMTP" value={smtpHost && smtpFrom ? "已配置" : "未配置"} detail={emailVerificationEnabled ? "注册验证码已开启" : "注册验证码关闭"} />
        <Metric label="通知通道" value={String(channels.filter((row) => row.status === "active").length)} detail={`${channels.length} 个通道总数`} />
        <Metric label="待处理事件" value={String(events.filter((row) => row.status === "pending").length)} detail="等待 dispatcher 发送" />
        <Metric label="失败事件" value={String(events.filter((row) => row.status === "failed").length)} detail="可人工重试" />
        <Metric label="消费限制" value={String(limits.length)} detail="用户/API Key 限制规则" />
      </div>
      <section className="panel">
        <SectionHeader title="注册邮箱验证码" description="配置 SMTP 并控制普通用户注册时是否需要邮箱验证码。" />
        <div className="form-grid">
          <select value={emailVerificationEnabled ? "true" : "false"} onChange={(event) => setEmailVerificationEnabled(event.target.value === "true")}>
            <option value="false">关闭注册验证码</option>
            <option value="true">开启注册验证码</option>
          </select>
          <input value={smtpHost} onChange={(event) => setSMTPHost(event.target.value)} placeholder="SMTP Host" />
          <input value={smtpPort} onChange={(event) => setSMTPPort(event.target.value)} placeholder="SMTP Port" inputMode="numeric" />
          <select value={smtpTLSMode} onChange={(event) => setSMTPTLSMode(event.target.value)}>
            <option value="starttls">STARTTLS</option>
            <option value="tls">TLS</option>
            <option value="none">无 TLS</option>
          </select>
          <input value={smtpUsername} onChange={(event) => setSMTPUsername(event.target.value)} placeholder="SMTP 用户名" autoComplete="off" />
          <input value={smtpPassword} onChange={(event) => setSMTPPassword(event.target.value)} placeholder={smtpPasswordPresent ? "SMTP 密码已配置，留空不变" : "SMTP 密码"} type="password" autoComplete="new-password" />
          <input value={smtpFrom} onChange={(event) => setSMTPFrom(event.target.value)} placeholder="发件人，例如 Relay <noreply@example.com>" />
          <button onClick={() => runAction(saveEmailVerificationSettings, "邮箱验证码设置已保存")}>保存邮箱设置</button>
        </div>
        <div className="form-grid">
          <input value={testEmail} onChange={(event) => setTestEmail(event.target.value)} placeholder="测试收件邮箱" autoComplete="email" />
          <button onClick={() => runAction(testEmailVerificationSettings, "测试邮件已发送")} disabled={!testEmail}>发送测试邮件</button>
        </div>
      </section>
      <section className="panel">
        <SectionHeader title="消费限制" action={<button onClick={() => setControlsModal("limit")}><ShieldCheck size={15} /> 新增/修改限制</button>} />
        <DataTable rows={limits} columns={["target_type", "target_id", "daily_usd_limit", "monthly_usd_limit", "daily_request_limit", "monthly_request_limit", "low_balance_threshold", "status"]} />
      </section>
      <section className="panel">
        <SectionHeader title="通知通道" action={<button onClick={() => setControlsModal("channel")}><Bell size={15} /> 创建通知通道</button>} />
        {channelSecret && <pre>{channelSecret}</pre>}
        <DataTable rows={channels} columns={["name", "channel_type", "min_severity", "event_types", "target_url_present", "signing_secret_present", "status", "created_at"]} action={(row) => <div className="actions"><button onClick={() => selectNotificationChannel(row)}>编辑</button><button onClick={() => testChannel(row)}>测试</button><button onClick={() => toggleChannel(row)}>{row.status === "active" ? "停用" : "启用"}</button><button onClick={() => rotateChannelSecret(row)}>轮换密钥</button></div>} />
      </section>
      <section className="panel">
        <SectionHeader title="通知事件" description="失败或被抑制的事件可以在修复通道后重新排队发送。" />
        <DataTable
          rows={events}
          columns={["event_type", "severity", "title", "message", "target_type", "target_id", "payload", "status", "attempts", "last_error", "created_at"]}
          action={(row) => (
            <button onClick={() => retryNotificationEvent(row)} disabled={!["failed", "suppressed", "pending"].includes(row.status)}>
              <RefreshCcw size={15} /> 重试
            </button>
          )}
        />
      </section>
      <ActionModal
        open={controlsModal === "limit"}
        title="消费限制"
        size="md"
        onClose={() => setControlsModal(null)}
        footer={<><button onClick={() => setControlsModal(null)}>取消</button><button className="primary" onClick={() => runAction(saveLimit, "消费限制已保存")} disabled={!targetId}>保存</button></>}
      >
        <div className="form-grid">
          <select value={targetType} onChange={(event) => setTargetType(event.target.value)}><option value="user">用户</option><option value="api_key">API 密钥</option></select>
          <input value={targetId} onChange={(event) => setTargetId(event.target.value)} placeholder="目标 ID" />
          <input value={dailyUSD} onChange={(event) => setDailyUSD(event.target.value)} placeholder="每日 USD" />
          <input value={monthlyUSD} onChange={(event) => setMonthlyUSD(event.target.value)} placeholder="每月 USD" />
          <input value={dailyRequests} onChange={(event) => setDailyRequests(event.target.value)} placeholder="每日请求数" />
          <input value={monthlyRequests} onChange={(event) => setMonthlyRequests(event.target.value)} placeholder="每月请求数" />
          <input value={lowBalance} onChange={(event) => setLowBalance(event.target.value)} placeholder="低余额阈值" disabled={targetType !== "user"} />
        </div>
      </ActionModal>
      <ActionModal
        open={controlsModal === "channel"}
        title="创建通知通道"
        size="md"
        onClose={() => setControlsModal(null)}
        footer={<><button onClick={() => setControlsModal(null)}>取消</button><button className="primary" onClick={() => runAction(createChannel, "通知通道已创建")} disabled={!channelName || !targetUrl}>创建</button></>}
      >
        <div className="form-grid">
          <input value={channelName} onChange={(event) => setChannelName(event.target.value)} placeholder="名称" />
          <input value={targetUrl} onChange={(event) => setTargetUrl(event.target.value)} placeholder="Webhook 地址" />
          <select value={minSeverity} onChange={(event) => setMinSeverity(event.target.value)}><option value="info">信息</option><option value="warning">警告</option><option value="critical">严重</option></select>
          <input value={eventTypes} onChange={(event) => setEventTypes(event.target.value)} placeholder="事件类型，可选" />
        </div>
      </ActionModal>
      <ActionModal
        open={controlsModal === "edit_channel"}
        title="编辑通知通道"
        size="md"
        onClose={() => setControlsModal(null)}
        footer={<><button onClick={() => setControlsModal(null)}>取消</button><button className="primary" onClick={() => runAction(saveNotificationChannel, "通知通道已保存")} disabled={!editNotificationId || !editNotificationName}>保存</button></>}
      >
        <div className="form-grid">
          <input value={editNotificationId} onChange={(event) => setEditNotificationId(event.target.value)} placeholder="通道 ID" />
          <input value={editNotificationName} onChange={(event) => setEditNotificationName(event.target.value)} placeholder="名称" />
          <input value={editNotificationUrl} onChange={(event) => setEditNotificationUrl(event.target.value)} placeholder="新 Webhook 地址，留空不变" />
          <select value={editNotificationSeverity} onChange={(event) => setEditNotificationSeverity(event.target.value)}><option value="info">信息</option><option value="warning">警告</option><option value="critical">严重</option></select>
          <input value={editNotificationTypes} onChange={(event) => setEditNotificationTypes(event.target.value)} placeholder="事件类型，可选" />
          <select value={editNotificationStatus} onChange={(event) => setEditNotificationStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option></select>
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminContent() {
  const [announcements, setAnnouncements] = useState<any[]>([]);
  const [pages, setPages] = useState<any[]>([]);
  const [announcementId, setAnnouncementId] = useState("");
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [audience, setAudience] = useState("all");
  const [severity, setSeverity] = useState("info");
  const [announcementStatus, setAnnouncementStatus] = useState("draft");
  const [pageId, setPageId] = useState("");
  const [slug, setSlug] = useState("");
  const [pageTitle, setPageTitle] = useState("");
  const [pageBody, setPageBody] = useState("");
  const [pageType, setPageType] = useState("custom");
  const [pageVisible, setPageVisible] = useState(false);
  const [pageStatus, setPageStatus] = useState("draft");
  const [contentModal, setContentModal] = useState<null | "announcement" | "page">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const [nextAnnouncements, nextPages] = await Promise.all([
      adminApi.request<any[]>("/api/admin/v1/announcements"),
      adminApi.request<any[]>("/api/admin/v1/content-pages"),
    ]);
    setAnnouncements(nextAnnouncements);
    setPages(nextPages);
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  async function runAction(action: () => Promise<void>, success = "") {
    setError("");
    setMessage("");
    try {
      await action();
      if (success) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function saveAnnouncement() {
    const path = announcementId ? `/api/admin/v1/announcements/${announcementId}` : "/api/admin/v1/announcements";
    await adminApi.request(path, {
      method: announcementId ? "PATCH" : "POST",
      body: JSON.stringify({ title, body, audience, severity, status: announcementStatus }),
    });
    await load();
    setContentModal(null);
  }

  async function savePage() {
    const path = pageId ? `/api/admin/v1/content-pages/${pageId}` : "/api/admin/v1/content-pages";
    await adminApi.request(path, {
      method: pageId ? "PATCH" : "POST",
      body: JSON.stringify({ slug, title: pageTitle, body: pageBody, page_type: pageType, public_visible: pageVisible, status: pageStatus }),
    });
    await load();
    setContentModal(null);
  }

  function newAnnouncement() {
    setAnnouncementId("");
    setTitle("");
    setBody("");
    setAudience("all");
    setSeverity("info");
    setAnnouncementStatus("draft");
    setContentModal("announcement");
  }

  function selectAnnouncement(row: any) {
    setAnnouncementId(row.id);
    setTitle(row.title ?? "");
    setBody(row.body ?? "");
    setAudience(row.audience ?? "all");
    setSeverity(row.severity ?? "info");
    setAnnouncementStatus(row.status ?? "draft");
    setContentModal("announcement");
  }

  function newPage() {
    setPageId("");
    setSlug("");
    setPageTitle("");
    setPageBody("");
    setPageType("custom");
    setPageVisible(false);
    setPageStatus("draft");
    setContentModal("page");
  }

  function selectPage(row: any) {
    setPageId(row.id);
    setSlug(row.slug ?? "");
    setPageTitle(row.title ?? "");
    setPageBody(row.body ?? "");
    setPageType(row.page_type ?? "custom");
    setPageVisible(row.public_visible === true);
    setPageStatus(row.status ?? "draft");
    setContentModal("page");
  }

  return (
    <div className="stack">
      {(error || message) && <section className="panel">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</section>}
      <section className="panel">
        <SectionHeader title="公告" action={<button onClick={newAnnouncement}>新建公告</button>} />
        <DataTable rows={announcements} columns={["title", "audience", "severity", "status", "body", "starts_at", "ends_at", "created_at"]} action={(row) => <button onClick={() => selectAnnouncement(row)}>编辑</button>} />
      </section>
      <section className="panel">
        <SectionHeader title="内容页面" action={<button onClick={newPage}>新建页面</button>} />
        <DataTable rows={pages} columns={["slug", "title", "page_type", "public_visible", "status", "created_at"]} action={(row) => <button onClick={() => selectPage(row)}>编辑</button>} />
      </section>
      <ActionModal
        open={contentModal === "announcement"}
        title={announcementId ? "编辑公告" : "新建公告"}
        size="lg"
        onClose={() => setContentModal(null)}
        footer={<><button onClick={() => setContentModal(null)}>取消</button><button className="primary" onClick={() => runAction(saveAnnouncement, "公告已保存")} disabled={!title}>保存公告</button></>}
      >
        <div className="form-grid">
          <input value={title} onChange={(event) => setTitle(event.target.value)} placeholder="标题" />
          <select value={audience} onChange={(event) => setAudience(event.target.value)}><option value="all">全部</option><option value="portal">用户侧</option><option value="admin">管理侧</option></select>
          <select value={severity} onChange={(event) => setSeverity(event.target.value)}><option value="info">信息</option><option value="warning">警告</option><option value="critical">严重</option></select>
          <select value={announcementStatus} onChange={(event) => setAnnouncementStatus(event.target.value)}><option value="draft">草稿</option><option value="published">发布</option><option value="archived">归档</option></select>
          <textarea value={body} onChange={(event) => setBody(event.target.value)} placeholder="内容" rows={5} />
        </div>
      </ActionModal>
      <ActionModal
        open={contentModal === "page"}
        title={pageId ? "编辑页面" : "新建页面"}
        size="lg"
        onClose={() => setContentModal(null)}
        footer={<><button onClick={() => setContentModal(null)}>取消</button><button className="primary" onClick={() => runAction(savePage, "页面已保存")} disabled={!slug || !pageTitle}>保存页面</button></>}
      >
        <div className="form-grid">
          <input value={slug} onChange={(event) => setSlug(event.target.value)} placeholder="slug" />
          <input value={pageTitle} onChange={(event) => setPageTitle(event.target.value)} placeholder="标题" />
          <select value={pageType} onChange={(event) => setPageType(event.target.value)}><option value="custom">自定义</option><option value="faq">FAQ</option><option value="api_info">API 信息</option><option value="about">关于</option><option value="privacy">隐私</option><option value="terms">用户协议</option><option value="legal">法律</option></select>
          <select value={pageStatus} onChange={(event) => setPageStatus(event.target.value)}><option value="draft">草稿</option><option value="published">发布</option><option value="archived">归档</option></select>
          <select value={pageVisible ? "true" : "false"} onChange={(event) => setPageVisible(event.target.value === "true")}><option value="false">隐藏</option><option value="true">公开</option></select>
          <textarea value={pageBody} onChange={(event) => setPageBody(event.target.value)} placeholder="内容" rows={6} />
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminGroups() {
  const [groups, setGroups] = useState<any[]>([]);
  const [users, setUsers] = useState<any[]>([]);
  const [models, setModels] = useState<any[]>([]);
  const [groupId, setGroupId] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [status, setStatus] = useState("active");
  const [priority, setPriority] = useState("100");
  const [multiplier, setMultiplier] = useState("1");
  const [rpmLimit, setRpmLimit] = useState("");
  const [monthlyLimit, setMonthlyLimit] = useState("");
  const [memberUserId, setMemberUserId] = useState("");
  const [memberRole, setMemberRole] = useState("member");
  const [permissionModel, setPermissionModel] = useState("");
  const [permissionEndpoint, setPermissionEndpoint] = useState("");
  const [permission, setPermission] = useState("allow");
  const [permissionRPM, setPermissionRPM] = useState("");
  const [permissionMultiplier, setPermissionMultiplier] = useState("");
  const [previewUserId, setPreviewUserId] = useState("");
  const [previewModel, setPreviewModel] = useState("");
  const [previewEndpoint, setPreviewEndpoint] = useState("chat");
  const [preview, setPreview] = useState<any>(null);
  const [groupsModal, setGroupsModal] = useState<null | "group" | "member" | "permission" | "preview">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const [nextGroups, nextUsers, nextModels] = await Promise.all([
      adminApi.request<any[]>("/api/admin/v1/groups"),
      adminApi.request<any[]>("/api/admin/v1/users?limit=200"),
      adminApi.request<any[]>("/api/admin/v1/models"),
    ]);
    setGroups(nextGroups);
    setUsers(nextUsers);
    setModels(nextModels);
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  async function runAction(action: () => Promise<void>, success = "") {
    setError("");
    setMessage("");
    try {
      await action();
      if (success) setMessage(success);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function saveGroup() {
    const path = groupId ? `/api/admin/v1/groups/${groupId}` : "/api/admin/v1/groups";
    const data = await adminApi.request<any>(path, {
      method: groupId ? "PATCH" : "POST",
      body: JSON.stringify({ name, description, status, priority: Number(priority) || 100, model_multiplier: multiplier, rpm_limit: rpmLimit ? Number(rpmLimit) : null, monthly_usd_limit: monthlyLimit || null }),
    });
    if (!groupId) setGroupId(data.id);
    await load();
    setGroupsModal(null);
  }

  async function addMember() {
    await adminApi.request(`/api/admin/v1/groups/${groupId}/members`, { method: "POST", body: JSON.stringify({ user_id: memberUserId, role: memberRole }) });
    await load();
    setGroupsModal(null);
  }

  async function addPermission() {
    await adminApi.request(`/api/admin/v1/groups/${groupId}/model-permissions`, { method: "POST", body: JSON.stringify({ model_name: permissionModel, endpoint: permissionEndpoint, permission, rpm_limit: permissionRPM ? Number(permissionRPM) : null, price_multiplier: permissionMultiplier || null }) });
    await load();
    setGroupsModal(null);
  }

  async function previewPolicy() {
    const query = new URLSearchParams({ user_id: previewUserId, model: previewModel });
    if (previewEndpoint) query.set("endpoint", previewEndpoint);
    setPreview(await adminApi.request<any>(`/api/admin/v1/groups/effective-policy?${query.toString()}`));
    setGroupsModal(null);
  }

  function newGroup() {
    setGroupId("");
    setName("");
    setDescription("");
    setStatus("active");
    setPriority("100");
    setMultiplier("1");
    setRpmLimit("");
    setMonthlyLimit("");
    setGroupsModal("group");
  }

  function selectGroup(row: any) {
    setGroupId(row.id);
    setName(row.name ?? "");
    setDescription(row.description ?? "");
    setStatus(row.status ?? "active");
    setPriority(String(row.priority ?? 100));
    setMultiplier(row.model_multiplier ?? "1");
    setRpmLimit(row.rpm_limit ? String(row.rpm_limit) : "");
    setMonthlyLimit(row.monthly_usd_limit ?? "");
    setGroupsModal("group");
  }

  return (
    <div className="stack">
      {(error || message) && <section className="panel">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</section>}
      <section className="panel">
        <SectionHeader title="用户分组" action={<button onClick={newGroup}>新建分组</button>} />
        <DataTable rows={groups} columns={["name", "description", "status", "priority", "model_multiplier", "rpm_limit", "monthly_usd_limit", "members", "permissions", "created_at"]} action={(row) => <button onClick={() => selectGroup(row)}>编辑</button>} />
      </section>
      <section className="panel">
        <SectionHeader title="成员与模型权限" action={<div className="actions"><button onClick={() => setGroupsModal("member")} disabled={!groupId}>添加成员</button><button onClick={() => setGroupsModal("permission")} disabled={!groupId}>添加权限</button><button onClick={() => setGroupsModal("preview")}>预览策略</button></div>} />
        {preview && <pre>{JSON.stringify(preview, null, 2)}</pre>}
      </section>
      <ActionModal open={groupsModal === "group"} title={groupId ? "编辑分组" : "新建分组"} size="md" onClose={() => setGroupsModal(null)} footer={<><button onClick={() => setGroupsModal(null)}>取消</button><button className="primary" onClick={() => runAction(saveGroup, "分组已保存")} disabled={!name}>保存分组</button></>}>
        <div className="form-grid">
          <input value={name} onChange={(event) => setName(event.target.value)} placeholder="名称" />
          <input value={description} onChange={(event) => setDescription(event.target.value)} placeholder="描述" />
          <select value={status} onChange={(event) => setStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option></select>
          <input value={priority} onChange={(event) => setPriority(event.target.value)} placeholder="优先级，数值越小越优先" />
          <input value={multiplier} onChange={(event) => setMultiplier(event.target.value)} placeholder="模型倍率" />
          <input value={rpmLimit} onChange={(event) => setRpmLimit(event.target.value)} placeholder="RPM 限制" />
          <input value={monthlyLimit} onChange={(event) => setMonthlyLimit(event.target.value)} placeholder="每月 USD 限制" />
        </div>
      </ActionModal>
      <ActionModal open={groupsModal === "member"} title="添加成员" size="sm" onClose={() => setGroupsModal(null)} footer={<><button onClick={() => setGroupsModal(null)}>取消</button><button className="primary" onClick={() => runAction(addMember, "成员已保存")} disabled={!groupId || !memberUserId}>保存成员</button></>}>
        <div className="form-grid single">
          <select value={memberUserId} onChange={(event) => setMemberUserId(event.target.value)}><option value="">选择用户</option>{users.map((user) => <option key={user.id} value={user.id}>{user.email}</option>)}</select>
          <input value={memberRole} onChange={(event) => setMemberRole(event.target.value)} placeholder="角色" />
        </div>
      </ActionModal>
      <ActionModal open={groupsModal === "permission"} title="添加模型权限" size="md" onClose={() => setGroupsModal(null)} footer={<><button onClick={() => setGroupsModal(null)}>取消</button><button className="primary" onClick={() => runAction(addPermission, "模型权限已保存")} disabled={!groupId || !permissionModel}>保存权限</button></>}>
        <div className="form-grid">
          <select value={permissionModel} onChange={(event) => setPermissionModel(event.target.value)}><option value="">选择模型</option>{models.map((model) => <option key={model.model_name} value={model.model_name}>{model.model_name}</option>)}</select>
          <input value={permissionEndpoint} onChange={(event) => setPermissionEndpoint(event.target.value)} placeholder="端点，可空" />
          <select value={permission} onChange={(event) => setPermission(event.target.value)}><option value="allow">允许</option><option value="deny">拒绝</option></select>
          <input value={permissionRPM} onChange={(event) => setPermissionRPM(event.target.value)} placeholder="权限 RPM 限制" />
          <input value={permissionMultiplier} onChange={(event) => setPermissionMultiplier(event.target.value)} placeholder="权限价格倍率，可空" />
        </div>
      </ActionModal>
      <ActionModal open={groupsModal === "preview"} title="有效策略预览" size="md" onClose={() => setGroupsModal(null)} footer={<><button onClick={() => setGroupsModal(null)}>取消</button><button className="primary" onClick={() => runAction(previewPolicy, "策略已刷新")} disabled={!previewUserId || !previewModel}>预览策略</button></>}>
        <div className="form-grid">
          <select value={previewUserId} onChange={(event) => setPreviewUserId(event.target.value)}><option value="">选择用户</option>{users.map((user) => <option key={user.id} value={user.id}>{user.email}</option>)}</select>
          <select value={previewModel} onChange={(event) => setPreviewModel(event.target.value)}><option value="">选择模型</option>{models.map((model) => <option key={model.model_name} value={model.model_name}>{model.model_name}</option>)}</select>
          <input value={previewEndpoint} onChange={(event) => setPreviewEndpoint(event.target.value)} placeholder="端点" />
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminOps({ role, onNavigate }: { role: AdminRole; onNavigate: (target: AdminTarget) => void }) {
  const [overview, setOverview] = useState<any>(null);
  const [timeRange, setTimeRange] = useState("24h");
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setLoading(true);
    setError("");
    try {
      const next = await adminApi.request<any>(`/api/admin/v1/ops/overview?time_range=${encodeURIComponent(timeRange)}`);
      setOverview(next);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, [timeRange]);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = window.setInterval(() => void load(), 30_000);
    return () => window.clearInterval(timer);
  }, [autoRefresh, timeRange]);

  const summary = overview?.summary ?? {};
  const readiness = overview?.readiness ?? {};
  const checks = readiness.checks ?? {};
  const readyStatus = readiness.status ?? "unknown";
  const trend = overview?.throughput_trend ?? [];
  const latency = overview?.latency_distribution ?? [];
  const errors = overview?.error_distribution ?? [];
  const events = overview?.events ?? {};
  const canManagePlatform = role === "platform_owner";

  return (
    <div className="stack">
      <PageNotice error={error} />
      <section className="panel">
        <div className="row filters">
          <select value={timeRange} onChange={(event) => setTimeRange(event.target.value)}>
            <option value="1h">1 小时</option>
            <option value="6h">6 小时</option>
            <option value="24h">24 小时</option>
            <option value="7d">7 天</option>
          </select>
          <button onClick={() => setAutoRefresh(!autoRefresh)}>{autoRefresh ? "自动刷新开" : "自动刷新关"}</button>
          <button onClick={() => void load()} disabled={loading}><RefreshCcw size={16} /> {loading ? "刷新中" : "刷新"}</button>
        </div>
      </section>
      <div className="metric-grid ops-metrics">
        <Metric label="Readiness" value={readyStatus} detail={`DB ${checks.database ?? "unknown"} · Redis ${checks.redis ?? "unknown"} · Config ${checks.config ?? "unknown"}`} />
        <Metric label="请求 / 失败 / 拒绝" value={`${num(summary.total_requests)} / ${num(summary.failed_requests)} / ${num(summary.rejected_requests)}`} detail={`${timeRangeLabel(timeRange)} 窗口`} />
        <Metric label="平均延迟" value={`${num(summary.avg_duration_ms)} ms`} detail={`1h 上游错误 ${num(summary.upstream_error_last_hour)} · 拒绝 ${num(summary.upstream_rejected_last_hour)}`} />
        <Metric label="账号池降级" value={`${num(summary.circuit_open_accounts)} / ${num(summary.cooldown_accounts)}`} detail="熔断开启 / 运行时冷却" />
        <Metric label="待处理事件" value={`${num(summary.payment_attention)} / ${num(summary.notification_attention)}`} detail="支付 / 通知" />
        <Metric label="成本" value={`$${money(summary.actual_cost)}`} detail={`Tokens ${num(summary.input_tokens)} / ${num(summary.output_tokens)}`} />
      </div>

      <div className="ops-grid">
        <section className="panel ops-panel wide">
          <SectionHeader title="吞吐趋势" description="按小时聚合请求量、失败量、拒绝量和平均延迟。" action={<span className="env-pill"><span /> {overview?.refreshed_at ? new Date(overview.refreshed_at).toLocaleTimeString() : "未刷新"}</span>} />
          <OpsTrendChart rows={trend} />
        </section>
        <section className="panel ops-panel">
          <SectionHeader title="延迟分布" description="只统计已记录耗时的请求。" />
          <OpsBars rows={latency} valueKey="count" />
        </section>
        <section className="panel ops-panel">
          <SectionHeader title="错误分布" description="失败和拒绝请求的 error_code。" />
          <OpsBars rows={errors} valueKey="count" />
        </section>
      </div>

      <section className="panel">
        <SectionHeader
          title="账号池健康"
          description="优先显示熔断、冷却、耗尽和仍有活跃请求的账号。"
          action={<div className="actions"><button onClick={() => onNavigate({ view: "pool", tab: "quality" })}>质量评分</button><button onClick={() => onNavigate({ view: "pool", tab: "events" })}>策略事件</button></div>}
        />
        <DataTable rows={overview?.account_health ?? []} columns={["name", "provider_name", "channel_name", "status", "active_requests", "circuit_state", "cooldown_until", "last_error", "updated_at"]} />
      </section>

      <section className="panel">
        <SectionHeader
          title="近期失败请求"
          description="用于快速定位上游错误、风控拒绝和余额拒绝。"
          action={<button onClick={() => onNavigate({ view: "usage" })}>打开用量</button>}
        />
        <DataTable rows={overview?.recent_failures ?? []} columns={["request_id", "requested_model", "endpoint", "status", "error_code", "upstream_status", "duration_ms", "actual_cost", "created_at"]} />
      </section>

      <div className="ops-grid">
        <section className="panel">
          <SectionHeader title="支付事件" description="pending、processing、failed 需要关注。" action={canManagePlatform ? <button onClick={() => onNavigate({ view: "billing" })}>商业化</button> : undefined} />
          <DataTable rows={events.payments ?? []} columns={["status", "event_type", "provider", "order_id", "attempts", "last_error", "created_at"]} />
        </section>
        <section className="panel">
          <SectionHeader title="通知事件" description="pending、failed、suppressed 需要关注。" action={canManagePlatform ? <button onClick={() => onNavigate({ view: "controls" })}>通知控制</button> : undefined} />
          <DataTable rows={events.notifications ?? []} columns={["status", "severity", "event_type", "title", "attempts", "last_error", "created_at"]} />
        </section>
      </div>

      <div className="ops-grid">
        <section className="panel">
          <SectionHeader title="风控事件" description="最近命中的阻断、限速和标记规则。" action={canManagePlatform ? <button onClick={() => onNavigate({ view: "risk" })}>风控</button> : undefined} />
          <DataTable rows={events.risk ?? []} columns={["request_id", "rule_type", "action", "severity", "target", "matched_value", "created_at"]} />
        </section>
        <section className="panel">
          <SectionHeader title="账号池策略事件" description="自动或手动质量动作的最近记录。" action={<button onClick={() => onNavigate({ view: "pool", tab: "events" })}>事件</button>} />
          <DataTable rows={events.pool ?? []} columns={["account_name", "provider_name", "channel_name", "event_type", "action", "previous_status", "next_status", "reason", "created_at"]} />
        </section>
      </div>

      <section className="panel">
        <SectionHeader title="运行摘要" description="当前看板聚合值，便于复制到排障记录。" />
        <DataTable rows={runtimeRows(summary)} columns={["metric", "value"]} />
      </section>
    </div>
  );
}

function OpsTrendChart({ rows }: { rows: any[] }) {
  const maxValue = Math.max(1, ...rows.map((row) => Math.max(Number(row.total ?? 0), Number(row.failed ?? 0), Number(row.rejected ?? 0))));
  if (!rows.length) return <div className="empty">暂无趋势数据</div>;
  return (
    <div className="ops-trend">
      {rows.map((row, index) => {
        const total = Number(row.total ?? 0);
        const failed = Number(row.failed ?? 0);
        const rejected = Number(row.rejected ?? 0);
        const label = trendBucketLabel(row.bucket, rows.length, index);
        return (
          <div className="ops-trend-point" key={row.bucket ?? index}>
            <div className="ops-trend-bars" title={`${label} 请求 ${total}，失败 ${failed}，拒绝 ${rejected}`}>
              <span className="total" style={{ height: `${Math.max(3, (total / maxValue) * 100)}%` }} />
              <span className="failed" style={{ height: `${Math.max(3, (failed / maxValue) * 100)}%` }} />
              <span className="rejected" style={{ height: `${Math.max(3, (rejected / maxValue) * 100)}%` }} />
            </div>
            <small>{label}</small>
          </div>
        );
      })}
    </div>
  );
}

function OpsBars({ rows, valueKey }: { rows: any[]; valueKey: string }) {
  const maxValue = Math.max(1, ...rows.map((row) => Number(row[valueKey] ?? 0)));
  if (!rows.length) return <div className="empty">暂无数据</div>;
  return (
    <div className="ops-bars">
      {rows.map((row, index) => {
        const value = Number(row[valueKey] ?? 0);
        return (
          <div className="ops-bar-row" key={row.id ?? row.label ?? index}>
            <span>{row.label ?? row.id ?? "unknown"}</span>
            <div><i style={{ width: `${Math.max(3, (value / maxValue) * 100)}%` }} /></div>
            <strong>{value}</strong>
          </div>
        );
      })}
    </div>
  );
}

function num(value: unknown) {
  const n = Number(value ?? 0);
  if (!Number.isFinite(n)) return "0";
  return n.toLocaleString();
}

function money(value: unknown) {
  const n = Number(value ?? 0);
  if (!Number.isFinite(n)) return "0.0000";
  return n.toFixed(4);
}

function timeRangeLabel(value: string) {
  switch (value) {
    case "1h": return "1 小时";
    case "6h": return "6 小时";
    case "7d": return "7 天";
    default: return "24 小时";
  }
}

function trendBucketLabel(value: string, bucketCount: number, index: number) {
  if (!value) return String(index + 1);
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(index + 1);
  if (bucketCount <= 8) return date.toLocaleDateString([], { month: "2-digit", day: "2-digit" });
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function AdminRiskControls() {
  const [rules, setRules] = useState<any[]>([]);
  const [events, setEvents] = useState<any[]>([]);
  const [ruleId, setRuleId] = useState("");
  const [ruleType, setRuleType] = useState("sensitive_word");
  const [name, setName] = useState("");
  const [pattern, setPattern] = useState("");
  const [action, setAction] = useState("flag");
  const [severity, setSeverity] = useState("warning");
  const [status, setStatus] = useState("active");
  const [metadata, setMetadata] = useState("{}");
  const [eventAction, setEventAction] = useState("");
  const [eventUserId, setEventUserId] = useState("");
  const [riskModal, setRiskModal] = useState<null | "rule">(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const query = new URLSearchParams({ limit: "100" });
    if (eventAction) query.set("action", eventAction);
    if (eventUserId.trim()) query.set("user_id", eventUserId.trim());
    const [nextRules, nextEvents] = await Promise.all([
      adminApi.request<any[]>("/api/admin/v1/risk-controls"),
      adminApi.request<any[]>(`/api/admin/v1/risk-events?${query.toString()}`),
    ]);
    setRules(nextRules);
    setEvents(nextEvents);
  }

  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);

  async function save() {
    const path = ruleId ? `/api/admin/v1/risk-controls/${ruleId}` : "/api/admin/v1/risk-controls";
    await adminApi.request(path, { method: ruleId ? "PATCH" : "POST", body: JSON.stringify({ rule_type: ruleType, name, pattern, action, severity, status, metadata: parseJSONObject(metadata) }) });
    setMessage("规则已保存");
    await load();
    setRiskModal(null);
  }

  function newRule() {
    setRuleId("");
    setRuleType("sensitive_word");
    setName("");
    setPattern("");
    setAction("flag");
    setSeverity("warning");
    setStatus("active");
    setMetadata("{}");
    setRiskModal("rule");
  }

  function select(row: any) {
    setRuleId(row.id);
    setRuleType(row.rule_type ?? "sensitive_word");
    setName(row.name ?? "");
    setPattern(row.pattern ?? "");
    setAction(row.action ?? "flag");
    setSeverity(row.severity ?? "warning");
    setStatus(row.status ?? "active");
    setMetadata(jsonText(row.metadata));
    setRiskModal("rule");
  }

  return (
    <div className="stack">
      {(error || message) && <section className="panel">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</section>}
      <section className="panel">
        <SectionHeader title="风控规则" action={<button onClick={newRule}>新建规则</button>} />
        <DataTable rows={rules} columns={["rule_type", "name", "pattern", "action", "severity", "status", "metadata", "created_at"]} action={(row) => <button onClick={() => select(row)}>编辑</button>} />
      </section>
      <section className="panel">
        <h2>风险事件</h2>
        <div className="form-grid">
          <select value={eventAction} onChange={(event) => setEventAction(event.target.value)}><option value="">全部动作</option><option value="flag">标记</option><option value="block">阻断</option><option value="throttle">限速</option></select>
          <input value={eventUserId} onChange={(event) => setEventUserId(event.target.value)} placeholder="用户 ID，可空" />
          <button onClick={() => load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))}>筛选</button>
        </div>
        <DataTable rows={events} columns={["request_id", "user_id", "api_key_id", "rule_type", "action", "severity", "target", "matched_value", "metadata", "created_at"]} />
      </section>
      <ActionModal
        open={riskModal === "rule"}
        title={ruleId ? "编辑风控规则" : "新建风控规则"}
        size="md"
        onClose={() => setRiskModal(null)}
        footer={<><button onClick={() => setRiskModal(null)}>取消</button><button className="primary" onClick={() => save().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))} disabled={!name}>保存规则</button></>}
      >
        <div className="form-grid">
          <select value={ruleType} onChange={(event) => setRuleType(event.target.value)}><option value="sensitive_word">敏感词</option><option value="ssrf_target">目标限制</option><option value="request_limit">请求限制</option><option value="bot_protection">Bot 防护</option><option value="abuse_pattern">异常模式</option></select>
          <input value={name} onChange={(event) => setName(event.target.value)} placeholder="名称" />
          <input value={pattern} onChange={(event) => setPattern(event.target.value)} placeholder="匹配内容" />
          <select value={action} onChange={(event) => setAction(event.target.value)}><option value="flag">标记</option><option value="block">阻断</option><option value="throttle">限速</option></select>
          <select value={severity} onChange={(event) => setSeverity(event.target.value)}><option value="info">信息</option><option value="warning">警告</option><option value="critical">严重</option></select>
          <select value={status} onChange={(event) => setStatus(event.target.value)}><option value="active">启用</option><option value="disabled">停用</option></select>
          <textarea value={metadata} onChange={(event) => setMetadata(event.target.value)} placeholder="元数据 JSON" rows={4} />
        </div>
      </ActionModal>
    </div>
  );
}

export function AdminAudit() {
  const [rows, setRows] = useState<any[]>([]);
  const [actorId, setActorId] = useState("");
  const [action, setAction] = useState("");
  const [targetType, setTargetType] = useState("");
  const [error, setError] = useState("");
  async function load() {
    const query = new URLSearchParams({ limit: "200" });
    if (actorId) query.set("actor_id", actorId);
    if (action) query.set("action", action);
    if (targetType) query.set("target_type", targetType);
    setRows(await adminApi.request<any[]>(`/api/admin/v1/audit?${query.toString()}`));
  }
  useEffect(() => { void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。")); }, []);
  return (
    <div className="stack">
      <section className="panel">
        <SectionHeader title="审计" description="记录后台写操作和敏感测试动作。" />
        {error && <div className="error">{error}</div>}
        <div className="row filters">
          <input value={actorId} onChange={(event) => setActorId(event.target.value)} placeholder="操作者 ID" />
          <input value={action} onChange={(event) => setAction(event.target.value)} placeholder="动作，例如 user.update" />
          <select value={targetType} onChange={(event) => setTargetType(event.target.value)}>
            <option value="">任意目标</option>
            <option value="user">用户</option>
            <option value="system_setting">系统设置</option>
            <option value="model">模型</option>
            <option value="channel">通道</option>
            <option value="account">账号</option>
            <option value="order">订单</option>
          </select>
          <button onClick={() => load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))}>筛选</button>
        </div>
        <DataTable rows={rows} columns={["actor_user_id", "actor_type", "action", "target_type", "target_id", "ip_address", "created_at"]} />
      </section>
    </div>
  );
}

function runtimeRows(metrics: Record<string, string>) {
  const labels: Record<string, string> = {
    total_requests: "请求数",
    success_requests: "成功请求",
    failed_requests: "失败请求",
    rejected_requests: "拒绝请求",
    upstream_rejected_last_hour: "1h 上游拒绝",
    upstream_error_last_hour: "1h 上游错误",
    input_tokens: "输入 tokens",
    output_tokens: "输出 tokens",
    actual_cost: "实际成本",
    avg_duration_ms: "平均延迟 ms",
    runtime_active_accounts: "有并发账号",
    active_requests: "活跃请求",
    cooldown_accounts: "运行时冷却账号",
    circuit_open_accounts: "熔断开启账号",
    circuit_half_open_accounts: "半开熔断账号",
    total_accounts: "账号总数",
    active_accounts: "启用账号",
    cooldown_status_accounts: "状态冷却账号",
    disabled_accounts: "停用账号",
    exhausted_accounts: "耗尽账号",
    total_channels: "通道总数",
    active_channels: "启用通道",
    cooldown_channels: "冷却通道",
    disabled_channels: "停用通道",
    payment_attention: "支付待关注",
    notification_attention: "通知待关注",
    risk_events: "风控事件",
    pool_events: "号池事件",
    failed_settlements: "结算异常",
  };
  return Object.entries(labels).map(([key, label]) => ({ id: key, metric: label, value: metrics[key] ?? "0" }));
}

function parseJSONObject(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return {};
  const parsed = JSON.parse(trimmed) as unknown;
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("JSON 必须是对象。");
  }
  return parsed as Record<string, unknown>;
}

function splitCSV(value: string) {
  return value.split(/[,\n]/).map((item) => item.trim()).filter(Boolean);
}

function parseLooseJSONValue(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return "";
  try {
    return JSON.parse(trimmed) as unknown;
  } catch {
    return value;
  }
}

function deepMergePlainObjects(base: Record<string, unknown>, patch: Record<string, unknown>): Record<string, unknown> {
  const next: Record<string, unknown> = { ...base };
  for (const [key, value] of Object.entries(patch)) {
    if (isPlainObject(value) && isPlainObject(next[key])) {
      next[key] = deepMergePlainObjects(next[key] as Record<string, unknown>, value as Record<string, unknown>);
    } else {
      next[key] = value;
    }
  }
  return next;
}

function isPlainObject(value: unknown) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function mergeModelMetadataFields(metadata: Record<string, unknown>, fields: Record<string, unknown>) {
  const next = { ...metadata };
  for (const [key, value] of Object.entries(fields)) {
    if (Array.isArray(value)) {
      if (value.length) next[key] = value;
      else delete next[key];
      continue;
    }
    if (typeof value === "string") {
      const trimmed = value.trim();
      if (trimmed) next[key] = trimmed;
      else delete next[key];
      continue;
    }
    if (value !== undefined && value !== null) next[key] = value;
  }
  return next;
}

function mergeAccountMetadataFields(metadata: Record<string, unknown>, poolGroup: string, routeTags: string[]) {
  const next = { ...metadata };
  if (poolGroup.trim()) next.pool_group = poolGroup.trim();
  else delete next.pool_group;
  if (routeTags.length) next.route_tags = routeTags;
  else delete next.route_tags;
  return next;
}

function metadataStringField(metadata: unknown, key: string) {
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) return "";
  const value = (metadata as Record<string, unknown>)[key];
  return typeof value === "string" ? value : "";
}

function metadataStringArrayField(metadata: unknown, key: string) {
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) return [];
  const value = (metadata as Record<string, unknown>)[key];
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string");
}

function jsonText(value: unknown) {
  if (!value || value === "") return "{}";
  return JSON.stringify(value, null, 2);
}

function proxyProtocol(proxyUrl: string) {
  const normalized = proxyUrl.trim().toLowerCase();
  if (normalized === "direct" || normalized === "none") return "direct";
  try {
    const protocol = new URL(proxyUrl).protocol.replace(":", "");
    return protocol || "";
  } catch {
    return "";
  }
}

function proxyQualityIssueText(source: any) {
  if (!source) return "";
  const direct = String(source.problem ?? source.quality_problem ?? "").trim();
  if (direct) return direct;
  const items = Array.isArray(source.items) ? source.items : Array.isArray(source.quality_items) ? source.quality_items : [];
  const hardIssues = items.filter((item: any) => ["fail", "challenge"].includes(item?.status));
  const softIssues = hardIssues.length ? hardIssues : items.filter((item: any) => item?.status === "warn");
  if (!softIssues.length) return "";
  const visible = softIssues.slice(0, 3).map(proxyQualityItemIssueText);
  if (softIssues.length > 3) visible.push(`还有 ${softIssues.length - 3} 项异常`);
  return visible.join("；");
}

function proxyQualityItemIssueText(item: any) {
  const target = proxyQualityTargetLabel(item?.target);
  const status = proxyQualityItemStatusLabel(item?.status);
  const message = String(item?.message || (item?.http_status ? `HTTP ${item.http_status}` : "未返回具体原因")).trim();
  return `${target} ${status}：${message}`;
}

function proxyQualityItemRows(report: any) {
  const items = Array.isArray(report?.items) ? report.items : [];
  return items.map((item: any) => ({
    ...item,
    target: proxyQualityTargetLabel(item.target),
    status: proxyQualityItemStatusLabel(item.status),
  }));
}

function proxyQualityTargetLabel(value: string | undefined) {
  const labels: Record<string, string> = {
    base_connectivity: "基础连通",
    openai: "OpenAI",
    anthropic: "Anthropic",
    gemini: "Gemini",
    custom: "自定义目标",
  };
  return labels[value ?? ""] ?? value ?? "-";
}

function proxyQualityItemStatusLabel(value: string | undefined) {
  const labels: Record<string, string> = {
    pass: "通过",
    warn: "告警",
    fail: "失败",
    challenge: "挑战",
  };
  return labels[value ?? ""] ?? value ?? "-";
}

function qualityLabel(value: string | undefined) {
  const labels: Record<string, string> = {
    healthy: "健康",
    warn: "告警",
    failed: "失败",
    challenge: "挑战",
  };
  return labels[value ?? ""] ?? value ?? "-";
}

function firstAbility(row: any) {
  return Array.isArray(row?.abilities) ? row.abilities[0] : undefined;
}

function providerTypeLabel(value: string) {
  const labels: Record<string, string> = {
    openai_compatible: "OpenAI 兼容",
    openai: "OpenAI",
    anthropic_compatible: "Anthropic 兼容",
    anthropic: "Anthropic",
    github_copilot: "GitHub Copilot",
    gemini: "Gemini",
    gemini_openai_compatible: "Gemini OpenAI 兼容",
    gemini_cli: "Gemini CLI",
    antigravity: "Antigravity",
    kiro: "Kiro",
    windsurf_codeium: "Windsurf Codeium",
    codex_compatible: "Codex 兼容",
    cli_openai_compatible: "CLI OpenAI 兼容",
    claude_compatible: "Claude 兼容",
    gemini_compatible: "Gemini 兼容",
  };
  return labels[value] ?? value;
}

function oauthProgress(row: any) {
  return row?.oauth_progress || row?.result?.oauth_progress || {};
}

function initials(value: string) {
  return value.slice(0, 2).toUpperCase();
}

function runtimeStatusLabel(status: "operational" | "degraded") {
  return status === "degraded" ? "降级" : "正常";
}
