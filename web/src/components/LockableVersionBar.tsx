import { Button, Flex, Space, Tag, Typography, theme } from 'antd'
import { LockFilled } from '@ant-design/icons'
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
    <Flex vertical gap={6} style={{ marginTop: 6 }}>
      <Space size={4} wrap align="center">
        {segments.map((num, idx) => {
          const isLast = idx === segments.length - 1
          const pos = idx + 1
          const locked = pos <= safeDepth

          const chipNode = isLast ? (
            <Tag
              variant="outlined"
              style={{
                margin: 0,
                borderStyle: 'dashed',
                fontFamily: token.fontFamilyCode,
                userSelect: 'none',
              }}
            >
              {num}
            </Tag>
          ) : (
            <Button
              size="small"
              type={locked ? 'primary' : 'default'}
              ghost={locked && !active}
              icon={locked ? <LockFilled /> : undefined}
              onClick={(e) => {
                e.stopPropagation()
                onDepthChange(pos)
              }}
              style={{ fontFamily: token.fontFamilyCode }}
            >
              {num}
            </Button>
          )

          return (
            <Space key={idx} size={4} align="center">
              {chipNode}
              {!isLast && (
                <Typography.Text type="secondary" strong>
                  .
                </Typography.Text>
              )}
            </Space>
          )
        })}
      </Space>

      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
        {t.ignoreModal.lockBarCaptionBefore}
        <Typography.Text code>{lockedStr}</Typography.Text>
        {t.ignoreModal.lockBarCaptionAfter}
      </Typography.Text>
    </Flex>
  )
}
