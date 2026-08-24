import { Check, ChevronDown } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { type UiThemeId, useUiTheme } from '../theme';

export function ThemeSwitcher({ placement = 'header' }: { placement?: 'header' | 'auth' }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const { activeThemeId, activeTheme, themes, setTheme } = useUiTheme();

  useEffect(() => {
    if (!open) return undefined;
    const handlePointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  const chooseTheme = (themeId: UiThemeId) => {
    setTheme(themeId);
    setOpen(false);
  };

  return (
    <div ref={rootRef} className={`theme-switcher theme-switcher-${placement} ${open ? 'is-open' : ''}`}>
      <button
        type="button"
        className="theme-trigger"
        title="切换界面主题"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <img src={activeTheme.icon} alt="" />
        <span>{activeTheme.label}</span>
        <ChevronDown size={13} aria-hidden="true" />
      </button>
      {open ? (
        <div className="theme-menu" role="listbox" aria-label="界面主题">
          <span className="theme-menu-title">界面主题</span>
          {themes.map((theme) => (
            <button
              type="button"
              role="option"
              aria-selected={theme.id === activeThemeId}
              className={`theme-option ${theme.id === activeThemeId ? 'is-active' : ''}`}
              key={theme.id}
              onClick={() => chooseTheme(theme.id)}
            >
              <img src={theme.icon} alt="" />
              <span>
                <strong>{theme.label}</strong>
                <small>{theme.description}</small>
              </span>
              {theme.id === activeThemeId ? <Check size={15} aria-hidden="true" /> : null}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
