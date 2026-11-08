import { Empty, Typography } from 'antd'

export default function HostsPage() {
  return (
    <>
      <Typography.Title level={3}>设备</Typography.Title>
      <Empty description="还没有添加设备" />
    </>
  )
}
