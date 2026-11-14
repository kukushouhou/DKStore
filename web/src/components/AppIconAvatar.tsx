import { useState } from 'react'
import { Avatar, theme } from 'antd'
import { resolveIconUrl } from '../api/client'

const AVATAR_COLORS = ['#1677ff', '#13c2c2', '#722ed1', '#eb2f96', '#fa8c16', '#52c41a']

function pickAvatarColor(name: string): string {
  let h = 0
  for (let i = 0; i < name.length; i++) {
    h = (h * 31 + name.charCodeAt(i)) | 0
  }
  return AVATAR_COLORS[Math.abs(h) % AVATAR_COLORS.length]
}

function getInitials(name: string): string {
  const cleaned = name.replace(/[^a-zA-Z0-9\u4e00-\u9fa5]/g, '')
  return (cleaned.slice(0, 2) || name.slice(0, 2) || 'AP').toUpperCase()
}

interface AppIconAvatarProps {
  name: string
  fallbackColorSeed?: string
  iconUrl?: string
  size?: number
  dimmed?: boolean
}

export default function AppIconAvatar({
  name,
  fallbackColorSeed,
  iconUrl,
  size = 40,
  dimmed = false,
}: AppIconAvatarProps) {
  const { token } = theme.useToken()
  const iconSrc = resolveIconUrl(iconUrl)
  const [failedIconSrc, setFailedIconSrc] = useState<string | null>(null)
  const imgErr = Boolean(iconSrc && failedIconSrc === iconSrc)
  const hasValidIcon = Boolean(iconSrc && !imgErr)
  const colorSeed = fallbackColorSeed || name
  const pad = size >= 38 ? 5 : 4

  return (
    <Avatar
      shape="square"
      size={size}
      src={
        hasValidIcon ? (
          <img
            src={iconSrc!}
            alt={name}
            onError={() => setFailedIconSrc(iconSrc ?? null)}
            style={{ objectFit: 'contain' }}
          />
        ) : undefined
      }
      onError={() => {
        setFailedIconSrc(iconSrc ?? null)
        return false
      }}
      style={{
        borderRadius: token.borderRadius,
        backgroundColor: hasValidIcon ? token.colorBgContainer : pickAvatarColor(colorSeed),
        border: hasValidIcon ? `1px solid ${token.colorBorderSecondary}` : 'none',
        padding: hasValidIcon ? pad : 0,
        flexShrink: 0,
        fontWeight: 600,
        fontSize: size <= 32 ? 12 : token.fontSize,
        opacity: dimmed ? 0.85 : 1,
      }}
    >
      {getInitials(name)}
    </Avatar>
  )
}
