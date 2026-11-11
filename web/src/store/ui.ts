import { create } from 'zustand'
import type { CheckProgress, LayerProgress, UpdateJobItem } from '../api/client'

export type ThemeMode = 'system' | 'light' | 'dark'
export type Locale = 'zh-CN' | 'en-US'
export type ProgressStyle = 'layered' | 'unified'

type JobLogCallback = (chunk: string, offset?: number) => void

const jobLogListeners = new Map<string, Set<JobLogCallback>>()

/** 订阅单个更新任务的实时终端增量日志流（独立于 React 渲染树，避免逐字节重绘） */
export function subscribeJobLog(jobId: string, cb: JobLogCallback): () => void {
  let set = jobLogListeners.get(jobId)
  if (!set) {
    set = new Set()
    jobLogListeners.set(jobId, set)
  }
  set.add(cb)
  return () => {
    const cur = jobLogListeners.get(jobId)
    if (!cur) return
    cur.delete(cb)
    if (cur.size === 0) {
      jobLogListeners.delete(jobId)
    }
  }
}

/** 推送单个更新任务的增量终端日志片段给已打开的终端弹窗 */
export function emitJobLog(jobId: string, chunk: string, offset?: number): void {
  if (!chunk) return
  const listeners = jobLogListeners.get(jobId)
  if (!listeners || listeners.size === 0) return
  for (const cb of listeners) {
    cb(chunk, offset)
  }
}

function detectInitialLocale(): Locale {
  const saved = localStorage.getItem('dkstore.locale')
  if (saved === 'zh-CN' || saved === 'en-US') {
    return saved
  }
  const nav = (navigator.language || '').toLowerCase()
  return nav.startsWith('zh') ? 'zh-CN' : 'en-US'
}

function detectInitialProgressStyle(): ProgressStyle {
  const saved = localStorage.getItem('dkstore_progress_style')
  return saved === 'unified' ? 'unified' : 'layered'
}

function isSameLayers(a?: LayerProgress[], b?: LayerProgress[]): boolean {
  const lenA = a?.length ?? 0
  const lenB = b?.length ?? 0
  if (lenA !== lenB) return false
  if (!a || !b) return true
  for (let i = 0; i < lenA; i++) {
    const la = a[i]
    const lb = b[i]
    if (
      la.id !== lb.id ||
      la.status !== lb.status ||
      la.percent !== lb.percent ||
      la.current !== lb.current ||
      la.total !== lb.total
    ) {
      return false
    }
  }
  return true
}

function isSameJobVisualState(prev: UpdateJobItem, next: UpdateJobItem): boolean {
  if (
    prev.status !== next.status ||
    prev.phase !== next.phase ||
    prev.errorCode !== next.errorCode ||
    prev.errorMsg !== next.errorMsg ||
    prev.finishedAt !== next.finishedAt ||
    prev.targetVersion !== next.targetVersion ||
    prev.targetImageRef !== next.targetImageRef
  ) {
    return false
  }
  const pa = prev.progress
  const pb = next.progress
  if (!pa || !pb) return pa === pb
  if (
    pa.phase !== pb.phase ||
    pa.statusText !== pb.statusText ||
    pa.percent !== pb.percent ||
    pa.completedLayers !== pb.completedLayers ||
    pa.totalLayers !== pb.totalLayers ||
    pa.downloadedBytes !== pb.downloadedBytes ||
    pa.totalBytes !== pb.totalBytes
  ) {
    return false
  }
  return isSameLayers(pa.layers, pb.layers)
}

interface UIState {
  themeMode: ThemeMode
  locale: Locale
  progressStyle: ProgressStyle
  liveProgress: CheckProgress | null
  activeUpdateJobs: Record<string, UpdateJobItem>
  lastFinishedJobs: Record<string, UpdateJobItem>
  updateQueueJobs: Record<string, UpdateJobItem>
  queueCollapsed: boolean
  activeLogJobId: string | null
  setThemeMode: (m: ThemeMode) => void
  setLocale: (l: Locale) => void
  setProgressStyle: (style: ProgressStyle) => void
  setLiveProgress: (p: CheckProgress | null) => void
  setQueueCollapsed: (collapsed: boolean) => void
  toggleQueueCollapsed: () => void
  setActiveLogJobId: (jobId: string | null) => void
  dismissQueueJob: (jobId: string) => void
  clearFinishedQueueJobs: () => void
  upsertUpdateJob: (job: UpdateJobItem) => void
}

export const useUIStore = create<UIState>((set) => ({
  themeMode: (localStorage.getItem('dkstore.theme') as ThemeMode) || 'system',
  locale: detectInitialLocale(),
  progressStyle: detectInitialProgressStyle(),
  liveProgress: null,
  activeUpdateJobs: {},
  lastFinishedJobs: {},
  updateQueueJobs: {},
  queueCollapsed: false,
  activeLogJobId: null,
  setThemeMode: (m) => {
    localStorage.setItem('dkstore.theme', m)
    set({ themeMode: m })
  },
  setLocale: (l) => {
    localStorage.setItem('dkstore.locale', l)
    set({ locale: l })
  },
  setProgressStyle: (style) => {
    localStorage.setItem('dkstore_progress_style', style)
    set({ progressStyle: style })
  },
  setLiveProgress: (p) => set({ liveProgress: p }),
  setQueueCollapsed: (collapsed) => set({ queueCollapsed: collapsed }),
  toggleQueueCollapsed: () => set((s) => ({ queueCollapsed: !s.queueCollapsed })),
  setActiveLogJobId: (jobId) => set({ activeLogJobId: jobId }),
  dismissQueueJob: (jobId) =>
    set((s) => {
      if (!s.updateQueueJobs[jobId]) return s
      const nextQueue = { ...s.updateQueueJobs }
      delete nextQueue[jobId]
      return { updateQueueJobs: nextQueue }
    }),
  clearFinishedQueueJobs: () =>
    set((s) => {
      const nextQueue: Record<string, UpdateJobItem> = {}
      for (const [id, job] of Object.entries(s.updateQueueJobs)) {
        if (job.status === 'queued' || job.status === 'running') {
          nextQueue[id] = job
        }
      }
      return { updateQueueJobs: nextQueue }
    }),
  upsertUpdateJob: (rawJob) =>
    set((s) => {
      const existing = s.updateQueueJobs[rawJob.id]
      const cleanProgress = rawJob.progress
        ? { ...rawJob.progress, logChunk: undefined }
        : rawJob.progress
      const job: UpdateJobItem = {
        ...existing,
        ...rawJob,
        progress: cleanProgress,
        logChunk: undefined,
      }

      if (existing && isSameJobVisualState(existing, job)) {
        return s
      }

      const isNewRunning =
        !existing && (job.status === 'queued' || job.status === 'running')
      const nextQueue = { ...s.updateQueueJobs, [job.id]: job }

      if (job.status === 'succeeded' || job.status === 'failed') {
        const nextActive = { ...s.activeUpdateJobs }
        delete nextActive[job.serviceKey]
        return {
          activeUpdateJobs: nextActive,
          lastFinishedJobs: { ...s.lastFinishedJobs, [job.serviceKey]: job },
          updateQueueJobs: nextQueue,
        }
      }

      const nextFinished = { ...s.lastFinishedJobs }
      delete nextFinished[job.serviceKey]
      return {
        activeUpdateJobs: { ...s.activeUpdateJobs, [job.serviceKey]: job },
        lastFinishedJobs: nextFinished,
        updateQueueJobs: nextQueue,
        ...(isNewRunning ? { queueCollapsed: false } : {}),
      }
    }),
}))
