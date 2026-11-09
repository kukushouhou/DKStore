import { create } from 'zustand'
import type { CheckProgress, UpdateJobItem } from '../api/client'

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
  liveProgress: CheckProgress | null
  activeUpdateJobs: Record<string, UpdateJobItem>
  lastFinishedJobs: Record<string, UpdateJobItem>
  setThemeMode: (m: ThemeMode) => void
  setLocale: (l: Locale) => void
  setLiveProgress: (p: CheckProgress | null) => void
  upsertUpdateJob: (job: UpdateJobItem) => void
}

export const useUIStore = create<UIState>((set) => ({
  themeMode: (localStorage.getItem('dkstore.theme') as ThemeMode) || 'system',
  locale: detectInitialLocale(),
  liveProgress: null,
  activeUpdateJobs: {},
  lastFinishedJobs: {},
  setThemeMode: (m) => {
    localStorage.setItem('dkstore.theme', m)
    set({ themeMode: m })
  },
  setLocale: (l) => {
    localStorage.setItem('dkstore.locale', l)
    set({ locale: l })
  },
  setLiveProgress: (p) => set({ liveProgress: p }),
  upsertUpdateJob: (job) =>
    set((s) => {
      if (job.status === 'succeeded' || job.status === 'failed') {
        const nextActive = { ...s.activeUpdateJobs }
        delete nextActive[job.serviceKey]
        return {
          activeUpdateJobs: nextActive,
          lastFinishedJobs: { ...s.lastFinishedJobs, [job.serviceKey]: job },
        }
      }
      const nextFinished = { ...s.lastFinishedJobs }
      delete nextFinished[job.serviceKey]
      return {
        activeUpdateJobs: { ...s.activeUpdateJobs, [job.serviceKey]: job },
        lastFinishedJobs: nextFinished,
      }
    }),
}))
