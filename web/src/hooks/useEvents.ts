import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { CheckProgress, UpdateJobItem } from '../api/client'
import { useUIStore } from '../store/ui'

/** 全局订阅 SSE 实时检查与更新进度事件，并统一刷新相关缓存。 */
export function useEvents(enabled: boolean) {
  const qc = useQueryClient()

  useEffect(() => {
    if (!enabled) return
    const es = new EventSource('/api/v1/events')
    es.addEventListener('progress', (ev) => {
      try {
        const p = JSON.parse((ev as MessageEvent).data) as CheckProgress
        useUIStore.getState().setLiveProgress(p)
        if (!p.running && p.phase === 'done') {
          qc.invalidateQueries({ queryKey: ['overview'] })
          qc.invalidateQueries({ queryKey: ['check-runs'] })
        }
      } catch {
        // ignore malformed event
      }
    })
    es.addEventListener('update_job', (ev) => {
      try {
        const job = JSON.parse((ev as MessageEvent).data) as UpdateJobItem
        useUIStore.getState().upsertUpdateJob(job)
        if (job.status === 'succeeded' || job.status === 'failed') {
          qc.invalidateQueries({ queryKey: ['overview'] })
          qc.invalidateQueries({ queryKey: ['updates'] })
        }
      } catch {
        // ignore malformed event
      }
    })
    return () => es.close()
  }, [enabled, qc])
}
