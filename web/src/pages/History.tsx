import { Card, Empty, Space, Tag, Timeline, Typography } from 'antd'
import { ArrowRightOutlined } from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { useI18n } from '../i18n'

export default function HistoryPage() {
  const { locale, t } = useI18n()
  const runsQuery = useQuery({ queryKey: ['checkRuns', locale], queryFn: api.checkRuns })
  const items = runsQuery.data?.items ?? []

  const triggerLabel = (tr: string) => {
    if (tr === 'manual') return t.history.triggerManual
    if (tr === 'schedule') return t.history.triggerSchedule
    return t.history.triggerStartup
  }

  const changeTag = (chg: string) => {
    if (chg === 'appeared') return <Tag color="processing">{t.history.changeAppeared}</Tag>
    if (chg === 'changed') return <Tag color="warning">{t.history.changeChanged}</Tag>
    return <Tag color="success">{t.history.changeResolved}</Tag>
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ margin: 0 }}>
        {t.history.title}
      </Typography.Title>

      {items.length === 0 ? (
        <Empty description={t.history.empty} />
      ) : (
        <Timeline
          items={items.map((r) => ({
            key: r.id,
            children: (
              <Card size="small" style={{ marginBottom: 8 }}>
                <Space direction="vertical" size={6} style={{ width: '100%' }}>
                  <Space wrap>
                    <Typography.Text strong>
                      {new Date(r.startedAt * 1000).toLocaleString(locale)}
                    </Typography.Text>
                    <Tag>{triggerLabel(r.trigger)}</Tag>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t.history.summary
                        .replace('{total}', String(r.summary?.total ?? 0))
                        .replace('{normal}', String(r.summary?.normal ?? 0))
                        .replace('{caution}', String(r.summary?.caution ?? 0))
                        .replace('{failed}', String(r.summary?.failed ?? 0))}
                    </Typography.Text>
                  </Space>
                  {r.changes && r.changes.length > 0 ? (
                    <Space direction="vertical" size={4}>
                      {r.changes.map((c, idx) => (
                        <Space key={idx} size={6} wrap>
                          {changeTag(c.change)}
                          <Typography.Text strong>
                            {c.appName}
                            {c.serviceName && c.serviceName !== 'main' && c.serviceName !== c.appName
                              ? `/${c.serviceName}`
                              : ''}
                          </Typography.Text>
                          <Typography.Text type="secondary">({c.hostName})</Typography.Text>
                          {c.targetVersion && (
                            <Space size={4}>
                              <Typography.Text code>
                                {c.currentVersion || t.home.unknownCurrent}
                              </Typography.Text>
                              <ArrowRightOutlined style={{ fontSize: 11 }} />
                              <Typography.Text code>{c.targetVersion}</Typography.Text>
                            </Space>
                          )}
                        </Space>
                      ))}
                    </Space>
                  ) : (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t.history.noChanges}
                    </Typography.Text>
                  )}
                </Space>
              </Card>
            ),
          }))}
        />
      )}
    </Space>
  )
}
