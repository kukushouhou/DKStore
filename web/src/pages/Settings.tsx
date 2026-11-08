import { Card, Segmented, Space, Typography } from 'antd'
import { useUIStore } from '../store/ui'

export default function SettingsPage() {
  const { themeMode, setThemeMode } = useUIStore()
  return (
    <>
      <Typography.Title level={3}>设置</Typography.Title>
      <Card>
        <Space>
          <Typography.Text>外观</Typography.Text>
          <Segmented
            value={themeMode}
            onChange={(v) => setThemeMode(v as typeof themeMode)}
            options={[
              { label: '跟随系统', value: 'system' },
              { label: '浅色', value: 'light' },
              { label: '深色', value: 'dark' },
            ]}
          />
        </Space>
      </Card>
    </>
  )
}
