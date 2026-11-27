import { type CSSProperties, useState } from 'react'
import { Button, Flex, Space, Typography, theme } from 'antd'
import {
  CloseOutlined,
  DownOutlined,
  ExclamationCircleFilled,
  InfoCircleFilled,
} from '@ant-design/icons'

export interface StatusNoticeRow {
  key: string
  name: string
  sub?: string
  reason: string
  actionText?: string
  onAction?: () => void
}

export interface StatusNoticeBarProps {
  level: 'error' | 'warning' | 'info'
  title: string
  summary?: string
  expandText?: string
  collapseText?: string
  actionText?: string
  primaryAction?: boolean
  onAction?: () => void
  onClose?: () => void
  rows?: StatusNoticeRow[]
  defaultExpanded?: boolean
  style?: CSSProperties
}

export default function StatusNoticeBar({
  level,
  title,
  summary,
  expandText = '展开明细',
  collapseText = '收起',
  actionText,
  primaryAction = false,
  onAction,
  onClose,
  rows,
  defaultExpanded = false,
  style,
}: StatusNoticeBarProps) {
  const { token } = theme.useToken()
  const [expanded, setExpanded] = useState(defaultExpanded)

  const hasRows = Boolean(rows && rows.length > 0)
  const accentColor =
    level === 'error'
      ? token.colorError
      : level === 'warning'
        ? token.colorWarning
        : token.colorPrimary

  return (
    <div
      style={{
        borderRadius: token.borderRadiusLG,
        background: token.colorBgContainer,
        border: `1px solid ${token.colorBorderSecondary}`,
        padding: '10px 16px',
        ...style,
      }}
    >
      <Flex wrap="wrap" align="center" justify="space-between" gap="8px 16px">
        <Flex align="center" gap={8} style={{ minWidth: 0, flex: 1 }}>
          {level === 'info' ? (
            <InfoCircleFilled style={{ color: accentColor, fontSize: 15, flexShrink: 0 }} />
          ) : (
            <ExclamationCircleFilled style={{ color: accentColor, fontSize: 15, flexShrink: 0 }} />
          )}

          <Typography.Text strong style={{ fontSize: token.fontSize, flexShrink: 0 }}>
            {title}
          </Typography.Text>

          {!expanded && summary && (
            <Typography.Text
              type="secondary"
              ellipsis={{ tooltip: summary }}
              style={{ fontSize: token.fontSizeSM, minWidth: 0 }}
            >
              {summary}
            </Typography.Text>
          )}
        </Flex>

        {(hasRows || actionText || onClose) && (
          <Space size={12} align="center" style={{ flexShrink: 0 }}>
            {hasRows && (
              <button
                type="button"
                onClick={() => setExpanded((v) => !v)}
                style={{
                  border: 'none',
                  background: 'transparent',
                  padding: 0,
                  cursor: 'pointer',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 4,
                  fontSize: token.fontSizeSM,
                  color: token.colorTextSecondary,
                  fontFamily: 'inherit',
                  lineHeight: 1.4,
                }}
              >
                <span>{expanded ? collapseText : expandText}</span>
                <DownOutlined
                  style={{
                    fontSize: 10,
                    transition: 'transform 0.2s ease',
                    transform: expanded ? 'rotate(180deg)' : 'none',
                  }}
                />
              </button>
            )}

            {actionText && onAction && (
              <Button
                size="small"
                type={primaryAction ? 'primary' : 'default'}
                onClick={onAction}
              >
                {actionText}
              </Button>
            )}

            {onClose && (
              <Button
                type="text"
                size="small"
                icon={<CloseOutlined style={{ fontSize: 12 }} />}
                onClick={onClose}
                style={{ width: 22, height: 22, color: token.colorTextTertiary }}
              />
            )}
          </Space>
        )}
      </Flex>

      {expanded && hasRows && (
        <div
          style={{
            marginTop: 10,
            paddingTop: 2,
            borderTop: `1px solid ${token.colorSplit}`,
          }}
        >
          {rows!.map((row, idx) => (
            <div
              key={row.key}
              style={{
                padding: '7px 0',
                borderTop: idx > 0 ? `1px solid ${token.colorSplit}` : undefined,
                display: 'flex',
                flexWrap: 'wrap',
                alignItems: 'center',
                justifyContent: 'space-between',
                gap: '4px 16px',
                fontSize: token.fontSizeSM,
              }}
            >
              <Flex align="center" gap={10} style={{ minWidth: 0 }}>
                <span
                  style={{
                    width: 6,
                    height: 6,
                    borderRadius: '50%',
                    background: accentColor,
                    flexShrink: 0,
                  }}
                />
                <Typography.Text
                  strong
                  ellipsis={{ tooltip: row.name }}
                  style={{ fontSize: token.fontSizeSM, minWidth: 96, maxWidth: 220 }}
                >
                  {row.name}
                </Typography.Text>
                {row.sub && (
                  <Typography.Text
                    type="secondary"
                    ellipsis={{ tooltip: row.sub }}
                    style={{ fontSize: token.fontSizeSM }}
                  >
                    {row.sub}
                  </Typography.Text>
                )}
              </Flex>

              <Flex align="center" gap={12} style={{ minWidth: 0 }}>
                <Typography.Text
                  type="secondary"
                  style={{ fontSize: token.fontSizeSM, wordBreak: 'break-word' }}
                >
                  {row.reason}
                </Typography.Text>
                {row.actionText && row.onAction && (
                  <Typography.Link
                    onClick={row.onAction}
                    style={{ fontSize: token.fontSizeSM, fontWeight: 500, whiteSpace: 'nowrap' }}
                  >
                    {row.actionText}
                  </Typography.Link>
                )}
              </Flex>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
