import { useEffect, useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";

export type TabItem<T extends string> = {
  id: T;
  label: string;
  count?: number;
};

export function SectionTabs<T extends string>({ tabs, active, onChange }: { tabs: TabItem<T>[]; active: T; onChange: (id: T) => void }) {
  return (
    <div className="section-tabs">
      {tabs.map((tab) => (
        <button key={tab.id} className={active === tab.id ? "active" : ""} onClick={() => onChange(tab.id)}>
          <span>{tab.label}</span>
          {typeof tab.count === "number" && <small>{tab.count}</small>}
        </button>
      ))}
    </div>
  );
}

export function PageNotice({ error, message, action }: { error?: string; message?: string; action?: ReactNode }) {
  if (!error && !message && !action) return null;
  return (
    <section className="notice-panel">
      <div>
        {error && <div className="error">{error}</div>}
        {message && <div className="success">{message}</div>}
      </div>
      {action && <div className="notice-action">{action}</div>}
    </section>
  );
}

export function SectionHeader({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="section-header">
      <div>
        <h2>{title}</h2>
        {description && <p>{description}</p>}
      </div>
      {action && <div className="actions">{action}</div>}
    </div>
  );
}

export function CopyButton({
  value,
  label = "复制",
  successLabel = "已复制",
  className,
}: {
  value: string;
  label?: string;
  successLabel?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  const Icon = copied ? Check : Copy;

  async function copy() {
    if (!value) return;
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(value);
      } else {
        fallbackCopy(value);
      }
    } catch {
      fallbackCopy(value);
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  }

  return (
    <button type="button" className={className} onClick={copy} disabled={!value}>
      <Icon size={15} />
      {copied ? successLabel : label}
    </button>
  );
}

function fallbackCopy(value: string) {
  const field = document.createElement("textarea");
  field.value = value;
  field.setAttribute("readonly", "true");
  field.style.position = "fixed";
  field.style.opacity = "0";
  document.body.appendChild(field);
  field.select();
  document.execCommand("copy");
  document.body.removeChild(field);
}

export function ActionModal({
  open,
  title,
  description,
  size = "md",
  presentation = "drawer",
  footer,
  onClose,
  children,
}: {
  open: boolean;
  title: string;
  description?: string;
  size?: "sm" | "md" | "lg" | "xl";
  presentation?: "drawer" | "dialog";
  footer?: ReactNode;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", onKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className={`modal-backdrop modal-backdrop-${presentation}`} role="presentation" onMouseDown={onClose}>
      <section
        className={`action-modal action-modal-${presentation} action-modal-${size}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="action-modal-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="action-modal-header">
          <div>
            <h2 id="action-modal-title">{title}</h2>
            {description && <p>{description}</p>}
          </div>
          <button className="icon" type="button" aria-label="关闭" onClick={onClose}>x</button>
        </header>
        <div className="action-modal-body">{children}</div>
        {footer && <footer className="action-modal-footer">{footer}</footer>}
      </section>
    </div>
  );
}
