import { Empty, Typography } from 'antd'

export default function HistoryPage() {
  return (
    <>
      <Typography.Title level={3}>检测历史</Typography.Title>
      <Empty description="暂无检测记录" />
    </>
  )
}
