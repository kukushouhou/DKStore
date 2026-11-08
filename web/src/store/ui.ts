import { create } from 'zustand'
import type { UpdateJobItem } from '../api/client'

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
  activeUpdateJobs: Record<string, UpdateJobItem>
  setThemeMode: (m: ThemeMode) => void
  setLocale: (l: Locale) => void
  upsertUpdateJob: (job: UpdateJobItem) => void
}

export const useUIStore = create<UIState>((set) => ({
  themeMode: (localStorage.getItem('dkstore.theme') as ThemeMode) || 'system',
  locale: detectInitialLocale(),
  activeUpdateJobs: {},
  setThemeMode: (m) => {
    localStorage.setItem('dkstore.theme', m)
    set({ themeMode: m })
  },
  setLocale: (l) => {
    localStorage.setItem('dkstore.locale', l)
    set({ locale: l })
  },
  upsertUpdateJob: (job) =>
    set((s) => ({
      activeUpdateJobs: { ...s.activeUpdateJobs, [job.serviceKey]: job },
    })),
}))
