import { create } from 'zustand'

export type ThemeMode = 'system' | 'light' | 'dark'
export type Locale = 'zh-CN' | 'en-US'

function detectInitialLocale(): Locale {
  const saved = localStorage.getItem('dkstore.locale')
  if (saved === 'zh-CN' || saved === 'en-US') {
    return saved
  }
  const nav = (navigator.language || '').toLowerCase()
  return nav.startsWith('zh') ? 'zh-CN' : 'en-US'
}

interface UIState {
  themeMode: ThemeMode
  locale: Locale
  setThemeMode: (m: ThemeMode) => void
  setLocale: (l: Locale) => void
}

export const useUIStore = create<UIState>((set) => ({
  themeMode: (localStorage.getItem('dkstore.theme') as ThemeMode) || 'system',
  locale: detectInitialLocale(),
  setThemeMode: (m) => {
    localStorage.setItem('dkstore.theme', m)
    set({ themeMode: m })
  },
  setLocale: (l) => {
    localStorage.setItem('dkstore.locale', l)
    set({ locale: l })
  },
}))
