import { useState } from 'react'
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Col,
  Empty,
  Flex,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Radio,
  Row,
  Space,
  Steps,
  Tag,
  Timeline,
  Typography,
  message,
} from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloudServerOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  PlusOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type HostInput, type HostItem, type HostTestResult } from '../api/client'
import { useI18n } from '../i18n'

export default function HostsPage() {
  const { locale, t } = useI18n()
  const qc = useQueryClient()
  const hostsQuery = useQuery({ queryKey: ['hosts', locale], queryFn: api.listHosts })

  const [wizardOpen, setWizardOpen] = useState(false)
  const [editing, setEditing] = useState<HostItem | null>(null)
  const [step, setStep] = useState(0)
  const [form] = Form.useForm<HostInput>()
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<HostTestResult | null>(null)
  const [trustFingerprint, setTrustFingerprint] = useState(false)
  const [generatedPubKey, setGeneratedPubKey] = useState('')
  const [saving, setSaving] = useState(false)

  const openAdd = () => {
    setEditing(null)
    setStep(0)
    setTestResult(null)
    setTrustFingerprint(false)
    setGeneratedPubKey('')
    form.resetFields()
    form.setFieldsValue({
      name: '',
      kind: 'docker',
      transport: 'ssh',
      port: 22,
      sshAuth: 'password',
    })
    setWizardOpen(true)
  }

  const openEdit = (h: HostItem) => {
    setEditing(h)
    setStep(0)
    setTestResult(null)
    setTrustFingerprint(Boolean(h.sshHostKey))
    setGeneratedPubKey('')
    form.resetFields()
    form.setFieldsValue({
      name: h.name,
      kind: h.kind,
      transport: h.transport,
      address: h.address,
      port: h.port || 22,
      sshUser: h.sshUser,
      sshAuth: h.sshAuth || 'password',
      sshHostKey: h.sshHostKey,
    })
    setWizardOpen(true)
  }

  const runTest = async () => {
    const vals = form.getFieldsValue(true) as HostInput
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api.testHost(vals)
      setTestResult(res)
      if (res.hostKeyTrusted) {
        setTrustFingerprint(true)
      }
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setTesting(false)
    }
  }

  const handleGenerateKey = async () => {
    try {
      const kp = await api.keygen()
      form.setFieldsValue({ sshAuth: 'key', sshSecret: kp.privateKey })
      setGeneratedPubKey(kp.authorizedKey)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const handleSave = async () => {
    const vals = form.getFieldsValue(true) as HostInput
    if (testResult?.presentedHostKey && trustFingerprint) {
      vals.sshHostKey = testResult.presentedHostKey
    }
    setSaving(true)
    try {
      if (editing) {
        await api.updateHost(editing.id, vals)
      } else {
        await api.createHost(vals)
      }
      message.success(t.hosts.saved)
      setWizardOpen(false)
      qc.invalidateQueries({ queryKey: ['hosts'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const hosts = hostsQuery.data?.items ?? []
  const transportVal = Form.useWatch('transport', form)
  const authVal = Form.useWatch('sshAuth', form)

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center">
        <Typography.Title level={3} style={{ margin: 0 }}>
          {t.hosts.title}
        </Typography.Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
          {t.hosts.addHost}
        </Button>
      </Flex>

      {hosts.length === 0 ? (
        <Empty description={t.hosts.empty} />
      ) : (
        <Row gutter={[16, 16]}>
          {hosts.map((h) => (
            <Col key={h.id} xs={24} md={12}>
              <Card
                title={
                  <Space>
                    <CloudServerOutlined />
                    <span>{h.name}</span>
                  </Space>
                }
                extra={
                  <Badge
                    status={
                      h.status === 'online'
                        ? 'success'
                        : h.status === 'offline'
                          ? 'error'
                          : 'default'
                    }
                    text={
                      h.status === 'online'
                        ? t.hosts.online
                        : h.status === 'offline'
                          ? t.hosts.offline
                          : t.hosts.unknown
                    }
                  />
                }
                actions={[
                  <Button
                    key="scan"
                    type="text"
                    size="small"
                    icon={<SyncOutlined />}
                    onClick={async () => {
                      await api.triggerCheck(h.id, true)
                      message.info(t.home.checking)
                    }}
                  >
                    {t.hosts.scanHost}
                  </Button>,
                  <Button
                    key="edit"
                    type="text"
                    size="small"
                    icon={<EditOutlined />}
                    onClick={() => openEdit(h)}
                  >
                    {t.hosts.editHost}
                  </Button>,
                  <Popconfirm
                    key="del"
                    title={t.hosts.deleteConfirm}
                    onConfirm={async () => {
                      await api.deleteHost(h.id)
                      message.success(t.hosts.deleted)
                      qc.invalidateQueries({ queryKey: ['hosts'] })
                      qc.invalidateQueries({ queryKey: ['overview'] })
                    }}
                  >
                    <Button type="text" danger size="small" icon={<DeleteOutlined />} />
                  </Popconfirm>,
                ]}
              >
                <Space direction="vertical" size={6}>
                  <Space wrap>
                    <Tag>{h.transport === 'local' ? t.hosts.localSocket : t.hosts.sshRemote}</Tag>
                    {h.address && (
                      <Typography.Text code>
                        {h.sshUser}@{h.address}:{h.port || 22}
                      </Typography.Text>
                    )}
                  </Space>
                  {h.info && (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      Docker {h.info.dockerVersion} · {h.info.platformVersion || h.info.os} ({h.info.os}/{h.info.arch})
                    </Typography.Text>
                  )}
                  {h.sshHostKey && (
                    <Typography.Text type="secondary" style={{ fontSize: 11, fontFamily: 'monospace' }}>
                      {h.sshHostKey}
                    </Typography.Text>
                  )}
                </Space>
              </Card>
            </Col>
          ))}
        </Row>
      )}

      <Modal
        open={wizardOpen}
        title={editing ? t.hosts.editHost : t.hosts.addHost}
        width={640}
        onCancel={() => setWizardOpen(false)}
        footer={
          <Flex justify="space-between">
            <div>
              {step > 0 && (
                <Button onClick={() => setStep(step - 1)}>{t.hosts.prevStep}</Button>
              )}
            </div>
            <Space>
              <Button onClick={() => setWizardOpen(false)}>{t.ignoreModal.cancel}</Button>
              {step < 2 ? (
                <Button
                  type="primary"
                  onClick={() => {
                    if (step === 1) {
                      setStep(2)
                      runTest()
                    } else {
                      setStep(step + 1)
                    }
                  }}
                >
                  {t.hosts.nextStep}
                </Button>
              ) : (
                <Button
                  type="primary"
                  loading={saving}
                  disabled={
                    !testResult?.ok ||
                    (transportVal === 'ssh' && Boolean(testResult?.presentedHostKey) && !trustFingerprint)
                  }
                  onClick={handleSave}
                >
                  {t.hosts.saveBtn}
                </Button>
              )}
            </Space>
          </Flex>
        }
      >
        <Space direction="vertical" size="large" style={{ width: '100%' }}>
          <Steps
            size="small"
            current={step}
            items={[
              { title: t.hosts.stepType },
              { title: t.hosts.stepConn },
              { title: t.hosts.stepTest },
            ]}
          />

          <Form form={form} layout="vertical">
            {step === 0 && (
              <>
                <Form.Item name="name" label={t.hosts.nameLabel} rules={[{ required: true }]}>
                  <Input placeholder={t.hosts.namePlaceholder} />
                </Form.Item>
                <Form.Item name="transport" label={t.hosts.transportLabel}>
                  <Radio.Group style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                    <Radio value="local">{t.hosts.transportLocal}</Radio>
                    <Radio value="ssh">{t.hosts.transportSSH}</Radio>
                  </Radio.Group>
                </Form.Item>
              </>
            )}

            {step === 1 && transportVal === 'local' && (
              <Alert type="info" showIcon title="/var/run/docker.sock" />
            )}

            {step === 1 && transportVal === 'ssh' && (
              <>
                <Row gutter={12}>
                  <Col span={17}>
                    <Form.Item name="address" label={t.hosts.addressLabel} rules={[{ required: true }]}>
                      <Input placeholder="192.168.1.10" />
                    </Form.Item>
                  </Col>
                  <Col span={7}>
                    <Form.Item name="port" label={t.hosts.portLabel}>
                      <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                </Row>
                <Form.Item name="sshUser" label={t.hosts.userLabel} rules={[{ required: true }]}>
                  <Input placeholder="root / ubuntu" />
                </Form.Item>
                <Form.Item name="sshAuth" label={t.hosts.authTypeLabel}>
                  <Radio.Group>
                    <Radio value="password">{t.hosts.authPassword}</Radio>
                    <Radio value="key">{t.hosts.authKey}</Radio>
                  </Radio.Group>
                </Form.Item>
                {authVal === 'key' ? (
                  <>
                    <Space style={{ marginBottom: 8 }}>
                      <Button size="small" icon={<KeyOutlined />} onClick={handleGenerateKey}>
                        {t.hosts.generateKey}
                      </Button>
                    </Space>
                    {generatedPubKey && (
                      <Alert
                        type="info"
                        style={{ marginBottom: 12 }}
                        title={t.hosts.generatedPubTip}
                        description={
                          <Space direction="vertical" style={{ width: '100%' }}>
                            <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                              {generatedPubKey}
                            </Typography.Text>
                            <Button
                              size="small"
                              icon={<CopyOutlined />}
                              onClick={() => {
                                navigator.clipboard.writeText(generatedPubKey)
                                message.success(t.detail.copied)
                              }}
                            >
                              {t.detail.copyCmd}
                            </Button>
                          </Space>
                        }
                      />
                    )}
                    <Form.Item name="sshSecret" label={t.hosts.privateKeyLabel}>
                      <Input.TextArea rows={4} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
                    </Form.Item>
                    <Form.Item name="sshPassphrase" label={t.hosts.passphraseLabel}>
                      <Input.Password />
                    </Form.Item>
                  </>
                ) : (
                  <Form.Item name="sshSecret" label={t.hosts.passwordLabel}>
                    <Input.Password />
                  </Form.Item>
                )}
              </>
            )}

            {step === 2 && (
              <Space direction="vertical" size="middle" style={{ width: '100%' }}>
                <Button icon={<SyncOutlined spin={testing} />} loading={testing} onClick={runTest}>
                  {testing ? t.hosts.testing : t.hosts.testBtn}
                </Button>

                {testResult && (
                  <>
                    <Alert
                      type={testResult.ok ? 'success' : 'error'}
                      showIcon
                      title={testResult.ok ? t.hosts.testPassed : t.hosts.testFailed}
                    />
                    <Timeline
                      items={testResult.steps.map((s) => ({
                        color: s.status === 'ok' ? 'green' : 'red',
                        dot:
                          s.status === 'ok' ? <CheckCircleOutlined /> : <CloseCircleOutlined />,
                        children: (
                          <div>
                            <Typography.Text strong>{s.step}</Typography.Text>
                            <div>
                              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                                {s.message}
                              </Typography.Text>
                            </div>
                          </div>
                        ),
                      }))}
                    />
                    {transportVal === 'ssh' && testResult.presentedHostKey && (
                      <Card size="small" title={t.hosts.fingerprintTitle}>
                        <Space direction="vertical">
                          <Typography.Text code>{testResult.presentedHostKey}</Typography.Text>
                          <Checkbox
                            checked={trustFingerprint}
                            onChange={(e) => setTrustFingerprint(e.target.checked)}
                          >
                            {t.hosts.fingerprintAccepted}
                          </Checkbox>
                        </Space>
                      </Card>
                    )}
                  </>
                )}
              </Space>
            )}
          </Form>
        </Space>
      </Modal>
    </Space>
  )
}
