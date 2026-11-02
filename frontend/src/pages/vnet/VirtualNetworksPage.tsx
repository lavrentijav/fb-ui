import { useCallback, useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Card, ConfigProvider, Layout, Space, Table, Tag, Typography, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { ReloadOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { HttpUtil } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';

interface Member {
  user: string;
  name?: string;
  ip?: string;
  role: string;
}

interface Network {
  id: string;
  title: string;
  owner: string;
  members: Member[];
}

// A native master answers the old panel surface through the subscriptions
// module, so the panel's session cookie authenticates this list.
async function fetchNetworks(): Promise<Network[]> {
  const msg = (await HttpUtil.get('/panel/api/vnets/list', undefined, { silent: true })) as {
    success?: boolean;
    msg?: string;
    obj?: Network[];
  };
  if (!msg?.success) throw new Error(msg?.msg || 'HTTP /panel/api/vnets/list');
  return msg.obj ?? [];
}

export default function VirtualNetworksPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { data: rows = [], isFetching: loading, error, refetch } = useQuery({
    queryKey: ['vnet-networks'],
    queryFn: fetchNetworks,
  });

  const load = useCallback(async () => {
    await refetch();
  }, [refetch]);

  const columns: ColumnsType<Network> = [
    {
      title: t('pages.vnet.network'),
      key: 'network',
      render: (_: unknown, r) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{r.title || r.id}</Typography.Text>
          <Typography.Text type="secondary">{r.id}</Typography.Text>
        </Space>
      ),
    },
    {
      title: t('pages.vnet.owner'),
      dataIndex: 'owner',
      key: 'owner',
      width: 160,
    },
    {
      title: t('pages.vnet.members'),
      key: 'members',
      render: (_: unknown, r) => (
        <Space size={[4, 4]} wrap>
          {r.members.map((m) => (
            <Tag key={m.user} color={m.role === 'owner' ? 'blue' : m.role === 'admin' ? 'gold' : 'default'}>
              {m.name || m.user}
              {m.ip ? ` · ${m.ip}` : ''}
              {m.user === r.owner ? ` · ${t('pages.vnet.owner')}` : ''}
            </Tag>
          ))}
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      <Layout className={isDark ? 'vnet-page is-dark' : 'vnet-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Card
              size="small"
              title={t('pages.vnet.title')}
              extra={
                <Button icon={<ReloadOutlined />} onClick={() => void load()}>
                  {t('refresh')}
                </Button>
              }
            >
              <Alert
                type={error ? 'error' : 'info'}
                showIcon
                message={error ? (error as Error).message : t('pages.vnet.intro')}
                style={{ marginBottom: 12 }}
              />
              <Table<Network>
                size="small"
                rowKey="id"
                loading={loading}
                columns={columns}
                dataSource={rows}
                pagination={false}
                locale={{ emptyText: t('pages.vnet.empty') }}
              />
            </Card>
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}
