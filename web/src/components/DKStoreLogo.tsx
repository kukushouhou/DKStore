import { useId, type CSSProperties } from 'react'

interface DKStoreLogoProps {
  size?: number
  withHalo?: boolean
  dark?: boolean
  style?: CSSProperties
}

export default function DKStoreLogo({
  size = 32,
  withHalo = false,
  dark = false,
  style,
}: DKStoreLogoProps) {
  const rawId = useId().replace(/:/g, '')
  const bgId = `dk-bg-${rawId}`

  const svg = (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      aria-label="DKStore"
      style={{
        display: 'block',
        flexShrink: 0,
        ...(!withHalo ? style : undefined),
      }}
    >
      <defs>
        <linearGradient id={bgId} x1="8" y1="4" x2="56" y2="60" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#4F8CFF" />
          <stop offset="50%" stopColor="#2F6BFF" />
          <stop offset="100%" stopColor="#153CC0" />
        </linearGradient>
      </defs>
      <rect x="2" y="2" width="60" height="60" rx="15" fill={`url(#${bgId})`} />
      <rect
        x="2.75"
        y="2.75"
        width="58.5"
        height="58.5"
        rx="14.25"
        stroke="#FFFFFF"
        strokeOpacity="0.3"
        strokeWidth="1.5"
      />
      <path d="M20 13H44" stroke="#FFFFFF" strokeWidth="4.4" strokeLinecap="round" />
      <path
        d="M13 28.5L32 14L51 28.5"
        stroke="#FFFFFF"
        strokeWidth="5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <path
        fillRule="evenodd"
        clipRule="evenodd"
        d="M17 47.5C17 49.2 18.3 50.5 20 50.5H44C45.7 50.5 47 49.2 47 47.5V41C47 31.8 40.5 25 32 25C23.5 25 17 31.8 17 41V47.5ZM22.4 45.2H41.6V41C41.6 35 37.4 30.5 32 30.5C26.6 30.5 22.4 35 22.4 41V45.2Z"
        fill="#FFFFFF"
      />
      <rect x="29.2" y="34.5" width="5.6" height="7.5" rx="2.8" fill="#67E8F9" />
    </svg>
  )

  if (!withHalo) {
    return svg
  }

  return (
    <div
      style={{
        padding: 6,
        borderRadius: 20,
        display: 'inline-flex',
        background: dark ? 'rgba(47, 107, 255, 0.12)' : 'rgba(255, 255, 255, 0.9)',
        border: dark
          ? '1px solid rgba(96, 165, 250, 0.22)'
          : '1px solid rgba(47, 107, 255, 0.14)',
        boxShadow: dark
          ? '0 12px 28px -6px rgba(47, 107, 255, 0.35)'
          : '0 12px 28px -6px rgba(47, 107, 255, 0.26)',
        ...style,
      }}
    >
      {svg}
    </div>
  )
}
