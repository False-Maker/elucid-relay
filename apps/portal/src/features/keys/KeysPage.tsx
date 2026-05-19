import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import {
  Key, Plus, Copy, Check, Trash2, ToggleLeft, ToggleRight,
  Loader2, ArrowRight, Shield, Clock, Globe,
} from "lucide-react";
import { motion, AnimatePresence } from "framer-motion";
import { toast } from "sonner";
import { useApiKeys, useCreateApiKey, useToggleApiKey, useDeleteApiKey, type ApiKeyRecord, type CreateKeyResponse } from "@/lib/queries/keys";
import { useModels } from "@/lib/queries/models";
import { StatusBadge, Skeleton, EmptyState, CopyButton, SectionHeader } from "@/components/shared";
import { RELAY_API_BASE } from "@/lib/api-client";
import { cn } from "@/lib/utils";

const createKeySchema = z.object({
  name: z.string().min(1, "请输入密钥名称"),
  routing_mode: z.enum(["pool", "byo"]),
  model_scope: z.string().optional(),
  ip_allowlist: z.string().optional(),
  expires_at: z.string().optional(),
});
type CreateKeyValues = z.infer<typeof createKeySchema>;

export function KeysPage() {
  const keys = useApiKeys();
  const [showCreate, setShowCreate] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<string | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const activeCount = keys.data?.filter((k) => k.status === "active").length ?? 0;
  const totalCount = keys.data?.length ?? 0;

  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25 }}
      className="space-y-6"
    >
      {/* Header */}
      <SectionHeader
        title={`API 密钥管理`}
        description={keys.data ? `${activeCount} 个活跃 / 共 ${totalCount} 个` : "用于 CLI 和 API 调用"}
        action={
          keys.data && keys.data.length > 0 ? (
            <button
              onClick={() => { setShowCreate(true); setCreatedSecret(null); setAdvancedOpen(false); }}
              className="flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
            >
              <Plus size={14} /> 创建密钥
            </button>
          ) : undefined
        }
      />

      {/* Created secret banner */}
      <AnimatePresence>
        {createdSecret && (
          <motion.div
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            exit={{ opacity: 0, height: 0 }}
            className="overflow-hidden"
          >
            <div className="rounded-xl border border-success/20 bg-success/5 p-4">
              <div className="mb-1 flex items-center gap-2 text-sm font-semibold text-success">
                <Check size={16} /> 密钥已创建
              </div>
              <p className="mb-3 text-xs text-muted">
                请立即复制并妥善保管，密钥只显示一次。
              </p>
              <div className="flex items-center gap-2 rounded-lg bg-surface px-3 py-2">
                <code className="flex-1 break-all font-mono text-xs">{createdSecret}</code>
                <CopyButton value={createdSecret} label="复制" />
              </div>
              <div className="mt-3 rounded-lg bg-surface px-3 py-2">
                <div className="mb-1 text-xs font-medium text-muted">快速配置</div>
                <code className="block font-mono text-xs text-foreground">
                  export ANTHROPIC_API_KEY={createdSecret}
                </code>
              </div>
            </div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* Loading */}
      {keys.isLoading && (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full rounded-xl" />
          ))}
        </div>
      )}

      {/* Empty state */}
      {keys.data && keys.data.length === 0 && !showCreate && (
        <div className="rounded-xl border border-border bg-surface px-6 py-12 text-center">
          <div className="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-surface-2">
            <Key size={28} className="text-bronze" />
          </div>
          <h3 className="text-base font-semibold">还没有 API 密钥</h3>
          <p className="mt-1 text-sm text-muted">
            创建您的第一个密钥，开始使用 API 服务
          </p>
          <button
            onClick={() => { setShowCreate(true); setAdvancedOpen(false); }}
            className="mt-4 inline-flex items-center gap-1.5 rounded-lg bg-primary px-4 py-2.5 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            <Plus size={16} /> 创建 API 密钥
          </button>

          {/* Step guide */}
          <div className="mt-8 grid grid-cols-3 gap-4 text-left">
            {[
              { num: 1, title: "创建密钥", desc: "点击上方按钮创建新密钥" },
              { num: 2, title: "复制保存", desc: "妥善保管您的完整密钥" },
              { num: 3, title: "配置环境", desc: "将密钥配置为环境变量" },
            ].map((s) => (
              <div key={s.num} className="flex items-start gap-2.5">
                <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-bronze/10 text-xs font-semibold text-bronze">
                  {s.num}
                </div>
                <div>
                  <div className="text-sm font-medium">{s.title}</div>
                  <div className="text-xs text-muted">{s.desc}</div>
                </div>
              </div>
            ))}
          </div>

          {/* Quick config */}
          <div className="mt-6 rounded-lg bg-surface-2 px-4 py-3 text-left">
            <div className="mb-1 flex items-center justify-between">
              <span className="text-xs font-medium text-muted">快速配置</span>
              <CopyButton value={`export ANTHROPIC_API_KEY=sk-relay_your-key-here`} />
            </div>
            <code className="block font-mono text-xs">
              export ANTHROPIC_API_KEY=sk-relay_your-key-here
            </code>
          </div>
        </div>
      )}

      {/* Create form */}
      <AnimatePresence>
        {showCreate && (
          <motion.div
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            exit={{ opacity: 0, height: 0 }}
            className="overflow-hidden"
          >
            <CreateKeyForm
              onCreated={(resp) => {
                setCreatedSecret(resp.secret);
                setShowCreate(false);
                toast.success("密钥已创建");
              }}
              onCancel={() => setShowCreate(false)}
              advancedOpen={advancedOpen}
              setAdvancedOpen={setAdvancedOpen}
            />
          </motion.div>
        )}
      </AnimatePresence>

      {/* Key list */}
      {keys.data && keys.data.length > 0 && (
        <div className="space-y-2">
          {keys.data.map((key) => (
            <KeyCard key={key.id} apiKey={key} />
          ))}
        </div>
      )}
    </motion.div>
  );
}

