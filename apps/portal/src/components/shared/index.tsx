import { cn } from "@/lib/utils";

/** Skeleton placeholder for loading states. */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        "animate-pulse rounded-md bg-surface-2",
        className,
      )}
    />
  );
}

/** Metric card with optional skeleton loading. */
export function MetricCard({
  label,
  value,
  detail,
  loading,
}: {
  label: string;
  value?: string | number;
  detail?: string;
  loading?: boolean;
}) {
  return (
    <div className="rounded-lg border border-border bg-surface p-4">
      <div className="text-xs font-medium text-muted">{label}</div>
      {loading ? (
        <>
          <Skeleton className="mt-2 h-6 w-20" />
          <Skeleton className="mt-1.5 h-3.5 w-28" />
        </>
      ) : (
        <>
          <div className="mt-1 text-xl font-semibold">{value ?? "—"}</div>
          {detail && (
            <div className="mt-0.5 text-xs text-muted">{detail}</div>
          )}
        </>
      )}
    </div>
  );
}

/** Status badge with color coding. */
export function StatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const tone = statusTone(status);
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium",
        tone === "success" && "bg-success/10 text-success",
        tone === "warning" && "bg-warning/10 text-warning",
        tone === "error" && "bg-destructive/10 text-destructive",
        tone === "neutral" && "bg-surface-2 text-muted",
        className,
      )}
    >
      {STATUS_LABELS[status] ?? status}
    </span>
  );
}

const STATUS_LABELS: Record<string, string> = {
  active: "启用",
  disabled: "停用",
  pending: "待处理",
  success: "成功",
  failed: "失败",
  rejected: "拒绝",
  expired: "已过期",
  paid: "已支付",
  refunded: "已退款",
  cancelled: "已取消",
};

function statusTone(
  status: string,
): "success" | "warning" | "error" | "neutral" {
  if (["active", "success", "paid", "authorized"].includes(status))
    return "success";
  if (["pending", "processing", "warning"].includes(status)) return "warning";
  if (["disabled", "failed", "rejected", "expired", "error"].includes(status))
    return "error";
  return "neutral";
}

/** Empty state placeholder. */
export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="text-sm font-medium text-muted">{title}</div>
      {description && (
        <div className="mt-1 text-xs text-muted-2">{description}</div>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

/** Copy-to-clipboard button with feedback. */
export function CopyButton({
  value,
  label = "复制",
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = React.useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      // Fallback for non-HTTPS contexts
      const ta = document.createElement("textarea");
      ta.value = value;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <button
      onClick={copy}
      className={cn(
        "inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs transition-colors hover:bg-surface-2",
        className,
      )}
    >
      {copied ? "已复制" : label}
    </button>
  );
}

/** Page section header with optional action. */
export function SectionHeader({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="mb-4 flex items-start justify-between">
      <div>
        <h2 className="text-sm font-semibold">{title}</h2>
        {description && (
          <p className="mt-0.5 text-xs text-muted">{description}</p>
        )}
      </div>
      {action && <div>{action}</div>}
    </div>
  );
}

import React from "react";
