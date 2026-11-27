import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react'
import { Button, message, theme } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import '@xterm/xterm/css/xterm.css'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { api, copyTextSafe, type UpdateJobItem } from '../api/client'
import { useI18n } from '../i18n'
import { subscribeJobLog, useUIStore } from '../store/ui'

export interface TerminalLogBoxHandle {
  copyLogs: () => Promise<void>
}

interface FallbackJobMeta {
  appName?: string
  serviceName?: string
  oldImageRef?: string
  targetImageRef?: string
  errorMsg?: string
  errorCode?: string
}

interface TerminalLogBoxProps {
  jobId: string
  status?: string
  initialLogs?: string
  fallbackMeta?: FallbackJobMeta
  height?: number
  showCopyOverlay?: boolean
  onJobLoaded?: (job: UpdateJobItem) => void
}

function stripAnsiCodes(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '')
}

function lastNonEmptyPlainLine(raw: string): string {
  const plain = stripAnsiCodes(raw).replace(/\r\n/g, '\n').replace(/\r/g, '\n')
  const parts = plain.split('\n')
  for (let i = parts.length - 1; i >= 0; i--) {
    const trimmed = parts[i].trim()
    if (trimmed) return trimmed
  }
  return ''
}

function buildFallbackLogs(meta?: FallbackJobMeta, status?: string): string {
  if (!meta || (status !== 'failed' && status !== 'succeeded')) return ''
  const headerLine = `\u001b[36m==> ${meta.appName || ''} / ${meta.serviceName || ''} (${meta.oldImageRef || ''} -> ${meta.targetImageRef || ''})\u001b[0m\n`
  if (status === 'failed' && (meta.errorMsg || meta.errorCode)) {
    return `${headerLine}\u001b[31m✖ ${meta.errorMsg || meta.errorCode}\u001b[0m\n`
  }
  if (status === 'succeeded') {
    return `${headerLine}\u001b[32m✓ Update completed (${meta.targetImageRef || ''})\u001b[0m\n`
  }
  return ''
}

