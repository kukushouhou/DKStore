import { useEffect, useMemo, useState } from 'react'
import type { UpdateJobItem } from '../api/client'
import { useUIStore } from '../store/ui'

export const ULTRAWIDE_MIN_WIDTH = 1980

export function useIsUltrawide(): boolean {
  const [isUltrawide, setIsUltrawide] = useState(
    () => typeof window !== 'undefined' && window.innerWidth >= ULTRAWIDE_MIN_WIDTH,
  )
  useEffect(() => {
    const onResize = () => {
      setIsUltrawide(window.innerWidth >= ULTRAWIDE_MIN_WIDTH)
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return isUltrawide
}

export function useOrderedQueueJobs(): UpdateJobItem[] {
  const updateQueueJobs = useUIStore((s) => s.updateQueueJobs)
  return useMemo(() => {
    const entries = Object.values(updateQueueJobs)
    return [...entries].reverse().sort((a, b) => (b.startedAt || 0) - (a.startedAt || 0))
  }, [updateQueueJobs])
}
