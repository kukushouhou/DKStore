import { useEffect, useRef, useState } from 'react'
import { Button, Grid, Modal, message, theme } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import '@xterm/xterm/css/xterm.css'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { api, type UpdateJobItem } from '../api/client'
import { useI18n } from '../i18n'
import { subscribeJobLog, useUIStore } from '../store/ui'

export default function UpdateLogModal() {
  const jobId = useUIStore((s) => s.activeLogJobId)
  const setActiveLogJobId = useUIStore((s) => s.setActiveLogJobId)
  const screens = Grid.useBreakpoint()

  return (
    <Modal
      open={Boolean(jobId)}
      onCancel={() => setActiveLogJobId(null)}
      footer={null}
      width={screens.md ? 820 : '96vw'}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { paddingTop: 4 } }}
    >
      {jobId && <UpdateLogModalBody key={jobId} jobId={jobId} onClose={() => setActiveLogJobId(null)} />}
    </Modal>
  )
}

function UpdateLogModalBody({ jobId, onClose }: { jobId: string; onClose: () => void }) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const liveJob = useUIStore((s) => s.updateQueueJobs[jobId])
  const [fetchedJob, setFetchedJob] = useState<UpdateJobItem | null>(null)
  const [hasAnyOutput, setHasAnyOutput] = useState(false)

  const containerRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const writtenOffsetRef = useRef<number>(0)
  const endedWithCRRef = useRef<boolean>(false)
  const rawFallbackRef = useRef<string>('')

  const job = liveJob ?? fetchedJob
  const status = job?.status ?? 'running'

  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    let disposed = false
    endedWithCRRef.current = false
    writtenOffsetRef.current = 0
    rawFallbackRef.current = ''

    const normalizeForXterm = (input: string): string => {
      if (!input) return ''
      let out = ''
      for (let i = 0; i < input.length; i++) {
        const ch = input[i]
        if (ch === '\n') {
          out += endedWithCRRef.current ? '\n' : '\r\n'
          endedWithCRRef.current = false
        } else {
          out += ch
          endedWithCRRef.current = ch === '\r'
        }
      }
      return out
    }

    const term = new Terminal({
      disableStdin: true,
      cursorBlink: false,
      cursorInactiveStyle: 'none',
      convertEol: false,
      fontFamily: 'JetBrains Mono, Consolas, "Courier New", monospace',
      fontSize: 12.5,
      lineHeight: 1.38,
      scrollback: 6000,
      theme: {
        background: '#0a0a0a',
        foreground: '#e5e7eb',
        cursor: '#0a0a0a',
        selectionBackground: 'rgba(59, 130, 246, 0.36)',
        black: '#1f2937',
        red: '#f87171',
        green: '#34d399',
        yellow: '#fbbf24',
        blue: '#60a5fa',
        magenta: '#c084fc',
        cyan: '#22d3ee',
        white: '#f3f4f6',
        brightBlack: '#6b7280',
        brightRed: '#fca5a5',
        brightGreen: '#6ee7b7',
        brightYellow: '#fde68a',
        brightBlue: '#93c5fd',
        brightMagenta: '#d8b4fe',
        brightCyan: '#67e8f9',
        brightWhite: '#ffffff',
      },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(el)
    termRef.current = term
    fitAddonRef.current = fitAddon

    const safeFit = () => {
      if (disposed) return
      try {
        fitAddon.fit()
      } catch {
        // ignore layout transition fit errors
      }
    }
    requestAnimationFrame(safeFit)
    const fitTimer = setTimeout(safeFit, 120)

    const ro = new ResizeObserver(() => {
      requestAnimationFrame(safeFit)
    })
    ro.observe(el)

    let baselineReady = false
    const pendingChunks: { chunk: string; offset?: number }[] = []

    const writeChunkWithDedup = (chunk: string, offset?: number) => {
      if (!chunk || disposed) return
      const encoder = new TextEncoder()
      if (offset !== undefined) {
        if (offset <= writtenOffsetRef.current) {
          return
        }
        const chunkBytes = encoder.encode(chunk)
        const expectedStart = offset - chunkBytes.length
        if (expectedStart < writtenOffsetRef.current && expectedStart >= 0) {
          const sliceFrom = writtenOffsetRef.current - expectedStart
          const tail = new TextDecoder().decode(chunkBytes.slice(sliceFrom))
          writtenOffsetRef.current = offset
          if (tail) {
            rawFallbackRef.current += tail
            setHasAnyOutput(true)
            term.write(normalizeForXterm(tail))
          }
          return
        }
        writtenOffsetRef.current = offset
      } else {
        writtenOffsetRef.current += encoder.encode(chunk).length
      }
      rawFallbackRef.current += chunk
      setHasAnyOutput(true)
      term.write(normalizeForXterm(chunk))
    }

    const unsubscribe = subscribeJobLog(jobId, (chunk, offset) => {
      if (!baselineReady) {
        pendingChunks.push({ chunk, offset })
        return
      }
      writeChunkWithDedup(chunk, offset)
    })

    ;(async () => {
      try {
        const res = await api.getUpdateJob(jobId)
        if (disposed) return
        setFetchedJob(res)
        useUIStore.getState().upsertUpdateJob(res)
        let baseLogs = res.logs ?? ''
        if (!baseLogs && (res.status === 'failed' || res.status === 'succeeded')) {
          const headerLine = `\x1b[36m==> ${res.appName} / ${res.serviceName} (${res.oldImageRef} -> ${res.targetImageRef})\x1b[0m\n`
          if (res.status === 'failed' && (res.errorMsg || res.errorCode)) {
            baseLogs = `${headerLine}\x1b[31m✖ ${res.errorMsg || res.errorCode}\x1b[0m\n`
          } else if (res.status === 'succeeded') {
            baseLogs = `${headerLine}\x1b[32m✓ Update completed (${res.targetImageRef})\x1b[0m\n`
          }
        }
        const byteLen = res.logs ? new TextEncoder().encode(res.logs).length : 0
        writtenOffsetRef.current = res.logOffset ?? byteLen
        if (baseLogs) {
          rawFallbackRef.current += baseLogs
          setHasAnyOutput(true)
          term.write(normalizeForXterm(baseLogs))
        }
      } catch {
        // allow SSE stream to continue even if initial fetch fails
      } finally {
        if (!disposed) {
          baselineReady = true
          for (const item of pendingChunks) {
            writeChunkWithDedup(item.chunk, item.offset)
          }
          pendingChunks.length = 0
        }
      }
    })()

    return () => {
      disposed = true
      clearTimeout(fitTimer)
      unsubscribe()
      ro.disconnect()
      termRef.current = null
      fitAddonRef.current = null
      term.dispose()
    }
  }, [jobId])

  // 任务刚结束时补拉一次最终落盘日志（防止尾行在状态切换瞬间未走增量流）
  useEffect(() => {
    if (status !== 'succeeded' && status !== 'failed') return
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.getUpdateJob(jobId)
        if (cancelled) return
        setFetchedJob(res)
        const fullLogs = res.logs ?? ''
        if (!fullLogs || !termRef.current) return
        const fullBytes = new TextEncoder().encode(fullLogs)
        if (fullBytes.length > writtenOffsetRef.current) {
          const tail = new TextDecoder().decode(fullBytes.slice(writtenOffsetRef.current))
          writtenOffsetRef.current = fullBytes.length
          if (tail) {
            rawFallbackRef.current += tail
            setHasAnyOutput(true)
            let out = ''
            for (let i = 0; i < tail.length; i++) {
              const ch = tail[i]
              if (ch === '\n') {
                out += endedWithCRRef.current ? '\n' : '\r\n'
                endedWithCRRef.current = false
              } else {
                out += ch
                endedWithCRRef.current = ch === '\r'
              }
            }
            termRef.current.write(out)
          }
        }
      } catch {
        // ignore
      }
    })()
    return () => {
      cancelled = true
    }
  }, [jobId, status])

  const handleCopyLogs = async () => {
    let text = ''
    const term = termRef.current
    if (term) {
      const buf = term.buffer.active
      const lines: string[] = []
      for (let i = 0; i < buf.length; i++) {
        const line = buf.getLine(i)
        if (line) {
          lines.push(line.translateToString(true))
        }
      }
      while (lines.length > 0 && lines[lines.length - 1] === '') {
        lines.pop()
      }
      text = lines.join('\n')
    }
    if (!text.trim() && rawFallbackRef.current) {
      // eslint-disable-next-line no-control-regex
      text = rawFallbackRef.current.replace(/\x1b\[[0-9;]*[A-Za-z]/g, '')
    }
    if (!text.trim()) {
      message.info(t.logModal.emptyCopyMsg)
      return
    }
    try {
      await navigator.clipboard.writeText(text)
      message.success(t.logModal.copiedMsg)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
      message.success(t.logModal.copiedMsg)
    }
  }

  const displayName =
    job?.serviceName && job.serviceName !== 'main' && job.serviceName !== job.appName
      ? `${job.appName} (${job.serviceName})`
      : job?.appName || jobId

  const renderStatusBadge = () => {
    if (status === 'succeeded') {
      return (
        <span
          style={{
            padding: '2px 9px',
            borderRadius: 999,
            background: 'rgba(16, 185, 129, 0.14)',
            color: '#10b981',
            fontSize: 12,
            fontWeight: 600,
          }}
        >
          {t.logModal.badgeSucceeded}
        </span>
      )
    }
    if (status === 'failed') {
      return (
        <span
          style={{
            padding: '2px 9px',
            borderRadius: 999,
            background: 'rgba(239, 68, 68, 0.14)',
            color: '#ef4444',
            fontSize: 12,
            fontWeight: 600,
          }}
        >
          {t.logModal.badgeFailed}
        </span>
      )
    }
    return (
      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 6,
          padding: '2px 10px',
          borderRadius: 999,
          background: 'rgba(37, 99, 235, 0.12)',
          color: token.colorPrimary,
          fontSize: 12,
          fontWeight: 600,
        }}
      >
        <span
          style={{
            width: 7,
            height: 7,
            borderRadius: '50%',
            background: token.colorPrimary,
          }}
        />
        {t.logModal.badgeRunning}
      </span>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {/* 顶栏 */}
      <div
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorBorderSecondary}`,
        }}
      >
        <div style={{ minWidth: 0, flex: 1 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 16, fontWeight: 700, color: token.colorText }}>
              {t.logModal.title.replace('{name}', displayName)}
            </span>
            {renderStatusBadge()}
          </div>
          {job && (
            <div
              style={{
                fontSize: 12,
                color: token.colorTextSecondary,
                marginTop: 3,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              {job.hostName} ·{' '}
              <code style={{ fontFamily: 'monospace', color: token.colorText }}>
                {job.targetImageRef}
              </code>
            </div>
          )}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
          <Button size="small" icon={<CopyOutlined />} onClick={handleCopyLogs}>
            {t.logModal.copyBtn}
          </Button>
          <Button size="small" type="primary" onClick={onClose}>
            {t.logModal.closeBtn}
          </Button>
        </div>
      </div>

      {/* 只读 xterm.js 终端视口 */}
      <div
        style={{
          position: 'relative',
          borderRadius: 12,
          background: '#0a0a0a',
          border: '1px solid #262626',
          padding: '10px 8px 10px 12px',
          overflow: 'hidden',
        }}
      >
        {!hasAnyOutput && (status === 'queued' || status === 'running') && (
          <div
            style={{
              position: 'absolute',
              top: 14,
              left: 16,
              fontSize: 12,
              fontFamily: 'JetBrains Mono, Consolas, monospace',
              color: '#9ca3af',
              pointerEvents: 'none',
              zIndex: 2,
            }}
          >
            {t.logModal.waitingOutput}
          </div>
        )}
        <div
          ref={containerRef}
          style={{
            width: '100%',
            height: 400,
          }}
        />
      </div>
    </div>
  )
}
