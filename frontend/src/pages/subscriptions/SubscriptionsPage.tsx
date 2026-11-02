import { useCallback, useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  ConfigProvider,
  Form,
  Input,
  Layout,
  Modal,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  DeleteOutlined,
  KeyOutlined,
  LinkOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { SizeFormatter } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';

interface Subscription {
  token: string;
  email: string;
  user?: string;
  enable: boolean;
  links: number;
  usedBytes: number;
  totalBytes: number;
  expire: number;
  url: string;
}

interface Secret {
  title: string;
  email: string;
  user: string;
  password?: string;
  url: string;
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: 'same-origin',
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  });
  const body = (await res.json().catch(() => ({}))) as T & { error?: string };
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
}

export default function SubscriptionsPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [modal, modalContextHolder] = Modal.useModal();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const [busy, setBusy] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [secret, setSecret] = useState<Secret | null>(null);
  const [form] = Form.useForm<{ email: string; user?: string; password?: string }>();

  const { data: rows = [], isFetching: loading, error, refetch } = useQuery({
    queryKey: ['subscriptions'],
    queryFn: () => call<Subscription[]>('/sub'),
  });

  const load = useCallback(async () => {
    await refetch();
  }, [refetch]);

  const subUrl = (s: { url: string }) => `${window.location.origin}${s.url}`;

  const copyLink = useCallback(
    async (s: Subscription) => {
      try {
        await navigator.clipboard.writeText(subUrl(s));
        messageApi.success(t('copied'));
      } catch {
        messageApi.error(t('somethingWentWrong'));
      }
    },
    [messageApi, t],
  );

  const setEnabled = useCallback(
    async (s: Subscription, enable: boolean) => {
      try {
        await call(`/sub/${s.token}/enable`, {
          method: 'POST',
          body: JSON.stringify({ enable }),
        });
        await load();
      } catch (e) {
        messageApi.error((e as Error).message);
      }
    },
    [load, messageApi],
  );

  const resetPassword = useCallback(
    async (s: Subscription) => {
      try {
        const out = await call<{ user?: string; password: string }>(
          `/sub/${s.email}/password`,
          {
            method: 'POST',
          },
        );
        setSecret({
          title: t('pages.subscriptions.resetPassword'),
          email: s.email,
          user: out.user ?? s.user ?? s.email,
          password: out.password,
          url: s.url,
        });
      } catch (e) {
        messageApi.error((e as Error).message);
      }
    },
    [messageApi, t],
  );

  const columns: ColumnsType<Subscription> = [
    {
      title: t('pages.subscriptions.email'),
      key: 'email',
      render: (_: unknown, r) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{r.email}</Typography.Text>
          {r.user && (
            <Typography.Text type="secondary">
              {t('pages.subscriptions.user')}: {r.user}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t('status'),
      key: 'status',
      width: 120,
      render: (_: unknown, r) => (
        <Tag color={r.enable ? 'green' : 'default'}>
          {r.enable ? t('enabled') : t('disabled')}
        </Tag>
      ),
    },
    {
      title: t('pages.subscriptions.links'),
      dataIndex: 'links',
      key: 'links',
      width: 90,
      render: (v: number) => (v > 0 ? v : t('none')),
    },
    {
      title: t('usage'),
      key: 'traffic',
      render: (_: unknown, r) => (
        <Typography.Text>
          {SizeFormatter.sizeFormat(r.usedBytes)}
          {' / '}
          {r.totalBytes > 0 ? SizeFormatter.sizeFormat(r.totalBytes) : t('unlimited')}
        </Typography.Text>
      ),
    },
    {
      title: t('pages.subscriptions.expire'),
      key: 'expire',
      width: 130,
      render: (_: unknown, r) =>
        r.expire > 0 ? new Date(r.expire * 1000).toLocaleDateString() : t('indefinite'),
    },
    {
      title: t('pages.subscriptions.actions'),
      key: 'actions',
      width: 210,
      render: (_: unknown, r) => (
        <Space size={4}>
          <Switch
            size="small"
            checked={r.enable}
            onChange={(v) => void setEnabled(r, v)}
            aria-label={r.enable ? t('disabled') : t('enabled')}
          />
          <Tooltip title={t('copy')}>
            <Button size="small" icon={<LinkOutlined />} onClick={() => void copyLink(r)} />
          </Tooltip>
          <Tooltip title={t('pages.subscriptions.resetPassword')}>
            <Button size="small" icon={<KeyOutlined />} onClick={() => void resetPassword(r)} />
          </Tooltip>
          <Tooltip title={t('delete')}>
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              onClick={() =>
                modal.confirm({
                  title: t('pages.subscriptions.deleteConfirm', { email: r.email }),
                  okButtonProps: { danger: true },
                  okText: t('delete'),
                  cancelText: t('cancel'),
                  onOk: async () => {
                    await call(`/sub/${r.token}`, { method: 'DELETE' });
                    await load();
                  },
                })
              }
            />
          </Tooltip>
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={isDark ? 'subscriptions-page is-dark' : 'subscriptions-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Card
              size="small"
              title={t('pages.subscriptions.title')}
              extra={
                <Space>
                  <Button icon={<ReloadOutlined />} onClick={() => void load()}>
                    {t('refresh')}
                  </Button>
                  <Button
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => setCreateOpen(true)}
                  >
                    {t('pages.subscriptions.create')}
                  </Button>
                </Space>
              }
            >
              <Alert
                type={error ? 'error' : 'info'}
                showIcon
                message={error ? (error as Error).message : t('pages.subscriptions.intro')}
                style={{ marginBottom: 12 }}
              />
              <Table<Subscription>
                size="small"
                rowKey="token"
                loading={loading}
                columns={columns}
                dataSource={rows}
                pagination={false}
                locale={{ emptyText: t('pages.subscriptions.empty') }}
              />
            </Card>
          </Layout.Content>
        </Layout>
      </Layout>

      <Modal
        open={createOpen}
        title={t('pages.subscriptions.create')}
        okText={t('create')}
        cancelText={t('cancel')}
        confirmLoading={busy}
        onCancel={() => setCreateOpen(false)}
        onOk={async () => {
          setBusy(true);
          try {
            const values = await form.validateFields();
            const out = await call<{ token: string; email: string; user: string; password?: string; url: string }>(
              `/sub/${encodeURIComponent(values.email)}`,
              { method: 'PUT', body: JSON.stringify({ user: values.user, password: values.password }) },
            );
            setCreateOpen(false);
            form.resetFields();
            setSecret({
              title: t('pages.subscriptions.create'),
              email: out.email,
              user: out.user,
              password: out.password,
              url: out.url,
            });
            await load();
          } catch (e) {
            if (e instanceof Error) messageApi.error(e.message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="email"
            label={t('pages.subscriptions.email')}
            extra={t('pages.subscriptions.emailHint')}
            rules={[{ required: true, message: t('pages.subscriptions.emailHint') }]}
          >
            <Input placeholder="alice" />
          </Form.Item>
          <Form.Item
            name="user"
            label={t('pages.subscriptions.user')}
            extra={t('pages.subscriptions.userHint')}
          >
            <Input placeholder="alice01" />
          </Form.Item>
          <Form.Item
            name="password"
            label={t('pages.subscriptions.password')}
            extra={t('pages.subscriptions.passwordHint')}
          >
            <Input.Password />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={secret !== null}
        title={secret?.title}
        okText={t('close')}
        onOk={() => setSecret(null)}
        onCancel={() => setSecret(null)}
        cancelButtonProps={{ style: { display: 'none' } }}
      >
        <Alert
          type="warning"
          showIcon
          message={t('pages.subscriptions.secretOnce')}
          style={{ marginBottom: 12 }}
        />
        <Space direction="vertical" size={6} style={{ width: '100%' }}>
          <Typography.Text>
            {t('pages.subscriptions.email')}: <Typography.Text strong>{secret?.email}</Typography.Text>
          </Typography.Text>
          <Typography.Text>
            {t('pages.subscriptions.user')}: <Typography.Text strong>{secret?.user}</Typography.Text>
          </Typography.Text>
          {secret?.password && (
            <Typography.Text>
              {t('pages.subscriptions.password')}:{' '}
              <Typography.Text strong copyable>
                {secret.password}
              </Typography.Text>
            </Typography.Text>
          )}
          <Typography.Text>
            {t('pages.subscriptions.subUrl')}:{' '}
            <Typography.Text strong copyable>
              {secret ? subUrl(secret) : ''}
            </Typography.Text>
          </Typography.Text>
        </Space>
      </Modal>
    </ConfigProvider>
  );
}
