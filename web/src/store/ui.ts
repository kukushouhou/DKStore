import { create } from 'zustand'

type ThemeMode = 'system' | 'light' | 'dark'

interface UIState {
  themeMode: ThemeMode
  setThemeMode: (m: ThemeMode) => void
}

// 少量全局 UI 状态；服务端数据交给 TanStack Query 管理。
export const useUIStore = create<UIState>((set) => ({
  themeMode: (localStorage.getItem('dkstore.theme') as ThemeMode) || 'system',
  setThemeMode: (m) => {
    localStorage.setItem('dkstore.theme', m)
    set({ themeMode: m })
  },
}))
