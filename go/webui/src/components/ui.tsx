import { X } from 'lucide-react';
import { useEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost';
  icon?: ReactNode;
};

export function Button({ variant = 'secondary', icon, children, className = '', ...props }: ButtonProps) {
  return (
    <button className={`ui-button ui-button-${variant} ${className}`.trim()} {...props}>
      {icon}
      {children}
    </button>
  );
}

export function StatusDot({ tone = 'ok' }: { tone?: 'ok' | 'warn' | 'bad' | 'idle' }) {
  return <span className={`status-dot status-dot-${tone}`} aria-hidden="true" />;
}

export function Panel({
  children,
  className = '',
  accent = 'cyan',
}: {
  children: ReactNode;
  className?: string;
  accent?: 'cyan' | 'rose' | 'amber' | 'violet' | 'green';
}) {
  return <section className={`ui-panel ui-panel-${accent} ${className}`.trim()}>{children}</section>;
}

export function PanelHeading({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <header className="panel-heading">
      <div>
        {eyebrow ? <span className="panel-eyebrow">{eyebrow}</span> : null}
        <h2>{title}</h2>
        {description ? <p>{description}</p> : null}
      </div>
      {action ? <div className="panel-heading-action">{action}</div> : null}
    </header>
  );
}

export function InfoDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  useEffect(() => {
    if (!open) return undefined;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onOpenChange(false);
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onOpenChange, open]);

  if (!open) return null;
  return createPortal(
    <div className="dialog-layer">
      <div className="dialog-overlay" onMouseDown={() => onOpenChange(false)} />
      <section
        className="dialog-content"
        role="dialog"
        aria-modal="true"
        aria-labelledby="modern-dialog-title"
        aria-describedby={description ? 'modern-dialog-description' : undefined}
      >
        <div className="dialog-header">
          <div>
            <h2 id="modern-dialog-title">{title}</h2>
            {description ? <p id="modern-dialog-description">{description}</p> : null}
          </div>
          <Button variant="ghost" icon={<X size={16} />} aria-label="关闭" onClick={() => onOpenChange(false)} />
        </div>
        <div className="dialog-body">{children}</div>
      </section>
    </div>,
    document.body,
  );
}
