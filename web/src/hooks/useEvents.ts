import { useEffect, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiBase, type CheckProgress, type UpdateJobItem } from '../api/client'
import { emitJobLog, useUIStore } from '../store/ui'

/** 全局订阅 SSE 实时检查与更新进度事件，并节流流式刷新相关缓存。 */
export function useEvents(enabled: boolean) {
  const qc = useQueryClient()
  const lastOverviewInvalidateRef = useRef<number>(0)
  const pendingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (!enabled) return
    const es = new EventSource(`${apiBase()}/events`)

    const clearPendingTimer = () => {
      if (pendingTimerRef.current !== null) {
        clearTimeout(pendingTimerRef.current)
        pendingTimerRef.current = null
      }
    }

    const throttledInvalidateOverview = () => {
      const now = Date.now()
      const elapsed = now - lastOverviewInvalidateRef.current
      if (elapsed >= 450) {
        clearPendingTimer()
        lastOverviewInvalidateRef.current = now
        qc.invalidateQueries({ queryKey: ['overview'] })
      } else if (pendingTimerRef.current === null) {
        pendingTimerRef.current = setTimeout(() => {
          pendingTimerRef.current = null
          lastOverviewInvalidateRef.current = Date.now()
          qc.invalidateQueries({ queryKey: ['overview'] })
        }, 450 - elapsed)
      }
    }

    es.addEventListener('progress', (ev) => {
      try {
        const p = JSON.parse((ev as MessageEvent).data) as CheckProgress
        useUIStore.getState().setLiveProgress(p)
        if (!p.running && p.phase === 'done') {
          clearPendingTimer()
          lastOverviewInvalidateRef.current = Date.now()
          qc.invalidateQueries({ queryKey: ['overview'] })
          qc.invalidateQueries({ queryKey: ['containers'] })
          qc.invalidateQueries({ queryKey: ['check-runs'] })
        } else if (p.running) {
          throttledInvalidateOverview()
        }
      } catch {
        // ignore malformed event
      }
    })

    es.addEventListener('update_job', (ev) => {
      try {
        const job = JSON.parse((ev as MessageEvent).data) as UpdateJobItem
        const chunk = job.logChunk ?? job.progress?.logChunk
        if (chunk) {
          emitJobLog(job.id, chunk, job.logOffset)
        }
        useUIStore.getState().upsertUpdateJob(job)
        if (job.status === 'succeeded' || job.status === 'failed') {
          qc.invalidateQueries({ queryKey: ['overview'] })
          qc.invalidateQueries({ queryKey: ['containers'] })
          qc.invalidateQueries({ queryKey: ['updates'] })
        }
      } catch {
        // ignore malformed event
      }
    })

    return () => {
      clearPendingTimer()
      es.close()
    }
  }, [enabled, qc])
}
