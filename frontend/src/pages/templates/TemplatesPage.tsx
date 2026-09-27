import { useEffect, useMemo, useState } from 'react';
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
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { DeleteOutlined, PlusOutlined, SendOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import { useNodesQuery } from '@/api/queries/useNodesQuery';
import {
  useTemplates,
  type InboundTemplate,
  type InboundTemplateResult,
} from '@/api/queries/useTemplates';

export default function TemplatesPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [modal, modalContextHolder] = Modal.useModal();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const api = useTemplates();
  const { nodes } = useNodesQuery();
  const nodeName = useMemo(
    () => new Map(nodes.map((n) => [n.id, n.name ?? String(n.id)])),
    [nodes],
  );

  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const [remark, setRemark] = useState('');
  const [picked, setPicked] = useState<number[]>([]);
  const [applyTo, setApplyTo] = useState<InboundTemplate | null>(null);
  const [targets, setTargets] = useState<number[]>([]);
  const [results, setResults] = useState<InboundTemplateResult[] | null>(null);
  const [busy, setBusy] = useState(false);

  const inboundOptions = api.inbounds.map((i) => ({
    value: i.id,
    label: `${i.nodeId ? (nodeName.get(i.nodeId) ?? `#${i.nodeId}`) : t('pages.templates.thisPanel')} · ${i.remark || i.protocol} :${i.port}`,
  }));
  const panelOptions = [
    { value: 0, label: t('pages.templates.thisPanel') },
    ...nodes.map((n) => ({ value: n.id, label: `${n.name} (${n.address})` })),
  ];

  const columns: ColumnsType<InboundTemplate> = [
    {
      title: t('pages.templates.name'),
      key: 'name',
      render: (_: unknown, r) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{r.name}</Typography.Text>
          {r.remark && <Typography.Text type="secondary">{r.remark}</Typography.Text>}
        </Space>
      ),
    },
    {
      title: t('pages.templates.inbounds'),
      key: 'items',
      render: (_: unknown, r) => (
        <Space size={[4, 4]} wrap>
          {(r.items ?? []).map((it, i) => (
            <Tag key={i} color={it.enable ? 'blue' : 'default'}>
              {it.remark || it.protocol} :{it.port}
            </Tag>
          ))}
        </Space>
      ),
    },
    {
      title: t('pages.templates.actions'),
      key: 'actions',
      render: (_: unknown, r) => (
        <Space size={4}>
          <Button
            size="small"
            type="primary"
            icon={<SendOutlined />}
            onClick={() => {
              setApplyTo(r);
              setTargets([]);
              setResults(null);
            }}
          >
            {t('pages.templates.apply')}
          </Button>
          <Button
            size="small"
            danger
            icon={<DeleteOutlined />}
            aria-label={t('delete')}
            onClick={() =>
              modal.confirm({
                title: t('pages.templates.deleteConfirm', { name: r.name }),
                onOk: () => api.remove(r.id),
              })
            }
          />
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={isDark ? 'templates-page is-dark' : 'templates-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Card
              size="small"
              title={t('pages.templates.title')}
              extra={
                <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
                  {t('pages.templates.create')}
                </Button>
              }
            >
              <Alert
                type="info"
                showIcon
                message={t('pages.templates.intro')}
                style={{ marginBottom: 12 }}
              />
              <Table<InboundTemplate>
                size="small"
                rowKey="id"
                loading={api.loading}
                columns={columns}
                dataSource={api.templates}
                pagination={false}
                locale={{ emptyText: t('pages.templates.empty') }}
              />
            </Card>
          </Layout.Content>
        </Layout>

        <Modal
          open={createOpen}
          title={t('pages.templates.create')}
          okText={t('save')}
          cancelText={t('cancel')}
          confirmLoading={busy}
          onCancel={() => setCreateOpen(false)}
          onOk={async () => {
            setBusy(true);
            try {
              const msg = await api.create(name, remark, picked);
              if (msg?.success) {
                setCreateOpen(false);
                setName('');
                setRemark('');
                setPicked([]);
              }
            } finally {
              setBusy(false);
            }
          }}
        >
          <Form layout="vertical">
            <Form.Item label={t('pages.templates.name')}>
              <Input
                value={name}
                placeholder="RU entry"
                onChange={(e) => setName(e.target.value)}
              />
            </Form.Item>
            <Form.Item label={t('pages.templates.remark')}>
              <Input value={remark} onChange={(e) => setRemark(e.target.value)} />
            </Form.Item>
            <Form.Item label={t('pages.templates.inbounds')} extra={t('pages.templates.pickHint')}>
              <Select
                mode="multiple"
                showSearch
                optionFilterProp="label"
                value={picked}
                onChange={setPicked}
                options={inboundOptions}
              />
            </Form.Item>
          </Form>
        </Modal>

        <Modal
          open={applyTo !== null}
          title={t('pages.templates.applyTitle', { name: applyTo?.name ?? '' })}
          okText={t('pages.templates.apply')}
          cancelText={t('close')}
          confirmLoading={busy}
          okButtonProps={{ disabled: targets.length === 0 }}
          onCancel={() => setApplyTo(null)}
          onOk={async () => {
            if (!applyTo) return;
            setBusy(true);
            try {
              setResults(await api.apply(applyTo.id, targets));
            } finally {
              setBusy(false);
            }
          }}
        >
          <Form layout="vertical">
            <Form.Item
              label={t('pages.templates.targets')}
              extra={t('pages.templates.targetsHint')}
            >
              <Select
                mode="multiple"
                value={targets}
                onChange={setTargets}
                options={panelOptions}
              />
            </Form.Item>
          </Form>
          {results?.map((r) => (
            <Alert
              key={r.nodeId}
              style={{ marginBottom: 8 }}
              type={r.errors?.length ? 'warning' : 'success'}
              showIcon
              message={`${r.nodeId === 0 ? t('pages.templates.thisPanel') : (nodeName.get(r.nodeId) ?? r.nodeId)}: ${t(
                'pages.templates.result',
                {
                  created: r.created?.length ?? 0,
                  skipped: r.skipped?.length ?? 0,
                  attached: r.attached,
                },
              )}`}
              description={r.errors?.length ? r.errors.join('\n') : undefined}
            />
          ))}
        </Modal>
      </Layout>
    </ConfigProvider>
  );
}
