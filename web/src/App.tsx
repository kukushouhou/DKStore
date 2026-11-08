import { useEffect, useState } from 'react'
import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router'
import { router } from './router'
import { buildTheme } from './theme'
import { useUIStore } from './store/ui'

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
})

function useSystemDark() {
  const [dark, setDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches)
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const fn = (e: MediaQueryListEvent) => setDark(e.matches)
    mq.addEventListener('change', fn)
    return () => mq.removeEventListener('change', fn)
  }, [])
  return dark
}

export default function App() {
  const mode = useUIStore((s) => s.themeMode)
  const systemDark = useSystemDark()
  const dark = mode === 'dark' || (mode === 'system' && systemDark)
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  }, [dark])

  return (
    <ConfigProvider locale={zhCN} theme={buildTheme(dark)}>
      <AntApp>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </AntApp>
    </ConfigProvider>
  )
}
