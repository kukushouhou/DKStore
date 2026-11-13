import { useEffect, useState } from 'react'
import { useUIStore } from '../store/ui'

export function useEffectiveDark(): boolean {
  const mode = useUIStore((s) => s.themeMode)
  const [sysDark, setSysDark] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches,
  )

  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const fn = (e: MediaQueryListEvent) => setSysDark(e.matches)
    mq.addEventListener('change', fn)
    return () => mq.removeEventListener('change', fn)
  }, [])

  return mode === 'dark' || (mode === 'system' && sysDark)
}
