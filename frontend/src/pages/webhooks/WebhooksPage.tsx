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
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
  SendOutlined,
} from '@ant-design/icons';
import { z } from 'zod';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { HttpUtil } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';
import { parseMsg } from '@/utils/zodValidate';

const WebhookSchema = z.object({
  id: z.string(),
  name: z.string(),
  url: z.string(),
  events: z
    .array(z.string())
    .nullish()
    .transform((v) => v ?? []),
  enable: z.boolean(),
  hasSecret: z.boolean().optional(),
  createdAt: z.number().optional(),
  lastAt: z.number().optional(),
  lastOk: z.boolean().optional(),
  lastError: z.string().optional(),
});
type Webhook = z.infer<typeof WebhookSchema>;

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } } as const;

const EVENTS = ['client.expired', 'client.exhausted', 'node.down', 'node.up', 'cert.expiring'];

interface FormValues {
  name: string;
  url: string;
  events?: string[];
  enable?: boolean;
  secret?: string;
}

export default function WebhooksPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [modal, modalContextHolder] = Modal.useModal();
  const queryClient = useQueryClient();
  const [form] = Form.useForm<FormValues>();

  const [editing, setEditing] = useState<Webhook | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const {
    data: rows = [],
    isFetching: loading,
    error,
    refetch,
  } = useQuery({
    queryKey: ['webhooks'],
    queryFn: async () => {
      const msg = await HttpUtil.get('/panel/api/webhooks/list', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || t('pages.webhooks.loadFailed'));
      return parseMsg(msg, z.array(WebhookSchema), 'webhooks/list').obj ?? [];
    },
  });

  const load = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ['webhooks'] });
    await refetch();
  }, [queryClient, refetch]);

  const openCreate = useCallback(() => {
    setEditing(null);
    form.setFieldsValue({ name: '', url: '', events: [], enable: true, secret: '' });
    setDialogOpen(true);
  }, [form]);

  const openEdit = useCallback(
    (w: Webhook) => {
      setEditing(w);
      form.setFieldsValue({
        name: w.name,
        url: w.url,
        events: w.events,
        enable: w.enable,
        secret: '',
      });
      setDialogOpen(true);
    },
    [form],
  );

  const save = useCallback(async () => {
    const values = await form.validateFields();
    setBusy(true);
    try {
      const body = { ...values };
      const msg = editing
        ? await HttpUtil.post(
            `/panel/api/webhooks/update/${encodeURIComponent(editing.id)}`,
            body,
            JSON_HEADERS,
          )
        : await HttpUtil.post('/panel/api/webhooks/add', body, JSON_HEADERS);
      if (!msg?.success) throw new Error(msg?.msg || t('somethingWentWrong'));
      setDialogOpen(false);
      await load();
    } catch (e) {
      messageApi.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [editing, form, load, messageApi, t]);

  const remove = useCallback(
    (w: Webhook) => {
      modal.confirm({
        title: t('pages.webhooks.deleteConfirm', { name: w.name }),
        okButtonProps: { danger: true },
        okText: t('delete'),
        cancelText: t('cancel'),
        onOk: async () => {
          const msg = await HttpUtil.post(
            `/panel/api/webhooks/del/${encodeURIComponent(w.id)}`,
            undefined,
            JSON_HEADERS,
          );
          if (!msg?.success) {
            messageApi.error(msg?.msg || t('somethingWentWrong'));
            return;
          }
          await load();
        },
      });
    },
    [load, messageApi, modal, t],
  );

  const test = useCallback(
    async (w: Webhook) => {
      const msg = await HttpUtil.post(
        `/panel/api/webhooks/test/${encodeURIComponent(w.id)}`,
        undefined,
        JSON_HEADERS,
      );
      if (!msg?.success) {
        messageApi.error(msg?.msg || t('pages.webhooks.testFailed'));
        return;
      }
      messageApi.success(t('pages.webhooks.testOk'));
      await load();
    },
    [load, messageApi, t],
  );

  const columns: ColumnsType<Webhook> = [
    {
      title: t('pages.webhooks.name'),
      key: 'name',
      render: (_: unknown, w) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{w.name}</Typography.Text>
          <Typography.Text type="secondary" ellipsis>
            {w.url}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('pages.webhooks.events'),
      key: 'events',
      render: (_: unknown, w) =>
        w.events.length === 0 ? (
          <Tag color="blue">{t('pages.webhooks.allEvents')}</Tag>
        ) : (
          <Space size={[4, 4]} wrap>
            {w.events.map((e) => (
              <Tag key={e}>{e}</Tag>
            ))}
          </Space>
        ),
    },
    {
      title: t('status'),
      key: 'status',
      width: 110,
      render: (_: unknown, w) => (
        <Tag color={w.enable ? 'green' : 'default'}>{w.enable ? t('enabled') : t('disabled')}</Tag>
      ),
    },
    {
      title: t('pages.webhooks.lastDelivery'),
      key: 'last',
      width: 220,
      render: (_: unknown, w) => {
        if (!w.lastAt) return <Typography.Text type="secondary">—</Typography.Text>;
        return (
          <Tooltip title={w.lastError || t('success')}>
            <Space direction="vertical" size={0}>
              <Tag color={w.lastOk ? 'green' : 'red'}>{w.lastOk ? t('success') : t('fail')}</Tag>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {new Date(w.lastAt).toLocaleString()}
              </Typography.Text>
            </Space>
          </Tooltip>
        );
      },
    },
    {
      title: t('pages.webhooks.actions'),
      key: 'actions',
      width: 150,
      render: (_: unknown, w) => (
        <Space size={4}>
          <Tooltip title={t('pages.webhooks.sendTest')}>
            <Button size="small" icon={<SendOutlined />} onClick={() => void test(w)} />
          </Tooltip>
          <Tooltip title={t('edit')}>
            <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(w)} />
          </Tooltip>
          <Tooltip title={t('delete')}>
            <Button size="small" danger icon={<DeleteOutlined />} onClick={() => remove(w)} />
          </Tooltip>
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={isDark ? 'webhooks-page is-dark' : 'webhooks-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Card
              size="small"
              title={t('pages.webhooks.title')}
              extra={
                <Space>
                  <Button icon={<ReloadOutlined />} onClick={() => void load()}>
                    {t('refresh')}
                  </Button>
                  <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
                    {t('pages.webhooks.create')}
                  </Button>
                </Space>
              }
            >
              <Alert
                type={error ? 'error' : 'info'}
                showIcon
                message={error ? (error as Error).message : t('pages.webhooks.intro')}
                style={{ marginBottom: 12 }}
              />
              <Table<Webhook>
                size="small"
                rowKey="id"
                loading={loading}
                columns={columns}
                dataSource={rows}
                pagination={false}
                locale={{ emptyText: t('pages.webhooks.empty') }}
              />
            </Card>
          </Layout.Content>
        </Layout>
      </Layout>

      <Modal
        open={dialogOpen}
        title={editing ? t('pages.webhooks.edit') : t('pages.webhooks.create')}
        okText={t('save')}
        cancelText={t('cancel')}
        confirmLoading={busy}
        onCancel={() => setDialogOpen(false)}
        onOk={() => void save()}
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label={t('pages.webhooks.name')}
            rules={[{ required: true, message: t('pages.webhooks.nameRequired') }]}
          >
            <Input placeholder="ops" />
          </Form.Item>
          <Form.Item
            name="url"
            label={t('pages.webhooks.url')}
            extra={t('pages.webhooks.urlHint')}
            rules={[{ required: true, message: t('pages.webhooks.urlRequired') }]}
          >
            <Input placeholder="https://example.com/hook" />
          </Form.Item>
          <Form.Item
            name="events"
            label={t('pages.webhooks.events')}
            extra={t('pages.webhooks.eventsHint')}
          >
            <Select
              mode="multiple"
              allowClear
              options={EVENTS.map((e) => ({ value: e, label: e }))}
              placeholder={t('pages.webhooks.allEvents')}
            />
          </Form.Item>
          <Form.Item
            name="secret"
            label={t('pages.webhooks.secret')}
            extra={editing ? t('pages.webhooks.secretKeep') : t('pages.webhooks.secretHint')}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="enable" label={t('status')} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </ConfigProvider>
  );
}
