import { useState, type ReactNode } from 'react'
import {
  ArrowRightOutlined,
  CheckOutlined,
  CopyOutlined,
  DesktopOutlined,
  GlobalOutlined,
  InfoCircleOutlined,
  LockOutlined,
} from '@ant-design/icons'
import {
  Button,
  Card,
  Col,
  Divider,
  Flex,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Segmented,
  Space,
  Typography,
  message,
  theme,
} from 'antd'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError, type AuthStatus, type HostInput } from '../api/client'
import AuthToolbar from '../components/AuthToolbar'
import DKStoreLogo from '../components/DKStoreLogo'
import { useI18n } from '../i18n'

const MIN_PW = 6

interface Props {
  authStatus?: AuthStatus
  onStepChange?: (inStep2: boolean) => void
  onDone: () => void
}

const COMPOSE_SNIPPET = `volumes:
  - ./data:/data
  - /var/run/docker.sock:/var/run/docker.sock`

const DOCKER_RUN_SNIPPET = `-v /var/run/docker.sock:/var/run/docker.sock`

function HomeAssistantMiniIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 48 48" fill="none" style={{ flexShrink: 0 }}>
      <rect width="48" height="48" rx="11" fill="#18BCF2" />
      <path
        d="M24 10L38 22V37C38 38.1 37.1 39 36 39H12C10.9 39 10 38.1 10 37V22L24 10Z"
        fill="#FFFFFF"
      />
      <path
        d="M24 39V20M24 30L18 24M24 27L30 21"
        stroke="#18BCF2"
        strokeWidth="3"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx="24" cy="18.5" r="2.3" fill="#18BCF2" />
      <circle cx="17.2" cy="23.2" r="2.1" fill="#18BCF2" />
      <circle cx="30.8" cy="20.2" r="2.1" fill="#18BCF2" />
    </svg>
  )
}

function JellyfinMiniIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 48 48" fill="none" style={{ flexShrink: 0 }}>
      <defs>
        <linearGradient id="dk-jf-g" x1="6" y1="6" x2="42" y2="42" gradientUnits="userSpaceOnUse">
          <stop stopColor="#AA5CC3" />
          <stop offset="1" stopColor="#00A4DC" />
        </linearGradient>
      </defs>
      <rect width="48" height="48" rx="11" fill="url(#dk-jf-g)" />
      <path
        d="M24 12C21.2 12 11.5 29.2 13 32.8C14.5 36.4 33.5 36.4 35 32.8C36.5 29.2 26.8 12 24 12ZM24 20.5C25.2 20.5 29.2 28 28.5 29.6C27.8 31.2 20.2 31.2 19.5 29.6C18.8 28 22.8 20.5 24 20.5Z"
        fill="#FFFFFF"
      />
    </svg>
  )
}

