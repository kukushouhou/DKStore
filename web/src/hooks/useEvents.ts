import { useEffect, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, apiBase, type AuthStatus, type CheckProgress, type UpdateJobItem } from '../api/client'
import { emitJobLog, useUIStore } from '../store/ui'

function isAuthActive(st: AuthStatus): boolean {
  return (
    st.mode !== 'setup_required' &&
    st.mode !== 'host_login_required' &&
    (st.mode !== 'password' || st.authenticated)
  )
}

/** 全局订阅 SSE 实时检查与更新进度事件，并在连接异常中断时自动探测登录态或重连。 */
export function useEvents(enabled: boolean) {
  const qc = useQueryClient()
  const lastOverviewInvalidateRef = useRef<number>(0)
  const pendingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (!enabled) return
    let disposed = false
    let es: EventSource | null = null
    let retryTimer: ReturnType<typeof setTimeout> | null = null
    let probingAuth = false

    const clearPendingTimer = () => {
      if (pendingTimerRef.current !== null) {
        clearTimeout(pendingTimerRef.current)
        pendingTimerRef.current = null
      }
    }

    const clearRetryTimer = () => {
      if (retryTimer !== null) {
        clearTimeout(retryTimer)
        retryTimer = null
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

    const probeAuthOrReconnect = () => {
      if (disposed || probingAuth) return
      probingAuth = true
      clearRetryTimer()
      api
        .authStatus()
        .then((st) => {
          if (disposed) return
          qc.setQueryData<AuthStatus>(['authStatus'], st)
          if (!isAuthActive(st)) {
            es?.close()
            es = null
            return
          }
          if (!es || es.readyState === EventSource.CLOSED) {
            retryTimer = setTimeout(() => {
              if (!disposed) connect()
            }, 1500)
          }
        })
        .catch(() => {
          if (disposed) return
          // 后端重启/短暂不可达期间持续探测，恢复后立即同步最新登录态
          retryTimer = setTimeout(() => {
            if (!disposed) probeAuthOrReconnect()
          }, 2500)
        })
        .finally(() => {
          probingAuth = false
        })
    }

    const connect = () => {
      if (disposed) return
      es?.close()
      const next = new EventSource(`${apiBase()}/events`)
      es = next

      next.addEventListener('progress', (ev) => {
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

      next.addEventListener('update_job', (ev) => {
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

      next.onerror = () => {
        if (disposed) return
        probeAuthOrReconnect()
      }
    }

    connect()

    return () => {
      disposed = true
      clearPendingTimer()
      clearRetryTimer()
      es?.close()
      es = null
    }
  }, [enabled, qc])
}
