import type { ReactNode } from "react";
import { RefreshCw, type LucideIcon } from "lucide-react";

export type NavItem<T extends string> = {
  id: T;
  label: string;
  icon: LucideIcon;
};

export type NavGroup<T extends string> = {
  label: string;
  items: NavItem<T>[];
};

type ProductShellProps<T extends string> = {
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
  contentVariant?: "default" | "wide";
  children: ReactNode;
};

export function ProductShell<T extends string>({
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
  contentVariant = "default",
  children,
}: ProductShellProps<T>) {
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <BrandMark />
          <span>
            <strong>{brandLabel}</strong>
            <small>{brandSubtitle}</small>
          </span>
        </div>
        <nav className="nav-groups">
          {navGroups.map((group) => (
            <div className="nav-group" key={group.label}>
              <div className="nav-label">{group.label}</div>
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <button key={item.id} className={activeId === item.id ? "active" : ""} onClick={() => onSelect(item.id)}>
                    <Icon size={18} />
                    <span>{item.label}</span>
                  </button>
                );
              })}
            </div>
          ))}
        </nav>
        <div className="sidebar-footer">{footer}</div>
      </aside>
      <main className="workspace">
        <header className="topbar">
          <div>
            <div className="crumb">Elucid Relay / <span>{crumb}</span></div>
            <h1>{title}</h1>
            <p>{description}</p>
          </div>
          <div className="topbar-actions">
            <div className={`env-pill ${statusTone === "warning" ? "degraded" : ""}`}><span /> {statusLabel}</div>
            <button className="icon" onClick={() => window.location.reload()} title="刷新">
              <RefreshCw size={18} />
            </button>
          </div>
        </header>
        <div className={`page-content ${contentVariant === "wide" ? "page-content-wide" : ""}`}>{children}</div>
      </main>
    </div>
  );
}

function BrandMark() {
  return (
    <span className="brand-mark" aria-hidden="true">
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