function AdGuardMiniIcon() {
  return (
    <svg width="32" height="32" viewBox="0 0 48 48" fill="none" style={{ flexShrink: 0 }}>
      <rect width="48" height="48" rx="11" fill="#27AE60" />
      <path
        d="M24 10L36 14.5V23.5C36 31.2 30.8 37.6 24 39.5C17.2 37.6 12 31.2 12 23.5V14.5L24 10Z"
        fill="#FFFFFF"
      />
      <path
        d="M19 24.5L22.5 28L29.5 20.5"
        stroke="#27AE60"
        strokeWidth="3.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

export default function SetupPage({ authStatus, onStepChange, onDone }: Props) {
  const { t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const [step, setStep] = useState<1 | 2>(1)
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [saving, setSaving] = useState(false)

  // 步骤 2（套件安装模式：当场绑定本机表单状态）
  const [spkMode, setSpkMode] = useState<'dsm_api' | 'ssh'>('dsm_api')
  const [hostName, setHostName] = useState('')
  const [dsmUrl, setDsmUrl] = useState('http://127.0.0.1:5000')
  const [sshAddress, setSshAddress] = useState('127.0.0.1')
  const [sshPort, setSshPort] = useState<number>(22)
  const [username, setUsername] = useState('')
  const [secret, setSecret] = useState('')
  const [connecting, setConnecting] = useState(false)
  const [whyModalOpen, setWhyModalOpen] = useState(false)

  // 2FA 即时弹窗状态
  const [otpModalOpen, setOtpModalOpen] = useState(false)
  const [otpCode, setOtpCode] = useState('')
  const [deviceTokenNonce, setDeviceTokenNonce] = useState<string | undefined>(undefined)

  // 步骤 2（Docker 安装模式：开启步骤指南弹窗状态）
  const [dockerGuideOpen, setDockerGuideOpen] = useState(false)

  // 点击“跳过”二次确认弹窗状态
  const [skipConfirmOpen, setSkipConfirmOpen] = useState(false)

  const tooShort = pw.length > 0 && [...pw].length < MIN_PW
  const mismatch = pw2.length > 0 && pw !== pw2
  const hasAutoLocal = Boolean(authStatus?.hasLocalHost)

  const onSubmitPw = async () => {
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
        setHostName(t.auth.spkDefaultName)
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

  const submitSpkHost = async (extraOtp?: string) => {
    const finalName = hostName.trim() || t.auth.spkDefaultName
    const payload: HostInput =
      spkMode === 'dsm_api'
        ? {
            name: finalName,
            kind: 'synology',
            transport: 'dsm_api',
            dsmUrl: dsmUrl.trim() || 'http://127.0.0.1:5000',
            dsmUser: username.trim(),
            dsmSecret: secret,
            dsmOtpCode: extraOtp?.trim() || undefined,
            deviceTokenNonce,
          }
        : {
            name: finalName,
            kind: authStatus?.installMode === 'synology' ? 'synology' : 'docker',
            transport: 'ssh',
            address: sshAddress.trim() || '127.0.0.1',
            port: sshPort || 22,
            sshUser: username.trim(),
            sshAuth: 'password',
            sshSecret: secret,
          }

    setConnecting(true)
    try {
      await api.createHost(payload)
      localStorage.removeItem('dkstore_skipped_local')
      setOtpModalOpen(false)
      setOtpCode('')
      message.success(t.hosts.saved)
      onStepChange?.(false)
      await qc.invalidateQueries()
      onDone()
    } catch (e) {
      if (e instanceof ApiError && (e.code === 'need_otp' || e.testResult?.needOtp)) {
        if (e.testResult?.deviceTokenNonce) {
          setDeviceTokenNonce(e.testResult.deviceTokenNonce)
        }
        setOtpCode('')
        setOtpModalOpen(true)
        return
      }
      message.error((e as Error).message)
    } finally {
      setConnecting(false)
    }
  }

  const confirmAndSkip = () => {
    localStorage.setItem('dkstore_skipped_local', '1')
    localStorage.removeItem('dkstore_dismiss_local_remind')
    setSkipConfirmOpen(false)
    onStepChange?.(false)
    onDone()
  }

  const skipConfirmModal = (
    <Modal
      open={skipConfirmOpen}
      title={t.auth.skipConfirmTitle}
      width={384}
      onCancel={() => setSkipConfirmOpen(false)}
      footer={
        <Flex justify="flex-end" gap={8}>
          <Button onClick={() => setSkipConfirmOpen(false)}>{t.auth.skipConfirmBack}</Button>
          <Button type="primary" onClick={confirmAndSkip}>
            {t.auth.skipConfirmOk}
          </Button>
        </Flex>
      }
    >
      <Typography.Text type="secondary" style={{ fontSize: token.fontSize }}>
        {t.auth.skipConfirmDesc}
      </Typography.Text>
    </Modal>
  )

  const renderStepPill = (activeStep: 1 | 2) => (
    <div
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 8,
        padding: '4px 12px',
        borderRadius: 999,
        fontSize: token.fontSizeSM,
        fontWeight: 500,
        background: token.colorFillAlter,
        border: `1px solid ${token.colorBorderSecondary}`,
        marginBottom: 16,
      }}
    >
      <span
        style={{
          color: activeStep === 1 ? token.colorPrimary : token.colorSuccess,
          fontWeight: 600,
        }}
      >
        {activeStep === 2 ? '✓ ' : '1. '}
        {t.auth.step1Short}
      </span>
      <span style={{ color: token.colorTextQuaternary }}>·</span>
      <span
        style={{
          color: activeStep === 2 ? token.colorPrimary : token.colorTextTertiary,
          fontWeight: activeStep === 2 ? 600 : 400,
        }}
      >
        2. {t.auth.step2Short}
      </span>
    </div>
  )

  const renderSplitShell = (rightContent: ReactNode, maxW: number, modals?: ReactNode) => (
    <div className="dk-setup-split">
      {/* 左半屏：深海流光品牌画卷（窄屏及触屏端自动隐藏） */}
      <div className="dk-deep-wing">
        <svg
          width="280"
          height="280"
          viewBox="0 0 64 64"
          fill="none"
          aria-hidden="true"
          style={{
            position: 'absolute',
            right: -48,
            bottom: -48,
            opacity: 0.05,
            pointerEvents: 'none',
          }}
        >
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
        </svg>

        {/* 顶部品牌标识 */}
        <Flex align="center" gap={14} style={{ position: 'relative', zIndex: 1 }}>
          <div
            style={{
              padding: 3,
              borderRadius: 14,
              background: 'rgba(255, 255, 255, 0.14)',
              border: '1px solid rgba(255, 255, 255, 0.2)',
              display: 'inline-flex',
            }}
          >
            <DKStoreLogo size={40} />
          </div>
          <div>
            <div
              style={{
                fontSize: 21,
                fontWeight: 700,
                letterSpacing: '-0.02em',
                lineHeight: 1.2,
                color: '#ffffff',
              }}
            >
              DKStore
            </div>
            <div style={{ fontSize: 12, color: 'rgba(219, 234, 254, 0.76)', marginTop: 2 }}>
              {t.auth.tagline}
            </div>
          </div>
        </Flex>

        {/* 中央多端应用更新状态条（零干扰纯展示） */}
        <div
          style={{
            position: 'relative',
            zIndex: 1,
            marginBlock: 28,
            maxWidth: 440,
            display: 'flex',
            flexDirection: 'column',
            gap: 10,
          }}
        >
          {/* 条目 1：Home Assistant */}
          <div
            style={{
              borderRadius: 12,
              padding: '12px 16px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              background: 'rgba(255, 255, 255, 0.11)',
              border: '1px solid rgba(255, 255, 255, 0.18)',
              backdropFilter: 'blur(8px)',
              boxShadow: '0 12px 28px -8px rgba(0, 0, 0, 0.35)',
            }}
          >
            <Flex align="center" gap={12} style={{ minWidth: 0 }}>
              <HomeAssistantMiniIcon />
              <div style={{ minWidth: 0 }}>
                <div
                  style={{
                    fontSize: 14,
                    fontWeight: 600,
                    color: '#ffffff',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  Home Assistant
                </div>
                <div style={{ fontSize: 12, color: 'rgba(191, 219, 254, 0.72)' }}>
                  {t.auth.showcaseHostSynology}
                </div>
              </div>
            </Flex>
            <Flex
              align="center"
              gap={6}
              style={{ fontFamily: token.fontFamilyCode, fontSize: 12, flexShrink: 0 }}
            >
              <span style={{ color: 'rgba(191, 219, 254, 0.62)' }}>2026.10.3</span>
              <span style={{ color: '#67e8f9' }}>→</span>
              <span
                style={{
                  padding: '2px 8px',
                  borderRadius: 6,
                  fontWeight: 600,
                  color: '#a5f3fc',
                  background: 'rgba(56, 189, 248, 0.2)',
                  border: '1px solid rgba(103, 232, 249, 0.35)',
                }}
              >
                2026.11.1
              </span>
            </Flex>
          </div>

          {/* 条目 2：Jellyfin */}
          <div
            style={{
              borderRadius: 12,
              padding: '10px 16px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              background: 'rgba(255, 255, 255, 0.06)',
              border: '1px solid rgba(255, 255, 255, 0.1)',
              opacity: 0.88,
            }}
          >
            <Flex align="center" gap={12} style={{ minWidth: 0 }}>
              <JellyfinMiniIcon />
              <div style={{ minWidth: 0 }}>
                <div style={{ fontSize: 13, fontWeight: 600, color: '#ffffff' }}>Jellyfin</div>
                <div style={{ fontSize: 12, color: 'rgba(191, 219, 254, 0.65)' }}>
                  {t.auth.showcaseHostFnos}
                </div>
              </div>
            </Flex>
            <Flex
              align="center"
              gap={6}
              style={{ fontFamily: token.fontFamilyCode, fontSize: 12, flexShrink: 0 }}
            >
              <span style={{ color: 'rgba(191, 219, 254, 0.56)' }}>10.9.11</span>
              <span style={{ color: '#67e8f9' }}>→</span>
              <span
                style={{
                  padding: '2px 8px',
                  borderRadius: 6,
                  fontWeight: 600,
                  color: '#a5f3fc',
                  background: 'rgba(56, 189, 248, 0.16)',
                  border: '1px solid rgba(103, 232, 249, 0.28)',
                }}
              >
                10.10.1
              </span>
            </Flex>
          </div>

          {/* 条目 3：AdGuard Home */}
          <div
            style={{
              borderRadius: 12,
              padding: '10px 16px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              background: 'rgba(255, 255, 255, 0.04)',
              border: '1px solid rgba(255, 255, 255, 0.07)',
              opacity: 0.68,
            }}
          >
            <Flex align="center" gap={12} style={{ minWidth: 0 }}>
              <AdGuardMiniIcon />
              <div style={{ minWidth: 0 }}>
                <div style={{ fontSize: 13, fontWeight: 600, color: '#ffffff' }}>AdGuard Home</div>
                <div style={{ fontSize: 12, color: 'rgba(191, 219, 254, 0.6)' }}>OpenWrt</div>
              </div>
            </Flex>
            <span style={{ fontSize: 12, fontWeight: 500, color: '#6ee7b7', flexShrink: 0 }}>
              ✓ {t.auth.showcaseUpToDate}
            </span>
          </div>
        </div>

        {/* 底部一句话核心定位 */}
        <div
          style={{
            position: 'relative',
            zIndex: 1,
            fontSize: 13,
            lineHeight: 1.65,
            color: 'rgba(219, 234, 254, 0.78)',
          }}
        >
          {t.auth.showcaseSummary}
        </div>
      </div>

      {/* 右半屏：纯净向导工作台 */}
      <div
        className="dk-setup-right"
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'space-between',
          padding: '24px 28px',
          minHeight: '100vh',
        }}
      >
        <Flex justify="flex-end">
          <AuthToolbar />
        </Flex>

        <Flex align="center" justify="center" style={{ flex: 1, paddingBlock: 24 }}>
          <div style={{ width: '100%', maxWidth: maxW }}>
            {/* 窄屏/触屏端左栏隐藏时展示紧凑品牌头 */}
            <div
              className="dk-setup-mobile-brand"
              style={{
                alignItems: 'center',
                gap: 12,
                marginBottom: 24,
              }}
            >
              <DKStoreLogo size={40} />
              <div>
                <div
                  style={{
                    fontSize: 18,
                    fontWeight: 700,
                    letterSpacing: '-0.02em',
                    color: token.colorText,
                    lineHeight: 1.25,
                  }}
                >
                  DKStore
                </div>
                <div style={{ fontSize: 12, color: token.colorTextSecondary }}>
                  {t.auth.tagline}
                </div>
              </div>
            </div>

            {rightContent}
          </div>
        </Flex>

        <div style={{ height: 16 }} />
      </div>

      {modals}
    </div>
  )

  // 步骤 2
  if (step === 2) {
    const isDockerMode = authStatus?.installMode === 'docker'

    // 分支 B：Docker 安装模式（未挂载本机容器访问权限）
    if (isDockerMode) {
      return renderSplitShell(
        <div>
          {renderStepPill(2)}
          <div style={{ marginBottom: 20 }}>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t.auth.dockerTitle}
            </Typography.Title>
            <Typography.Text
              type="secondary"
              style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
            >
              {t.auth.dockerSubtitle}
            </Typography.Text>
          </div>

          <Row gutter={[12, 12]} align="stretch">
            <Col xs={24} sm={12} style={{ display: 'flex' }}>
              <Card
                size="small"
                style={{
                  width: '100%',
                  background: token.colorFillAlter,
                  display: 'flex',
                  flexDirection: 'column',
                }}
                styles={{
                  body: {
                    flex: 1,
                    display: 'flex',
                    flexDirection: 'column',
                    justifyContent: 'space-between',
                    padding: 16,
                  },
                }}
              >
                <div>
                  <div
                    style={{
                      width: 32,
                      height: 32,
                      borderRadius: token.borderRadius,
                      background: token.colorPrimaryBg,
                      color: token.colorPrimary,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      fontSize: 16,
                      marginBottom: 10,
                    }}
                  >
                    <DesktopOutlined />
                  </div>
                  <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>
                    {t.auth.dockerCardLocalTitle}
                  </Typography.Text>
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: token.fontSizeSM, lineHeight: 1.6 }}
                  >
                    {t.auth.dockerCardLocalDesc}
                  </Typography.Text>
                </div>
                <div
                  style={{
                    marginTop: 12,
                    paddingTop: 10,
                    borderTop: `1px solid ${token.colorBorderSecondary}`,
                  }}
                >
                  <Button
                    type="link"
                    size="small"
                    style={{ padding: 0, height: 'auto' }}
                    onClick={() => setDockerGuideOpen(true)}
                  >
                    {t.auth.dockerCardLocalLink} <ArrowRightOutlined />
                  </Button>
                </div>
              </Card>
            </Col>

            <Col xs={24} sm={12} style={{ display: 'flex' }}>
              <Card
                size="small"
                style={{
                  width: '100%',
                  background: token.colorFillAlter,
                  display: 'flex',
                  flexDirection: 'column',
                }}
                styles={{
                  body: {
                    flex: 1,
                    display: 'flex',
                    flexDirection: 'column',
                    justifyContent: 'space-between',
                    padding: 16,
                  },
                }}
              >
                <div>
                  <div
                    style={{
                      width: 32,
                      height: 32,
                      borderRadius: token.borderRadius,
                      background: token.colorSuccessBg,
                      color: token.colorSuccess,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      fontSize: 16,
                      marginBottom: 10,
                    }}
                  >
                    <GlobalOutlined />
                  </div>
                  <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>
                    {t.auth.dockerCardRemoteTitle}
                  </Typography.Text>
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: token.fontSizeSM, lineHeight: 1.6 }}
                  >
                    {t.auth.dockerCardRemoteDesc}
                  </Typography.Text>
                </div>
                <div
                  style={{
                    marginTop: 12,
                    paddingTop: 10,
                    borderTop: `1px solid ${token.colorBorderSecondary}`,
                  }}
                >
                  <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                    {t.auth.dockerCardRemoteFooter}
                  </Typography.Text>
                </div>
              </Card>
            </Col>
          </Row>

          <Divider style={{ margin: '20px 0 16px' }} />

          <Flex justify="flex-end">
            <Button type="primary" onClick={() => setSkipConfirmOpen(true)}>
              {t.auth.dockerSkipBtn}
            </Button>
          </Flex>
        </div>,
        520,
        <>
          {skipConfirmModal}
          <Modal
            open={dockerGuideOpen}
            title={t.auth.dockerGuideTitle}
            onCancel={() => setDockerGuideOpen(false)}
            footer={
              <Button type="primary" onClick={() => setDockerGuideOpen(false)}>
                {t.auth.dockerGuideOk}
              </Button>
            }
          >
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              <Card size="small" style={{ background: token.colorFillAlter }}>
                <Flex justify="space-between" align="center" style={{ marginBottom: 4 }}>
                  <Typography.Text strong>{t.auth.dockerGuideMethod1Title}</Typography.Text>
                  <Button
                    size="small"
                    type="text"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      navigator.clipboard.writeText(COMPOSE_SNIPPET)
                      message.success(t.detail.copied)
                    }}
                  >
                    {t.detail.copyCmd}
                  </Button>
                </Flex>
                <Typography.Paragraph
                  type="secondary"
                  style={{ fontSize: token.fontSizeSM, marginBottom: 8 }}
                >
                  {t.auth.dockerGuideMethod1Desc}
                </Typography.Paragraph>
                <pre
                  style={{
                    margin: 0,
                    padding: '8px 12px',
                    borderRadius: token.borderRadiusSM,
                    background: token.colorBgContainer,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    fontFamily: token.fontFamilyCode,
                    fontSize: token.fontSizeSM,
                    overflowX: 'auto',
                  }}
                >
                  {COMPOSE_SNIPPET}
                </pre>
              </Card>

              <Card size="small" style={{ background: token.colorFillAlter }}>
                <Flex justify="space-between" align="center" style={{ marginBottom: 4 }}>
                  <Typography.Text strong>{t.auth.dockerGuideMethod2Title}</Typography.Text>
                  <Button
                    size="small"
                    type="text"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      navigator.clipboard.writeText(DOCKER_RUN_SNIPPET)
                      message.success(t.detail.copied)
                    }}
                  >
                    {t.detail.copyCmd}
                  </Button>
                </Flex>
                <Typography.Paragraph
                  type="secondary"
                  style={{ fontSize: token.fontSizeSM, marginBottom: 8 }}
                >
                  {t.auth.dockerGuideMethod2Desc}
                </Typography.Paragraph>
                <pre
                  style={{
                    margin: 0,
                    padding: '8px 12px',
                    borderRadius: token.borderRadiusSM,
                    background: token.colorBgContainer,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    fontFamily: token.fontFamilyCode,
                    fontSize: token.fontSizeSM,
                    overflowX: 'auto',
                  }}
                >
                  {DOCKER_RUN_SNIPPET}
                </pre>
              </Card>
            </Space>
          </Modal>
        </>,
      )
    }

    // 分支 A：套件安装模式（当场绑定本机表单）
    return renderSplitShell(
      <div>
        {renderStepPill(2)}
        <div style={{ marginBottom: 16 }}>
          <Typography.Title level={4} style={{ margin: 0 }}>
            {t.auth.spkTitle}
          </Typography.Title>
          <Typography.Text
            type="secondary"
            style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
          >
            {t.auth.spkSubtitle}
          </Typography.Text>
        </div>

        <div style={{ marginBottom: 16 }}>
          <Segmented
            block
            value={spkMode}
            onChange={(v) => setSpkMode(v as 'dsm_api' | 'ssh')}
            options={[
              { label: t.auth.spkModeWeb, value: 'dsm_api' },
              { label: t.auth.spkModeSsh, value: 'ssh' },
            ]}
          />
          <Card
            size="small"
            style={{
              marginTop: 10,
              background: token.colorFillAlter,
            }}
          >
            <Space direction="vertical" size={4} style={{ width: '100%' }}>
              <div>
                <Typography.Text
                  strong
                  style={{ color: token.colorSuccess, fontSize: token.fontSizeSM }}
                >
                  {t.hosts.connProsLabel}
                </Typography.Text>
                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  {spkMode === 'dsm_api' ? t.hosts.connDsmPros : t.hosts.connSshPros}
                </Typography.Text>
              </div>
              <div>
                <Typography.Text
                  strong
                  style={{ color: token.colorWarning, fontSize: token.fontSizeSM }}
                >
                  {t.hosts.connLimitLabel}
                </Typography.Text>
                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  {spkMode === 'dsm_api' ? t.hosts.connDsmLimit : t.hosts.connSshLimit}
                </Typography.Text>
              </div>
            </Space>
          </Card>
        </div>

        <Form layout="vertical">
          {spkMode === 'dsm_api' ? (
            <Row gutter={12}>
              <Col xs={24} sm={10}>
                <Form.Item label={t.hosts.nameLabel} style={{ marginBottom: 12 }}>
                  <Input
                    value={hostName}
                    onChange={(e) => setHostName(e.target.value)}
                    placeholder={t.auth.spkDefaultName}
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={14}>
                <Form.Item label={t.auth.spkWebAddrLabel} style={{ marginBottom: 12 }}>
                  <Input
                    value={dsmUrl}
                    onChange={(e) => setDsmUrl(e.target.value)}
                    style={{ fontFamily: token.fontFamilyCode }}
                    placeholder="http://127.0.0.1:5000"
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item label={t.hosts.dsmUserLabel} style={{ marginBottom: 0 }}>
                  <Input
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder={t.auth.spkUserPlaceholder}
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item label={t.hosts.dsmSecretLabel} style={{ marginBottom: 0 }}>
                  <Input.Password
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                    onPressEnter={() => submitSpkHost()}
                  />
                </Form.Item>
              </Col>
            </Row>
          ) : (
            <Row gutter={12}>
              <Col xs={24} sm={9}>
                <Form.Item label={t.hosts.nameLabel} style={{ marginBottom: 12 }}>
                  <Input
                    value={hostName}
                    onChange={(e) => setHostName(e.target.value)}
                    placeholder={t.auth.spkDefaultName}
                  />
                </Form.Item>
              </Col>
              <Col xs={16} sm={10}>
                <Form.Item label={t.hosts.addressLabel} style={{ marginBottom: 12 }}>
                  <Input
                    value={sshAddress}
                    onChange={(e) => setSshAddress(e.target.value)}
                    style={{ fontFamily: token.fontFamilyCode }}
                    placeholder="127.0.0.1"
                  />
                </Form.Item>
              </Col>
              <Col xs={8} sm={5}>
                <Form.Item label={t.hosts.portLabel} style={{ marginBottom: 12 }}>
                  <InputNumber
                    min={1}
                    max={65535}
                    value={sshPort}
                    onChange={(v) => setSshPort(v || 22)}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item label={t.hosts.userLabel} style={{ marginBottom: 0 }}>
                  <Input
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder="admin / root"
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item label={t.hosts.passwordLabel} style={{ marginBottom: 0 }}>
                  <Input.Password
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                    onPressEnter={() => submitSpkHost()}
                  />
                </Form.Item>
              </Col>
            </Row>
          )}
        </Form>

        <Divider style={{ margin: '18px 0 14px' }} />

        <Flex justify="space-between" align="center" wrap gap={8}>
          <Button
            type="link"
            size="small"
            icon={<InfoCircleOutlined />}
            style={{ padding: 0 }}
            onClick={() => setWhyModalOpen(true)}
          >
            {t.auth.spkWhyLink}
          </Button>
          <Space size={8}>
            <Button onClick={() => setSkipConfirmOpen(true)}>{t.auth.spkSkipBtn}</Button>
            <Button type="primary" loading={connecting} onClick={() => submitSpkHost()}>
              {t.auth.spkConnectBtn}
            </Button>
          </Space>
        </Flex>
      </div>,
      490,
      <>
        {skipConfirmModal}

        <Modal
          open={whyModalOpen}
          title={t.auth.spkWhyTitle}
          width={420}
          onCancel={() => setWhyModalOpen(false)}
          footer={
            <Button type="primary" onClick={() => setWhyModalOpen(false)}>
              {t.auth.spkWhyOk}
            </Button>
          }
        >
          <Space direction="vertical" size={10}>
            <Typography.Text type="secondary">{t.auth.spkWhyP1}</Typography.Text>
            <Typography.Text type="secondary">{t.auth.spkWhyP2}</Typography.Text>
          </Space>
        </Modal>

        <Modal
          open={otpModalOpen}
          title={t.hosts.otpModalTitle}
          width={380}
          onCancel={() => setOtpModalOpen(false)}
          footer={
            <Flex justify="flex-end" gap={8}>
              <Button onClick={() => setOtpModalOpen(false)}>{t.ignoreModal.cancel}</Button>
              <Button
                type="primary"
                loading={connecting}
                onClick={() => submitSpkHost(otpCode)}
              >
                {t.hosts.otpVerifySaveBtn}
              </Button>
            </Flex>
          }
        >
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
              {t.hosts.otpModalDesc}
            </Typography.Text>
            <Input
              autoFocus
              size="large"
              maxLength={6}
              placeholder="000000"
              value={otpCode}
              onChange={(e) => setOtpCode(e.target.value)}
              onPressEnter={() => submitSpkHost(otpCode)}
              style={{
                textAlign: 'center',
                letterSpacing: '0.4em',
                fontFamily: token.fontFamilyCode,
              }}
            />
          </Space>
        </Modal>
      </>,
    )
  }

  // 步骤 1：设置管理员密码
  return renderSplitShell(
    <div>
      {!hasAutoLocal && renderStepPill(1)}
      <div style={{ marginBottom: 20 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {t.auth.setupTitle}
        </Typography.Title>
        <Typography.Text
          type="secondary"
          style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
        >
          {t.auth.setupDesc}
        </Typography.Text>
      </div>

      {hasAutoLocal && (
        <div
          style={{
            marginBottom: 16,
            padding: '8px 12px',
            borderRadius: token.borderRadius,
            background: token.colorSuccessBg,
            border: `1px solid ${token.colorSuccessBorder}`,
            color: token.colorSuccess,
            fontSize: token.fontSizeSM,
            display: 'flex',
            alignItems: 'center',
            gap: 8,
          }}
        >
          <CheckOutlined />
          <span>{t.auth.localAutoReady}</span>
        </div>
      )}

      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Input.Password
          size="large"
          prefix={<LockOutlined style={{ color: token.colorTextQuaternary }} />}
          placeholder={t.auth.newPw}
          value={pw}
          status={tooShort ? 'error' : undefined}
          onChange={(e) => setPw(e.target.value)}
          autoComplete="new-password"
          style={{ borderRadius: 10 }}
        />
        <Input.Password
          size="large"
          prefix={<LockOutlined style={{ color: token.colorTextQuaternary }} />}
          placeholder={t.auth.confirmPw}
          value={pw2}
          status={mismatch ? 'error' : undefined}
          onChange={(e) => setPw2(e.target.value)}
          onPressEnter={onSubmitPw}
          autoComplete="new-password"
          style={{ borderRadius: 10 }}
        />
        {(tooShort || mismatch) && (
          <Typography.Text type="danger" style={{ fontSize: token.fontSizeSM }}>
            {tooShort ? t.settings.pwTooShort : t.settings.pwMismatch}
          </Typography.Text>
        )}
        <Button
          type="primary"
          size="large"
          block
          loading={saving}
          onClick={onSubmitPw}
          style={{
            height: 42,
            borderRadius: 10,
            marginTop: 4,
            fontWeight: 500,
          }}
        >
          {hasAutoLocal ? t.auth.setupFinishBtn : t.auth.setupBtn}
        </Button>
      </Space>
    </div>,
    370,
  )
}