/* ─── Create Key Form ─── */

function CreateKeyForm({
  onCreated,
  onCancel,
  advancedOpen,
  setAdvancedOpen,
}: {
  onCreated: (resp: CreateKeyResponse) => void;
  onCancel: () => void;
  advancedOpen: boolean;
  setAdvancedOpen: (v: boolean) => void;
}) {
  const createKey = useCreateApiKey();
  const models = useModels();

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<CreateKeyValues>({
    resolver: zodResolver(createKeySchema),
    defaultValues: { routing_mode: "pool" },
  });

  async function onSubmit(values: CreateKeyValues) {
    const payload: any = { name: values.name, routing_mode: values.routing_mode };
    if (values.model_scope) {
      payload.model_scope = values.model_scope.split(",").map((s: string) => s.trim()).filter(Boolean);
    }
    if (values.ip_allowlist) {
      payload.ip_allowlist = values.ip_allowlist.split(",").map((s: string) => s.trim()).filter(Boolean);
    }
    if (values.expires_at) {
      payload.expires_at = values.expires_at;
    }
    const resp = await createKey.mutateAsync(payload);
    onCreated(resp);
  }

  return (
    <form
      onSubmit={handleSubmit(onSubmit)}
      className="rounded-xl border border-border bg-surface p-4 space-y-4"
    >
      <h3 className="text-sm font-semibold">创建新密钥</h3>

      <div>
        <label className="mb-1 block text-xs font-medium text-muted">密钥名称</label>
        <input
          {...register("name")}
          placeholder="例如 my-dev-key"
          className="w-full rounded-lg border border-input bg-surface px-3 py-2 text-sm outline-none focus:border-bronze-soft focus:ring-2 focus:ring-ring"
        />
        {errors.name && <p className="mt-1 text-xs text-destructive">{errors.name.message}</p>}
      </div>

      <div>
        <label className="mb-1 block text-xs font-medium text-muted">路由模式</label>
        <div className="flex gap-2">
          {(["pool", "byo"] as const).map((mode) => (
            <label key={mode} className="flex items-center gap-1.5 text-sm">
              <input type="radio" value={mode} {...register("routing_mode")} className="accent-bronze" />
              {mode === "pool" ? "共享账号池" : "自带账号 (BYO)"}
            </label>
          ))}
        </div>
      </div>

      {/* Advanced toggle */}
      <button
        type="button"
        onClick={() => setAdvancedOpen(!advancedOpen)}
        className="flex items-center gap-1 text-xs text-muted hover:text-foreground transition-colors"
      >
        <Shield size={12} />
        {advancedOpen ? "收起高级选项" : "高级选项 (模型范围、IP 白名单、过期时间)"}
      </button>

      <AnimatePresence>
        {advancedOpen && (
          <motion.div
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            exit={{ opacity: 0, height: 0 }}
            className="space-y-3 overflow-hidden"
          >
            <div>
              <label className="mb-1 block text-xs font-medium text-muted">
                <Globe size={11} className="mr-1 inline" />
                模型范围 (逗号分隔，留空表示全部)
              </label>
              <input
                {...register("model_scope")}
                placeholder="claude-sonnet-4-6, gpt-4o"
                className="w-full rounded-lg border border-input bg-surface px-3 py-2 text-sm outline-none focus:border-bronze-soft focus:ring-2 focus:ring-ring"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-muted">
                <Shield size={11} className="mr-1 inline" />
                IP 白名单 (逗号分隔)
              </label>
              <input
                {...register("ip_allowlist")}
                placeholder="192.168.1.0/24, 10.0.0.1"
                className="w-full rounded-lg border border-input bg-surface px-3 py-2 text-sm outline-none focus:border-bronze-soft focus:ring-2 focus:ring-ring"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-muted">
                <Clock size={11} className="mr-1 inline" />
                过期时间
              </label>
              <input
                {...register("expires_at")}
                type="datetime-local"
                className="w-full rounded-lg border border-input bg-surface px-3 py-2 text-sm outline-none focus:border-bronze-soft focus:ring-2 focus:ring-ring"
              />
            </div>
          </motion.div>
        )}
      </AnimatePresence>

      <div className="flex gap-2">
        <button
          type="submit"
          disabled={createKey.isPending}
          className="flex items-center gap-1.5 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-60"
        >
          {createKey.isPending ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
          创建
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="rounded-lg border border-border px-4 py-2 text-sm text-muted hover:bg-surface-2 transition-colors"
        >
          取消
        </button>
      </div>
    </form>
  );
}

/* ─── Key Card ─── */

function KeyCard({ apiKey }: { apiKey: ApiKeyRecord }) {
  const toggle = useToggleApiKey();
  const deleteKey = useDeleteApiKey();
  const [confirming, setConfirming] = useState(false);
  const isActive = apiKey.status === "active";

  function handleToggle() {
    toggle.mutate(
      { id: apiKey.id, status: isActive ? "disabled" : "active" },
      { onSuccess: () => toast.success(isActive ? "密钥已停用" : "密钥已启用") },
    );
  }

  function handleDelete() {
    if (!confirming) {
      setConfirming(true);
      setTimeout(() => setConfirming(false), 3000);
      return;
    }
    deleteKey.mutate(apiKey.id, {
      onSuccess: () => toast.success("密钥已撤销"),
    });
  }

  return (
    <div className={cn(
      "rounded-xl border bg-surface px-4 py-3 transition-colors",
      isActive ? "border-border" : "border-border/50 opacity-60",
    )}>
      <div className="flex items-center justify-between">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{apiKey.name}</span>
            <StatusBadge status={apiKey.status} />
            <span className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] text-muted">
              {apiKey.routing_mode}
            </span>
          </div>
          <div className="mt-1 flex items-center gap-3 text-xs text-muted">
            <span className="font-mono">{apiKey.key_prefix}...</span>
            {apiKey.last_used_at && (
              <span>最后使用: {new Date(apiKey.last_used_at).toLocaleDateString("zh-CN")}</span>
            )}
            {apiKey.expires_at && (
              <span>过期: {new Date(apiKey.expires_at).toLocaleDateString("zh-CN")}</span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={handleToggle}
            disabled={toggle.isPending}
            title={isActive ? "停用" : "启用"}
            className="rounded-md p-1.5 text-muted hover:bg-surface-2 hover:text-foreground transition-colors"
          >
            {isActive ? <ToggleRight size={18} /> : <ToggleLeft size={18} />}
          </button>
          <button
            onClick={handleDelete}
            disabled={deleteKey.isPending}
            title={confirming ? "再次点击确认撤销" : "撤销"}
            className={cn(
              "rounded-md p-1.5 transition-colors",
              confirming
                ? "bg-destructive/10 text-destructive"
                : "text-muted hover:bg-surface-2 hover:text-destructive",
            )}
          >
            <Trash2 size={16} />
          </button>
        </div>
      </div>
    </div>
  );
}
