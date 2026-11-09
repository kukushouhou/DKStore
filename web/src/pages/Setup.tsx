import { useState } from 'react'
import { Alert, Button, Card, Flex, Input, Segmented, Space, Typography, message } from 'antd'
import { LockOutlined } from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { useI18n } from '../i18n'
import type { Locale } from '../store/ui'

const MIN_PW = 6

/** 首次初始化页：必须设置 DKStore 管理员密码，不可跳过（实施文档 §10.1.1）。 */
export default function SetupPage({ deviceAuthHint }: { deviceAuthHint?: string }) {
  const { locale, setLocale, t } = useI18n()
  const qc = useQueryClient()
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [saving, setSaving] = useState(false)

  const tooShort = pw.length > 0 && [...pw].length < MIN_PW
  const mismatch = pw2.length > 0 && pw !== pw2

  const onSubmit = async () => {
    if ([...pw].length < MIN_PW) {
      message.error(t.settings.pwTooShort)
      return
    }
    if (pw !== pw2) {
      message.error(t.settings.pwMismatch)
      return
    }
    setSaving(true)
    try {
      await api.setupPassword(pw)
      setPw('')
      setPw2('')
      message.success(t.auth.setupDone)
      await qc.invalidateQueries()
    } catch (e) {
      message.error((e as Error).message)
      // 可能已被其他人设置过密码，刷新状态以切换到登录页
      qc.invalidateQueries({ queryKey: ['authStatus'] })
    } finally {
      setSaving(false)
    }
  }

  return (
    <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
      <Card style={{ width: 420 }}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Flex justify="space-between" align="center">
            <Typography.Title level={4} style={{ margin: 0 }}>
              <LockOutlined /> {t.auth.setupTitle}
            </Typography.Title>
            <Segmented
              size="small"
              value={locale}
              onChange={(v) => setLocale(v as Locale)}
              options={[
                { label: '中文', value: 'zh-CN' },
                { label: 'EN', value: 'en-US' },
              ]}
            />
          </Flex>
          <Typography.Text type="secondary">{t.auth.setupDesc}</Typography.Text>
          {deviceAuthHint && (
            <Alert type="info" showIcon title={t.auth.deviceHintTitle} description={deviceAuthHint} />
          )}
          <Input.Password
            placeholder={t.auth.newPw}
            value={pw}
            status={tooShort ? 'error' : undefined}
            onChange={(e) => setPw(e.target.value)}
            autoComplete="new-password"
          />
          <Input.Password
            placeholder={t.auth.confirmPw}
            value={pw2}
            status={mismatch ? 'error' : undefined}
            onChange={(e) => setPw2(e.target.value)}
            onPressEnter={onSubmit}
            autoComplete="new-password"
          />
          {(tooShort || mismatch) && (
            <Typography.Text type="danger">
              {tooShort ? t.settings.pwTooShort : t.settings.pwMismatch}
            </Typography.Text>
          )}
          <Button type="primary" block loading={saving} onClick={onSubmit}>
            {t.auth.setupBtn}
          </Button>
        </Space>
      </Card>
    </Flex>
  )
}
