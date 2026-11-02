import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Card,
  ConfigProvider,
  Form,
  Input,
  Layout,
  Modal,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  DeleteOutlined,
  KeyOutlined,
  LogoutOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';

interface Token {
  id: string;
  name: string;
  scope: string;
  expires: number;
  at: number;
  live: boolean;
  sessions: number;
}

interface Session {
  id: string;
  name: string;
  scope: string;
  expires: number;
  at: number;
  token: string;
}

const TTL_OPTIONS = [
  { value: 0, label: 'never' },
  { value: 24, label: '24h' },
  { value: 24 * 7, label: '7d' },
  { value: 24 * 30, label: '30d' },
  { value: 24 * 90, label: '90d' },
  { value: 24 * 365, label: '1y' },
];

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch('/api/v1/access' + path, {
    credentials: 'same-origin',
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  });
  const body = (await res.json().catch(() => ({}))) as T & { error?: string };
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
}

export default function AccessPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [modal, modalContextHolder] = Modal.useModal();
  const queryClient = useQueryClient();
  const [form] = Form.useForm<{ name: string; scope: string; ttlHours: number }>();

  const [tab, setTab] = useState('tokens');
  const [createOpen, setCreateOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState<{ name: string; token: string } | null>(null);

  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const tokensQuery = useQuery({
    queryKey: ['access-tokens'],
    queryFn: () => call<Token[]>('/tokens'),
  });
  const sessionsQuery = useQuery({
    queryKey: ['access-sessions'],
    queryFn: () => call<Session[]>('/sessions'),
  });

  const reload = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ['access-tokens'] });
    await queryClient.invalidateQueries({ queryKey: ['access-sessions'] });
  }, [queryClient]);

  const createToken = useCallback(async () => {
    setBusy(true);
    try {
      const values = await form.validateFields();
      const out = await call<{ token: string; view: Token }>('/tokens', {
        method: 'POST',
        body: JSON.stringify(values),
      });
      setCreateOpen(false);
      form.resetFields();
      setShown({ name: out.view.name, token: out.token });
      await reload();
    } catch (e) {
      if (e instanceof Error) messageApi.error(e.message);
    } finally {
      setBusy(false);
    }
  }, [form, messageApi, reload]);

  const removeToken = useCallback(
    (tok: Token) => {
      modal.confirm({
        title: t('pages.access.deleteConfirm', { name: tok.name }),
        content: t('pages.access.deleteDesc', { count: tok.sessions }),
        okButtonProps: { danger: true },
        okText: t('delete'),
        cancelText: t('cancel'),
        onOk: async () => {
          try {
            await call(`/tokens/${encodeURIComponent(tok.id)}`, { method: 'DELETE' });
            await reload();
          } catch (e) {
            messageApi.error((e as Error).message);
          }
        },
      });
    },
    [messageApi, modal, reload, t],
  );

  const signOut = useCallback(
    async (tok: Token) => {
      try {
        const out = await call<{ ended: number }>(
          `/tokens/${encodeURIComponent(tok.id)}/signout`,
          { method: 'POST' },
        );
        messageApi.success(t('pages.access.signedOut', { count: out.ended }));
        await reload();
      } catch (e) {
        messageApi.error((e as Error).message);
      }
    },
    [messageApi, reload, t],
  );

  const dropSession = useCallback(
    (s: Session) => {
      modal.confirm({
        title: t('pages.access.endSessionConfirm', { name: s.name }),
        okButtonProps: { danger: true },
        okText: t('pages.access.endSession'),
        cancelText: t('cancel'),
        onOk: async () => {
          try {
            await call(`/sessions/${encodeURIComponent(s.id)}`, { method: 'DELETE' });
            await reload();
          } catch (e) {
            messageApi.error((e as Error).message);
          }
        },
      });
    },
    [messageApi, modal, reload, t],
  );

  const scopeTag = (scope: string) => (
    <Tag color={scope === 'admin' ? 'red' : 'blue'}>
      {scope === 'admin' ? t('pages.access.admin') : t('pages.access.monitor')}
    </Tag>
  );

  const when = (ms: number) => (ms > 0 ? new Date(ms).toLocaleString() : '—');

  const tokenColumns: ColumnsType<Token> = [
    {
      title: t('pages.access.name'),
      key: 'name',
      render: (_: unknown, r) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{r.name}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {r.id.slice(0, 12)}…
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('pages.access.scope'),
      key: 'scope',
      width: 110,
      render: (_: unknown, r) => scopeTag(r.scope),
    },
    {
      title: t('status'),
      key: 'status',
      width: 100,
      render: (_: unknown, r) => (
        <Tag color={r.live ? 'green' : 'default'}>
          {r.live ? t('enabled') : t('pages.access.expired')}
        </Tag>
      ),
    },
    {
      title: t('pages.access.created'),
      dataIndex: 'at',
      key: 'at',
      width: 170,
      render: (v: number) => when(v),
    },
    {
      title: t('pages.access.expires'),
      dataIndex: 'expires',
      key: 'expires',
      width: 170,
      render: (v: number) => (v === 0 ? t('indefinite') : when(v)),
    },
    {
      title: t('pages.access.sessions'),
      dataIndex: 'sessions',
      key: 'sessions',
      width: 90,
      render: (v: number) => (v > 0 ? v : t('none')),
    },
    {
      title: t('pages.access.actions'),
      key: 'actions',
      width: 110,
      render: (_: unknown, r) => (
        <Space size={4}>
          <Tooltip title={t('pages.access.signOutAll')}>
            <Button
              size="small"
              icon={<LogoutOutlined />}
              disabled={r.sessions === 0}
              onClick={() => void signOut(r)}
            />
          </Tooltip>
          <Tooltip title={t('delete')}>
            <Button size="small" danger icon={<DeleteOutlined />} onClick={() => removeToken(r)} />
          </Tooltip>
        </Space>
      ),
    },
  ];

  const sessionColumns: ColumnsType<Session> = [
    {
      title: t('pages.access.name'),
      dataIndex: 'name',
      key: 'name',
      render: (v: string) => <Typography.Text strong>{v || '—'}</Typography.Text>,
    },
    {
      title: t('pages.access.scope'),
      key: 'scope',
      width: 110,
      render: (_: unknown, r) => scopeTag(r.scope),
    },
    {
      title: t('pages.access.signedInAt'),
      dataIndex: 'at',
      key: 'at',
      width: 170,
      render: (v: number) => when(v),
    },
    {
      title: t('pages.access.expires'),
      dataIndex: 'expires',
      key: 'expires',
      width: 170,
      render: (v: number) => when(v),
    },
    {
      title: t('pages.access.actions'),
      key: 'actions',
      width: 90,
      render: (_: unknown, r) => (
        <Tooltip title={t('pages.access.endSession')}>
          <Button size="small" danger icon={<LogoutOutlined />} onClick={() => dropSession(r)} />
        </Tooltip>
      ),
    },
  ];

  const error = tokensQuery.error || sessionsQuery.error;

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={isDark ? 'access-page is-dark' : 'access-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Card
              size="small"
              title={
                <Space>
                  <KeyOutlined />
                  {t('pages.access.title')}
                </Space>
              }
              extra={
                <Space>
                  <Button
                    icon={<ReloadOutlined />}
                    onClick={() => void reload()}
                  >
                    {t('refresh')}
                  </Button>
                  <Button
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => {
                      form.setFieldsValue({ name: '', scope: 'admin', ttlHours: 0 });
                      setCreateOpen(true);
                    }}
                  >
                    {t('pages.access.createToken')}
                  </Button>
                </Space>
              }
            >
              <Alert
                type={error ? 'error' : 'info'}
                showIcon
                message={error ? (error as Error).message : t('pages.access.intro')}
                style={{ marginBottom: 12 }}
              />
              <Tabs
                activeKey={tab}
                onChange={setTab}
                items={[
                  {
                    key: 'tokens',
                    label: t('pages.access.tokensTab'),
                    children: (
                      <Table<Token>
                        size="small"
                        rowKey="id"
                        loading={tokensQuery.isFetching}
                        columns={tokenColumns}
                        dataSource={tokensQuery.data ?? []}
                        pagination={false}
                        locale={{ emptyText: t('pages.access.noTokens') }}
                      />
                    ),
                  },
                  {
                    key: 'sessions',
                    label: t('pages.access.sessionsTab'),
                    children: (
                      <Table<Session>
                        size="small"
                        rowKey="id"
                        loading={sessionsQuery.isFetching}
                        columns={sessionColumns}
                        dataSource={sessionsQuery.data ?? []}
                        pagination={false}
                        locale={{ emptyText: t('pages.access.noSessions') }}
                      />
                    ),
                  },
                ]}
              />
            </Card>
          </Layout.Content>
        </Layout>
      </Layout>

      <Modal
        open={createOpen}
        title={t('pages.access.createToken')}
        okText={t('create')}
        cancelText={t('cancel')}
        confirmLoading={busy}
        onCancel={() => setCreateOpen(false)}
        onOk={() => void createToken()}
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label={t('pages.access.name')}
            rules={[{ required: true, message: t('pages.access.nameRequired') }]}
          >
            <Input placeholder="ci" />
          </Form.Item>
          <Form.Item name="scope" label={t('pages.access.scope')}>
            <Select
              options={[
                { value: 'admin', label: t('pages.access.admin') },
                { value: 'monitor', label: t('pages.access.monitor') },
              ]}
            />
          </Form.Item>
          <Form.Item name="ttlHours" label={t('pages.access.ttl')} extra={t('pages.access.ttlHint')}>
            <Select
              options={TTL_OPTIONS.map((o) => ({
                value: o.value,
                label: o.value === 0 ? t('indefinite') : o.label,
              }))}
            />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={shown !== null}
        title={t('pages.access.tokenCreated')}
        okText={t('close')}
        onOk={() => setShown(null)}
        onCancel={() => setShown(null)}
        cancelButtonProps={{ style: { display: 'none' } }}
      >
        <Alert
          type="warning"
          showIcon
          message={t('pages.access.tokenOnce')}
          style={{ marginBottom: 12 }}
        />
        <Typography.Text>
          {t('pages.access.name')}: <Typography.Text strong>{shown?.name}</Typography.Text>
        </Typography.Text>
        <div style={{ marginTop: 8 }}>
          <Typography.Text code copyable style={{ wordBreak: 'break-all' }}>
            {shown?.token ?? ''}
          </Typography.Text>
        </div>
      </Modal>
    </ConfigProvider>
  );
}
