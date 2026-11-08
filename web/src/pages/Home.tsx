import { Button, Collapse, Empty, Flex, Space, Typography } from 'antd'
import { SyncOutlined } from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'

// 首页骨架：三组（可更新 / 需谨慎更新 / 已忽略），只默认展开“可更新”（实施文档 §10.2）。
export default function HomePage() {
  const health = useQuery({ queryKey: ['health'], queryFn: api.health })

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center" wrap gap={12}>
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            应用更新
          </Typography.Title>
          <Typography.Text type="secondary">
            {health.isSuccess
              ? `服务正常 · v${health.data.version}`
              : health.isError
                ? '无法连接后端服务'
                : '正在连接…'}
          </Typography.Text>
        </div>
        <Button type="primary" icon={<SyncOutlined />} disabled>
          检查更新
        </Button>
      </Flex>

      <Collapse
        ghost
        defaultActiveKey={['normal']}
        items={[
          { key: 'normal', label: '可更新', children: <Empty description="暂无可更新的应用" /> },
          { key: 'caution', label: '需谨慎更新', children: <Empty description="暂无" /> },
          { key: 'ignored', label: '已忽略', children: <Empty description="暂无" /> },
        ]}
      />
    </Space>
  )
}
