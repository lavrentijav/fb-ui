import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Card,
  ConfigProvider,
  Input,
  Layout,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { z } from 'zod';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { HttpUtil } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';
import { parseMsg } from '@/utils/zodValidate';

const AuditEntrySchema = z.object({
  id: z.string(),
  at: z.number(),
  actor: z.string().optional(),
  method: z.string(),
  path: z.string(),
  status: z.number().optional(),
  ip: z.string().optional(),
});
type AuditEntry = z.infer<typeof AuditEntrySchema>;

const PAGE_SIZE = 100;

export default function AuditPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [actor, setActor] = useState('');
  const [filter, setFilter] = useState('');

  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { data: rows = [], isFetching, error, refetch } = useQuery({
    queryKey: ['audit', filter],
    queryFn: async () => {
      const msg = await HttpUtil.get(
        '/panel/api/audit/list',
        { limit: PAGE_SIZE, actor: filter || undefined },
        { silent: true },
      );
      if (!msg?.success) throw new Error(msg?.msg || t('pages.audit.loadFailed'));
      return parseMsg(msg, z.array(AuditEntrySchema), 'audit/list').obj ?? [];
    },
  });

  const reload = useCallback(async () => {
    await refetch();
  }, [refetch]);

  const statusColor = (code?: number) => {
    if (!code) return 'default';
    if (code >= 500) return 'red';
    if (code >= 400) return 'orange';
    if (code >= 300) return 'blue';
    return 'green';
  };

  const columns: ColumnsType<AuditEntry> = [
    {
      title: t('pages.audit.time'),
      dataIndex: 'at',
      key: 'at',
      width: 180,
      render: (v: number) => new Date(v * 1000).toLocaleString(),
    },
    {
      title: t('pages.audit.actor'),
      dataIndex: 'actor',
      key: 'actor',
      width: 160,
      render: (v?: string) => v || '—',
    },
    {
      title: t('pages.audit.method'),
      dataIndex: 'method',
      key: 'method',
      width: 90,
      render: (v: string) => <Tag>{v}</Tag>,
    },
    {
      title: t('pages.audit.path'),
      dataIndex: 'path',
      key: 'path',
      render: (v: string) => <Typography.Text code>{v}</Typography.Text>,
    },
    {
      title: t('status'),
      dataIndex: 'status',
      key: 'status',
      width: 90,
      render: (v?: number) => <Tag color={statusColor(v)}>{v ?? '—'}</Tag>,
    },
    {
      title: t('pages.audit.ip'),
      dataIndex: 'ip',
      key: 'ip',
      width: 150,
      render: (v?: string) => <Typography.Text type="secondary">{v || '—'}</Typography.Text>,
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      <Layout className={isDark ? 'audit-page is-dark' : 'audit-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Card
              size="small"
              title={t('pages.audit.title')}
              extra={
                <Space>
                  <Input
                    allowClear
                    value={actor}
                    placeholder={t('pages.audit.filterActor')}
                    onChange={(e) => setActor(e.target.value)}
                    onPressEnter={() => setFilter(actor.trim())}
                    prefix={<SearchOutlined />}
                  />
                  <Button onClick={() => setFilter(actor.trim())}>{t('filter')}</Button>
                  <Button icon={<ReloadOutlined />} onClick={() => void reload()}>
                    {t('refresh')}
                  </Button>
                </Space>
              }
            >
              <Alert
                type={error ? 'error' : 'info'}
                showIcon
                message={error ? (error as Error).message : t('pages.audit.intro')}
                style={{ marginBottom: 12 }}
              />
              <Table<AuditEntry>
                size="small"
                rowKey="id"
                loading={isFetching}
                columns={columns}
                dataSource={rows}
                pagination={{ pageSize: 50, showSizeChanger: false }}
                locale={{ emptyText: t('pages.audit.empty') }}
              />
            </Card>
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}
