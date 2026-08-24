import { useEffect, useMemo, useState } from 'react';

export type UiThemeId = 'native' | 'standard' | 'anime' | 'industrial';

export type UiTheme = {
  id: UiThemeId;
  label: string;
  description: string;
  icon: string;
};

export const UI_THEMES: readonly UiTheme[] = [
  {
    id: 'native',
    label: '原生',
    description: '保留二呆现有深色控制台',
    icon: '/assets/themes/erdai/native.webp',
  },
  {
    id: 'standard',
    label: '标准',
    description: '中性、克制的日常工作台',
    icon: '/assets/themes/erdai/standard.webp',
  },
  {
    id: 'anime',
    label: '二次元',
    description: '导航员与多模型中继空间',
    icon: '/assets/themes/erdai/anime.webp',
  },
  {
    id: 'industrial',
    label: '废土工业',
    description: '锈橙信号与重装工业骨架',
    icon: '/assets/themes/erdai/industrial.webp',
  },
] as const;

const STORAGE_KEY = 'erdai:ui-theme:v1';
const DEFAULT_THEME: UiThemeId = 'native';
const THEME_EVENT = 'erdai:themechange';

function isThemeId(value: unknown): value is UiThemeId {
  return UI_THEMES.some((theme) => theme.id === value);
}

function readTheme(): UiThemeId {
  if (typeof window === 'undefined') return DEFAULT_THEME;
  const documentTheme = document.documentElement.dataset.uiTheme;
  if (isThemeId(documentTheme)) return documentTheme;
  try {
    const storedTheme = window.localStorage.getItem(STORAGE_KEY);
    return isThemeId(storedTheme) ? storedTheme : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

function applyTheme(themeId: UiThemeId, persist: boolean) {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.uiTheme = themeId;
  document.documentElement.style.colorScheme = themeId === 'native' || themeId === 'industrial' ? 'dark' : 'light';
  if (persist) {
    try {
      window.localStorage.setItem(STORAGE_KEY, themeId);
    } catch {
      // Theme selection remains usable when browser storage is unavailable.
    }
  }
}

export function initializeUiTheme(): UiThemeId {
  const themeId = readTheme();
  applyTheme(themeId, false);
  return themeId;
}

export function setUiTheme(themeId: UiThemeId) {
  if (!isThemeId(themeId)) return;
  applyTheme(themeId, true);
  window.dispatchEvent(new CustomEvent(THEME_EVENT, { detail: { themeId } }));
}

export function useUiTheme() {
  const [activeThemeId, setActiveThemeId] = useState<UiThemeId>(() => readTheme());

  useEffect(() => {
    const handleThemeChange = (event: Event) => {
      const themeId = (event as CustomEvent<{ themeId?: unknown }>).detail?.themeId;
      if (isThemeId(themeId)) setActiveThemeId(themeId);
    };
    window.addEventListener(THEME_EVENT, handleThemeChange);
    return () => window.removeEventListener(THEME_EVENT, handleThemeChange);
  }, []);

  return {
    activeThemeId,
    activeTheme: useMemo(
      () => UI_THEMES.find((theme) => theme.id === activeThemeId) || UI_THEMES[0],
      [activeThemeId],
    ),
    themes: UI_THEMES,
    setTheme: setUiTheme,
  };
}