const TerminalLogBox = forwardRef<TerminalLogBoxHandle, TerminalLogBoxProps>(
  function TerminalLogBox(
    {
      jobId,
      status = 'running',
      initialLogs,
      fallbackMeta,
      height = 400,
      showCopyOverlay = false,
      onJobLoaded,
    },
    ref,
  ) {
    const { token } = theme.useToken()
    const { t } = useI18n()
    const [hasAnyOutput, setHasAnyOutput] = useState(Boolean(initialLogs))

    const containerRef = useRef<HTMLDivElement | null>(null)
    const termRef = useRef<Terminal | null>(null)
    const fitAddonRef = useRef<FitAddon | null>(null)
    const writtenOffsetRef = useRef<number>(0)
    const endedWithCRRef = useRef<boolean>(false)
    const rawFallbackRef = useRef<string>('')
    const wasActiveRef = useRef<boolean>(status === 'queued' || status === 'running')
    const initialCodeFontRef = useRef<string>(token.fontFamilyCode)
    const initialLogsRef = useRef<string | undefined>(initialLogs)
    const initialStatusRef = useRef<string>(status)
    const fallbackMetaRef = useRef<FallbackJobMeta | undefined>(fallbackMeta)
    const onJobLoadedRef = useRef<((job: UpdateJobItem) => void) | undefined>(onJobLoaded)

    useEffect(() => {
      onJobLoadedRef.current = onJobLoaded
    }, [onJobLoaded])

    useEffect(() => {
      fallbackMetaRef.current = fallbackMeta
    }, [fallbackMeta])

    useEffect(() => {
      if (status === 'queued' || status === 'running') {
        wasActiveRef.current = true
      }
    }, [status])

    const handleCopyLogs = useCallback(async () => {
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
        text = stripAnsiCodes(rawFallbackRef.current)
      }
      if (!text.trim()) {
        message.info(t.logModal.emptyCopyMsg)
        return
      }
      await copyTextSafe(text)
      message.success(t.logModal.copiedMsg)
    }, [t.logModal.copiedMsg, t.logModal.emptyCopyMsg])

    useImperativeHandle(
      ref,
      () => ({
        copyLogs: handleCopyLogs,
      }),
      [handleCopyLogs],
    )

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
      const fitTimer1 = setTimeout(safeFit, 120)
      const fitTimer2 = setTimeout(safeFit, 260)

      const ro = new ResizeObserver(() => {
        requestAnimationFrame(safeFit)
      })
      ro.observe(el)

      const initSt = initialStatusRef.current
      const preloaded = initialLogsRef.current
      const isFinishedAtMount = initSt === 'succeeded' || initSt === 'failed'

      // 已结束记录若上层已持有完整日志，直接秒开渲染，无需再发请求
      if (isFinishedAtMount && preloaded) {
        rawFallbackRef.current = preloaded
        writtenOffsetRef.current = new TextEncoder().encode(preloaded).length
        setHasAnyOutput(true)
        term.write(normalizeForXterm(preloaded))
        return () => {
          disposed = true
          clearTimeout(fitTimer1)
          clearTimeout(fitTimer2)
          ro.disconnect()
          termRef.current = null
          fitAddonRef.current = null
          term.dispose()
        }
      }

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
          onJobLoadedRef.current?.(res)
          const curSt = useUIStore.getState()
          if (curSt.updateQueueJobs[jobId] || res.status === 'queued' || res.status === 'running') {
            curSt.upsertUpdateJob(res)
          }
          let baseLogs = res.logs || buildFallbackLogs(res, res.status)
          if (!baseLogs) {
            baseLogs = buildFallbackLogs(fallbackMetaRef.current, res.status)
          }
          const byteLen = res.logs ? new TextEncoder().encode(res.logs).length : 0
          writtenOffsetRef.current = res.logOffset && res.logOffset > 0 ? res.logOffset : byteLen
          if (baseLogs) {
            rawFallbackRef.current += baseLogs
            setHasAnyOutput(true)
            term.write(normalizeForXterm(baseLogs))
          }
        } catch {
          const fb = buildFallbackLogs(fallbackMetaRef.current, initSt)
          if (!disposed && fb) {
            rawFallbackRef.current += fb
            setHasAnyOutput(true)
            term.write(normalizeForXterm(fb))
          }
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
        clearTimeout(fitTimer1)
        clearTimeout(fitTimer2)
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

    // 从运行态刚切换为结束态时补拉一次最终落盘日志，防止尾行漏传
    useEffect(() => {
      if (!wasActiveRef.current) return
      if (status !== 'succeeded' && status !== 'failed') return
      let cancelled = false
      ;(async () => {
        try {
          const res = await api.getUpdateJob(jobId)
          if (cancelled) return
          onJobLoadedRef.current?.(res)
          const fullLogs = res.logs ?? ''
          const term = termRef.current
          if (!fullLogs || !term) return

          const wantTail = lastNonEmptyPlainLine(fullLogs)
          if (!wantTail) return

          const buf = term.buffer.active
          let curTail = ''
          for (let i = buf.length - 1; i >= 0; i--) {
            const line = buf.getLine(i)?.translateToString(true).trim() ?? ''
            if (line) {
              curTail = line
              break
            }
          }
          if (curTail.includes(wantTail) || wantTail.includes(curTail && curTail.length > 8 ? curTail : '\0')) {
            return
          }

          term.reset()
          endedWithCRRef.current = false
          rawFallbackRef.current = fullLogs
          setHasAnyOutput(true)
          let out = ''
          for (let i = 0; i < fullLogs.length; i++) {
            const ch = fullLogs[i]
            if (ch === '\n') {
              out += endedWithCRRef.current ? '\n' : '\r\n'
              endedWithCRRef.current = false
            } else {
              out += ch
              endedWithCRRef.current = ch === '\r'
            }
          }
          term.write(out)
        } catch {
          // ignore
        }
      })()
      return () => {
        cancelled = true
      }
    }, [jobId, status])

    return (
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
        {showCopyOverlay && (
          <Button
            size="small"
            icon={<CopyOutlined />}
            onClick={() => void handleCopyLogs()}
            style={{
              position: 'absolute',
              top: 8,
              right: 12,
              zIndex: 3,
              background: 'rgba(31, 41, 55, 0.86)',
              borderColor: '#374151',
              color: '#e5e7eb',
              fontSize: 12,
            }}
          >
            {t.logModal.copyBtn}
          </Button>
        )}
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
            height,
          }}
        />
      </div>
    )
  },
)

export default TerminalLogBox
