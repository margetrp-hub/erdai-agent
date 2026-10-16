import { X } from 'lucide-react';
import { useEffect, useId, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

let openDialogs = 0;
let originalBodyOverflow = '';

type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost';
  icon?: ReactNode;
};

export function Button({ variant = 'secondary', icon, children, className = '', ...props }: ButtonProps) {
  return (
    <button className={`ui-button ui-button-${variant}${variant === 'ghost' && children ? ' ui-button-text' : ''} ${className}`.trim()} {...props}>
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
  className = '',
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  className?: string;
}) {
  const titleId = useId();
  const descriptionId = useId();
  const dialogRef = useRef<HTMLElement>(null);
  const onChangeRef = useRef(onOpenChange);
  onChangeRef.current = onOpenChange;
  useEffect(() => {
    if (!open) return undefined;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (openDialogs++ === 0) {
      originalBodyOverflow = document.body.style.overflow;
      document.body.style.overflow = 'hidden';
    }
    const focusable = () => [...(dialogRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], summary, [tabindex="0"]') || [])].filter((item) => item.getClientRects().length > 0);
    const initialFocus = requestAnimationFrame(() => (focusable()[0] || dialogRef.current)?.focus());
    const handleKeyDown = (event: KeyboardEvent) => {
      const dialogs = document.querySelectorAll('[role="dialog"]');
      if (dialogs[dialogs.length - 1] !== dialogRef.current) return;
      if (event.key === 'Escape') { event.preventDefault(); onChangeRef.current(false); }
      if (event.key !== 'Tab') return;
      const targets = focusable();
      const first = targets[0];
      const last = targets[targets.length - 1];
      if (!first) { event.preventDefault(); dialogRef.current?.focus(); return; }
      if (event.shiftKey && (document.activeElement === first || !dialogRef.current?.contains(document.activeElement))) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && (document.activeElement === last || !dialogRef.current?.contains(document.activeElement))) { event.preventDefault(); first.focus(); }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      cancelAnimationFrame(initialFocus);
      document.removeEventListener('keydown', handleKeyDown);
      if (--openDialogs === 0) document.body.style.overflow = originalBodyOverflow;
      if (previousFocus?.isConnected) previousFocus.focus();
    };
  }, [open]);

  if (!open) return null;
  return createPortal(
    <div className="dialog-layer">
      <div className="dialog-overlay" onMouseDown={() => onOpenChange(false)} />
      <section
        ref={dialogRef}
        tabIndex={-1}
        className={`dialog-content ${className}`.trim()}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
      >
        <div className="dialog-header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description ? <p id={descriptionId}>{description}</p> : null}
          </div>
          <Button variant="ghost" icon={<X size={16} />} aria-label="关闭" onClick={() => onOpenChange(false)} />
        </div>
        <div className="dialog-body">{children}</div>
      </section>
    </div>,
    document.body,
  );
}
