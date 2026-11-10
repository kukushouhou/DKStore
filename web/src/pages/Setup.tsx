import { useState } from 'react'
import { Button, Card, Col, Flex, Input, Row, Segmented, Space, Tag, Typography, message, theme } from 'antd'
import {
  ContainerOutlined,
  DownOutlined,
  HddOutlined,
  LockOutlined,
  UpOutlined,
} from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { api, type AuthStatus } from '../api/client'
import { useI18n } from '../i18n'
import type { Locale } from '../store/ui'

const MIN_PW = 6

interface Props {
  authStatus?: AuthStatus
  onStepChange?: (inStep2: boolean) => void
  onDone: () => void
}

/** 首次初始化向导：第 1 步设置管理员密码；若未自动连上本机容器则进入第 2 步本机连接状态说明。 */
export default function SetupPage({ authStatus, onStepChange, onDone }: Props) {
  const { locale, setLocale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const [step, setStep] = useState<1 | 2>(1)
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [saving, setSaving] = useState(false)
  const [showDetails, setShowDetails] = useState(false)

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
      if (authStatus?.hasLocalHost) {
        onStepChange?.(false)
        await qc.invalidateQueries()
        onDone()
      } else {
        onStepChange?.(true)
        setStep(2)
        await qc.invalidateQueries()
      }
    } catch (e) {
      message.error((e as Error).message)
      qc.invalidateQueries({ queryKey: ['authStatus'] })
    } finally {
      setSaving(false)
    }
  }

  const localReasonText = () => {
    switch (authStatus?.localStatus) {
      case 'socket_missing':
        return t.auth.step2ReasonSocketMissing
      case 'permission_denied':
        return t.auth.step2ReasonPermDenied
      default:
        return t.auth.step2ReasonOther
    }
  }

  if (step === 2) {
    return (
      <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
        <Card style={{ width: '100%', maxWidth: 680 }}>
          <Space direction="vertical" size="large" style={{ width: '100%' }}>
            <Flex justify="flex-end">
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

            {/* 顶部直观状态图示 */}
            <Flex vertical align="center" gap={12} style={{ padding: '4px 0' }}>
              <Flex align="center" justify="center" gap={12}>
                <Tag
                  icon={<HddOutlined />}
                  style={{
                    padding: '6px 14px',
                    fontSize: 14,
                    borderRadius: 8,
                    margin: 0,
                  }}
                >
                  {t.auth.step2DiagramLeft}
                </Tag>
                <Typography.Text type="secondary" style={{ letterSpacing: 2, userSelect: 'none' }}>
                  ┄┄ ⚪ ┄┄
                </Typography.Text>
                <Tag
                  icon={<ContainerOutlined />}
                  style={{
                    padding: '6px 14px',
                    fontSize: 14,
                    borderRadius: 8,
                    margin: 0,
                  }}
                >
                  {t.auth.step2DiagramRight}
                </Tag>
              </Flex>
              <Typography.Title level={4} style={{ margin: 0, textAlign: 'center' }}>
                {t.auth.step2Title}
              </Typography.Title>
            </Flex>

            {/* 中部两张对称、中立的图文说明卡片 */}
            <Row gutter={[16, 16]} align="stretch">
              <Col xs={24} md={12} style={{ display: 'flex' }}>
                <Card
                  size="small"
                  style={{
                    width: '100%',
                    background: token.colorFillAlter,
                    borderRadius: 10,
                  }}
                >
                  <Space direction="vertical" size={10} style={{ width: '100%' }}>
                    <Typography.Text strong style={{ fontSize: 15 }}>
                      {t.auth.step2CardLocalTitle}
                    </Typography.Text>
                    <Typography.Paragraph style={{ marginBottom: 0, fontSize: 13 }}>
                      <Typography.Text strong>{t.auth.step2CardLocalPkgLabel}</Typography.Text>
                      <Typography.Text type="secondary">{t.auth.step2CardLocalPkgDesc}</Typography.Text>
                    </Typography.Paragraph>
                    <Typography.Paragraph style={{ marginBottom: 0, fontSize: 13 }}>
                      <Typography.Text strong>{t.auth.step2CardLocalDockerLabel}</Typography.Text>
                      <Typography.Text type="secondary">{t.auth.step2CardLocalDockerDesc}</Typography.Text>
                    </Typography.Paragraph>
                  </Space>
                </Card>
              </Col>
              <Col xs={24} md={12} style={{ display: 'flex' }}>
                <Card
                  size="small"
                  style={{
                    width: '100%',
                    background: token.colorFillAlter,
                    borderRadius: 10,
                  }}
                >
                  <Space direction="vertical" size={10} style={{ width: '100%' }}>
                    <Typography.Text strong style={{ fontSize: 15 }}>
                      {t.auth.step2CardRemoteTitle}
                    </Typography.Text>
                    <Typography.Paragraph type="secondary" style={{ marginBottom: 0, fontSize: 13 }}>
                      {t.auth.step2CardRemoteDesc}
                    </Typography.Paragraph>
                  </Space>
                </Card>
              </Col>
            </Row>

            {/* 展开的技术详情 */}
            {showDetails && (
              <Card
                size="small"
                style={{
                  background: token.colorBgLayout,
                  borderRadius: 8,
                }}
              >
                <Space direction="vertical" size={6} style={{ width: '100%' }}>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {localReasonText()}
                  </Typography.Text>
                  <div>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t.auth.step2MountRef}{' '}
                    </Typography.Text>
                    <Typography.Text code style={{ fontSize: 12 }}>
                      -v /var/run/docker.sock:/var/run/docker.sock
                    </Typography.Text>
                  </div>
                </Space>
              </Card>
            )}

            {/* 底部按钮栏：左下角 [查看详情 ▾]，右下角 [跳过] */}
            <Flex justify="space-between" align="center">
              <Button
                type="text"
                size="small"
                icon={showDetails ? <UpOutlined /> : <DownOutlined />}
                onClick={() => setShowDetails((v) => !v)}
              >
                {t.auth.step2ViewDetails}
              </Button>
              <Button
                type="primary"
                onClick={() => {
                  onStepChange?.(false)
                  onDone()
                }}
              >
                {t.auth.step2SkipBtn}
              </Button>
            </Flex>
          </Space>
        </Card>
      </Flex>
    )
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
