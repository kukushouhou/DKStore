import { useEffect, useRef, useState } from 'react'
import { Button, Flex, Grid, Modal, Space, Tag, Typography, message, theme } from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CopyOutlined,
  SyncOutlined,
} from '@ant-design/icons'
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
  const initialCodeFontRef = useRef<string>(token.fontFamilyCode)

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
      fontFamily: initialCodeFontRef.current,
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

  useEffect(() => {
    if (termRef.current) {
      termRef.current.options.fontFamily = token.fontFamilyCode
    }
  }, [token.fontFamilyCode])

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
        <Tag color="success" icon={<CheckCircleOutlined />} style={{ margin: 0 }}>
          {t.logModal.badgeSucceeded}
        </Tag>
      )
    }
    if (status === 'failed') {
      return (
        <Tag color="error" icon={<CloseCircleOutlined />} style={{ margin: 0 }}>
          {t.logModal.badgeFailed}
        </Tag>
      )
    }
    return (
      <Tag color="processing" icon={<SyncOutlined spin />} style={{ margin: 0 }}>
        {t.logModal.badgeRunning}
      </Tag>
    )
  }

  return (
    <Flex vertical gap={12}>
      {/* 顶栏 */}
      <Flex
        wrap="wrap"
        align="center"
        justify="space-between"
        gap={12}
        style={{
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <div style={{ minWidth: 0, flex: 1 }}>
          <Space size={8} wrap align="center">
            <Typography.Title level={5} style={{ margin: 0 }}>
              {t.logModal.title.replace('{name}', displayName)}
            </Typography.Title>
            {renderStatusBadge()}
          </Space>
          {job && (
            <Typography.Text
              type="secondary"
              ellipsis={{ tooltip: `${job.hostName} · ${job.targetImageRef}` }}
              style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
            >
              {job.hostName} · <Typography.Text code>{job.targetImageRef}</Typography.Text>
            </Typography.Text>
          )}
        </div>

        <Space size={8} style={{ flexShrink: 0 }}>
          <Button size="small" icon={<CopyOutlined />} onClick={handleCopyLogs}>
            {t.logModal.copyBtn}
          </Button>
          <Button size="small" type="primary" onClick={onClose}>
            {t.logModal.closeBtn}
          </Button>
        </Space>
      </Flex>

      {/* 只读 xterm.js 终端视口 */}
      <div
        style={{
          position: 'relative',
          borderRadius: token.borderRadiusLG,
          background: '#0a0a0a',
          border: `1px solid ${token.colorBorderSecondary}`,
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
              fontSize: token.fontSizeSM,
              fontFamily: token.fontFamilyCode,
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
    </Flex>
  )
}
