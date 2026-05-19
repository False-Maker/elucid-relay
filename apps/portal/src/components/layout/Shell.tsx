import type { ReactNode } from "react";
import { RefreshCw, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export type NavItem<T extends string> = {
  id: T;
  label: string;
  icon: LucideIcon;
  badge?: number;
};

export type NavGroup<T extends string> = {
  label: string;
  items: NavItem<T>[];
};

type ShellProps<T extends string> = {
  title: string;
  description: string;
  crumb: string;
  brandLabel: string;
  brandSubtitle: string;
  navGroups: NavGroup<T>[];
  activeId: T;
  onSelect: (id: T) => void;
  footer: ReactNode;
  statusLabel?: string;
  statusTone?: "ok" | "warning";
  wide?: boolean;
  children: ReactNode;
};

export function Shell<T extends string>({
  title,
  description,
  crumb,
  brandLabel,
  brandSubtitle,
  navGroups,
  activeId,
  onSelect,
  footer,
  statusLabel = "正常",
  statusTone = "ok",
  wide = false,
  children,
}: ShellProps<T>) {
  return (
    <div className="flex h-screen overflow-hidden bg-workspace-bg">
      {/* Sidebar */}
      <aside className="flex w-56 shrink-0 flex-col border-r border-border bg-surface">
        {/* Brand */}
        <div className="flex items-center gap-2.5 px-4 py-5">
          <BrandMark />
          <span className="flex flex-col leading-tight">
            <strong className="text-sm font-semibold text-foreground">{brandLabel}</strong>
            <small className="font-mono text-xs text-muted">{brandSubtitle}</small>
          </span>
        </div>

        {/* Navigation */}
        <nav className="flex-1 overflow-y-auto px-2 pb-4">
          {navGroups.map((group) => (
            <div key={group.label} className="mb-4">
              <div className="mb-1 px-2 text-[11px] font-medium uppercase tracking-wider text-muted-2">
                {group.label}
              </div>
              {group.items.map((item) => {
                const Icon = item.icon;
                const active = activeId === item.id;
                return (
                  <button
                    key={item.id}
                    onClick={() => onSelect(item.id)}
                    className={cn(
                      "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
                      active
                        ? "bg-primary text-primary-foreground"
                        : "text-foreground hover:bg-surface-2",
                    )}
                  >
                    <Icon size={16} className="shrink-0" />
                    <span className="truncate">{item.label}</span>
                    {item.badge != null && item.badge > 0 && (
                      <span className="ml-auto rounded-full bg-bronze/15 px-1.5 text-[10px] font-medium text-bronze-deep">
                        {item.badge}
                      </span>
                    )}
                  </button>
                );
              })}
            </div>
          ))}
        </nav>

        {/* Footer */}
        <div className="border-t border-border px-3 py-3">{footer}</div>
      </aside>

      {/* Main content */}
      <main className="flex flex-1 flex-col overflow-hidden">
        {/* Top bar */}
        <header className="flex items-start justify-between border-b border-border bg-surface px-6 py-4">
          <div>
            <div className="mb-0.5 font-mono text-xs text-muted">
              Elucid Relay / <span className="text-bronze">{crumb}</span>
            </div>
            <h1 className="text-xl font-semibold leading-tight">{title}</h1>
            <p className="mt-0.5 text-sm text-muted">{description}</p>
          </div>
          <div className="flex items-center gap-2">
            <div
              className={cn(
                "flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium",
                statusTone === "ok"
                  ? "bg-success/10 text-success"
                  : "bg-warning/10 text-warning",
              )}
            >
              <span
                className={cn(
                  "inline-block h-1.5 w-1.5 rounded-full",
                  statusTone === "ok" ? "bg-success" : "bg-warning",
                )}
              />
              {statusLabel}
            </div>
            <button
              onClick={() => window.location.reload()}
              title="刷新"
              className="rounded-md p-1.5 text-muted hover:bg-surface-2 hover:text-foreground transition-colors"
            >
              <RefreshCw size={16} />
            </button>
          </div>
        </header>

        {/* Page content */}
        <div className="flex-1 overflow-y-auto p-6">
          <div className={cn("mx-auto", wide ? "max-w-6xl" : "max-w-4xl")}>
            {children}
          </div>
        </div>
      </main>
    </div>
  );
}

function BrandMark() {
  return (
    <span aria-hidden="true">
      <svg viewBox="30 60 350 320" width="26" height="26" fill="none">
        <path d="M80 220L124 130L360 78L348 138L142 178Z" fill="#d4ba85" />
        <path d="M80 220L138 209L360 202L354 238L142 240Z" fill="#8294a8" />
        <path d="M80 220L124 310L360 362L348 302L142 262Z" fill="#0f172a" />
        <rect x="77" y="120" width="6" height="200" rx="3" fill="#0f172a" />
        <rect x="78.5" y="216" width="3" height="8" rx="1.5" fill="#8b6b3e" />
      </svg>
    </span>
  );
}
