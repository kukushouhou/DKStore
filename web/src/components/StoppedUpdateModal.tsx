import { Button, Flex, Modal, Space, Typography } from 'antd'
import { useI18n } from '../i18n'

interface StoppedUpdateModalProps {
  open: boolean
  title: string
  description: string
  partial?: boolean
  loadingChoice?: 'keep' | 'start' | null
  onConfirm: (startAfter: boolean) => void
  onCancel: () => void
}

export default function StoppedUpdateModal({
  open,
  title,
  description,
  partial = false,
  loadingChoice = null,
  onConfirm,
  onCancel,
}: StoppedUpdateModalProps) {
  const { t } = useI18n()
  const busy = loadingChoice !== null

  return (
    <Modal
      open={open}
      title={title}
      onCancel={busy ? undefined : onCancel}
      closable={!busy}
      maskClosable={!busy}
      width={440}
      destroyOnHidden
      footer={
        <Flex align="center" justify="space-between" gap={8}>
          <Button type="text" disabled={busy} onClick={onCancel}>
            {t.lifecycle.cancel}
          </Button>
          <Space size={8}>
            <Button
              loading={loadingChoice === 'keep'}
              disabled={busy && loadingChoice !== 'keep'}
              onClick={() => onConfirm(false)}
            >
              {partial ? t.lifecycle.btnKeepState : t.lifecycle.btnUpdateNoStart}
            </Button>
            <Button
              loading={loadingChoice === 'start'}
              disabled={busy && loadingChoice !== 'start'}
              onClick={() => onConfirm(true)}
            >
              {t.lifecycle.btnUpdateAndStart}
            </Button>
          </Space>
        </Flex>
      }
    >
      <Typography.Text type="secondary" style={{ display: 'block', lineHeight: 1.6 }}>
        {description}
      </Typography.Text>
    </Modal>
  )
}
