import { theme } from 'antd'
import { useI18n } from '../i18n'

interface LockableVersionBarProps {
  segments: string[]
  depth: number
  active: boolean
  onDepthChange: (depth: number) => void
}

export default function LockableVersionBar({
  segments,
  depth,
  active,
  onDepthChange,
}: LockableVersionBarProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()

  if (segments.length < 2) return null

  const maxDepth = Math.max(1, segments.length - 1)
  const safeDepth = Math.min(Math.max(1, depth), maxDepth)
  const lockedStr = segments.slice(0, safeDepth).join('.')

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 4 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 4 }}>
        {segments.map((num, idx) => {
          const isLast = idx === segments.length - 1
          const pos = idx + 1
          const locked = pos <= safeDepth

          const chipNode = isLast ? (
            <span
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                padding: '3px 10px',
                borderRadius: 8,
                border: `1px dashed ${token.colorBorder}`,
                color: token.colorTextSecondary,
                fontFamily: 'monospace',
                fontSize: 12,
                userSelect: 'none',
              }}
            >
              {num}
            </span>
          ) : (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onDepthChange(pos)
              }}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 5,
                padding: '3px 10px',
                borderRadius: 8,
                fontFamily: 'monospace',
                fontSize: 12,
                cursor: 'pointer',
                transition: 'all 0.15s ease',
                ...(locked
                  ? active
                    ? {
                        border: `1px solid ${token.colorPrimary}`,
                        background: token.colorPrimary,
                        color: '#ffffff',
                        fontWeight: 600,
                      }
                    : {
                        border: '1px solid rgba(47, 107, 255, 0.4)',
                        background: 'rgba(47, 107, 255, 0.10)',
                        color: token.colorPrimary,
                        fontWeight: 600,
                      }
                  : {
                      border: `1px solid ${token.colorBorder}`,
                      background: token.colorBgContainer,
                      color: token.colorText,
                      fontWeight: 400,
                    }),
              }}
            >
              {locked && (
                <svg
                  width="13"
                  height="13"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2.2"
                  style={{ flexShrink: 0 }}
                >
                  <rect x="4" y="11" width="16" height="10" rx="2" />
                  <path d="M8 11V7a4 4 0 018 0v4" />
                </svg>
              )}
              <span>{num}</span>
            </button>
          )

          return (
            <span key={idx} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              {chipNode}
              {!isLast && (
                <span
                  style={{
                    color: token.colorTextSecondary,
                    fontWeight: 700,
                    padding: '0 1px',
                  }}
                >
                  .
                </span>
              )}
            </span>
          )
        })}
      </div>

      <div style={{ fontSize: 12, color: token.colorTextSecondary }}>
        {t.ignoreModal.lockBarCaptionBefore}
        <code
          style={{
            fontFamily: 'monospace',
            color: token.colorText,
            fontWeight: 600,
          }}
        >
          {lockedStr}
        </code>
        {t.ignoreModal.lockBarCaptionAfter}
      </div>
    </div>
  )
}
