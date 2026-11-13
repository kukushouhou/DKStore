import { MoonOutlined, SunOutlined } from '@ant-design/icons'
import { Button, Segmented, Tooltip } from 'antd'
import { useEffectiveDark } from '../hooks/useEffectiveDark'
import { useI18n } from '../i18n'
import { useUIStore, type Locale } from '../store/ui'

export default function AuthToolbar() {
  const { locale, setLocale, t } = useI18n()
  const setThemeMode = useUIStore((s) => s.setThemeMode)
  const isDark = useEffectiveDark()

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      <Tooltip title={isDark ? t.auth.themeSwitchLight : t.auth.themeSwitchDark}>
        <Button
          size="small"
          type="default"
          icon={isDark ? <SunOutlined /> : <MoonOutlined />}
          aria-label={isDark ? t.auth.themeSwitchLight : t.auth.themeSwitchDark}
          onClick={() => setThemeMode(isDark ? 'light' : 'dark')}
          style={{ borderRadius: 8 }}
        />
      </Tooltip>
      <Segmented
        size="small"
        value={locale}
        onChange={(v) => setLocale(v as Locale)}
        options={[
          { label: '中文', value: 'zh-CN' },
          { label: 'EN', value: 'en-US' },
        ]}
      />
    </div>
  )
}
