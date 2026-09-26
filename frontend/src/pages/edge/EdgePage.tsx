import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  Col,
  ConfigProvider,
  Form,
  Input,
  InputNumber,
  Layout,
  Modal,
  Row,
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
import { DeleteOutlined, EditOutlined, PlusOutlined, RadarChartOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import {
  useEdge,
  type EdgeRoute,
  type EdgeSettings,
  type EdgeTargetCheck,
} from '@/api/queries/useEdge';

const emptyRoute: EdgeRoute = {
  id: 0,
  name: '',
  kind: 'sni',
  enable: true,
  sortOrder: 0,
  match: '',
  pathPrefix: '',
  action: 'tcp',
  target: '',
  proxyProtocol: 0,
  stripPrefix: false,
  createdAt: 0,
  updatedAt: 0,
};

export default function EdgePage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [modal, modalContextHolder] = Modal.useModal();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const api = useEdge();
  const [draft, setDraft] = useState<EdgeSettings | null>(null);
  const shown = draft ?? api.settings;
  const [edit, setEdit] = useState<EdgeRoute | null>(null);
  const [checks, setChecks] = useState<Map<number, EdgeTargetCheck>>(new Map());
  const [checking, setChecking] = useState(false);

  async function runCheck() {
    setChecking(true);
    try {
      const res = await api.check();
      setChecks(new Map(res.map((c) => [c.routeId, c])));
    } finally {
      setChecking(false);
    }
  }

  const columns = useMemo<ColumnsType<EdgeRoute>>(
    () => [
      {
        title: t('pages.edge.match'),
        key: 'match',
        render: (_: unknown, r) => (
          <Space direction="vertical" size={0}>
            <Space size={4}>
              <Tag color={r.kind === 'sni' ? 'geekblue' : 'purple'}>{r.kind.toUpperCase()}</Tag>
              <Typography.Text strong>
                {r.match || (r.kind === 'sni' ? t('pages.edge.noSni') : t('pages.edge.anyHost'))}
                {r.kind === 'http' ? r.pathPrefix : ''}
              </Typography.Text>
            </Space>
            {r.name && <Typography.Text type="secondary">{r.name}</Typography.Text>}
          </Space>
        ),
      },
      {
        title: t('pages.edge.target'),
        key: 'target',
        render: (_: unknown, r) => {
          const c = checks.get(r.id);
          return (
            <Space size={4} wrap>
              <Tag>{t(`pages.edge.action_${r.action}`)}</Tag>
              {r.target && <Typography.Text code>{r.target}</Typography.Text>}
              {r.proxyProtocol > 0 && <Tag color="gold">PROXY v{r.proxyProtocol}</Tag>}
              {c && (
                <Tooltip title={c.error || undefined}>
                  <Tag color={c.ok ? 'green' : 'red'}>{c.ok ? 'up' : 'down'}</Tag>
                </Tooltip>
              )}
            </Space>
          );
        },
      },
      {
        title: t('pages.edge.enable'),
        key: 'enable',
        render: (_: unknown, r) => (
          <Switch
            size="small"
            checked={r.enable}
            onChange={(v) => api.saveRoute({ ...r, enable: v })}
          />
        ),
      },
      {
        title: t('pages.edge.actions'),
        key: 'actions',
        render: (_: unknown, r) => (
          <Space size={4}>
            <Button
              size="small"
              icon={<EditOutlined />}
              onClick={() => setEdit(r)}
              aria-label={t('edit')}
            />
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              aria-label={t('delete')}
              onClick={() =>
                modal.confirm({
                  title: t('pages.edge.deleteConfirm', { name: r.name || r.match }),
                  onOk: () => api.removeRoute(r.id),
                })
              }
            />
          </Space>
        ),
      },
    ],
    [t, api, checks, modal],
  );

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={isDark ? 'edge-page is-dark' : 'edge-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Row gutter={[16, 12]}>
              <Col span={24}>
                <Alert type="info" showIcon message={t('pages.edge.intro')} />
              </Col>
              <Col xs={24} lg={10}>
                <Card
                  size="small"
                  title={t('pages.edge.portTitle')}
                  extra={
                    api.status?.running ? (
                      <Tag color="green">
                        {t('pages.edge.running', { addr: api.status.listen })}
                      </Tag>
                    ) : (
                      <Tag>{t('pages.edge.stopped')}</Tag>
                    )
                  }
                >
                  {api.status?.lastError && (
                    <Alert
                      type="error"
                      showIcon
                      message={api.status.lastError}
                      style={{ marginBottom: 12 }}
                    />
                  )}
                  {api.status && !api.status.panelTls && (
                    <Alert
                      type="warning"
                      showIcon
                      message={t('pages.edge.noPanelTls')}
                      style={{ marginBottom: 12 }}
                    />
                  )}
                  {shown && (
                    <Form layout="vertical">
                      <Form.Item label={t('pages.edge.enableEdge')}>
                        <Switch
                          checked={shown.enable}
                          onChange={(v) => setDraft({ ...shown, enable: v })}
                        />
                      </Form.Item>
                      <Form.Item label={t('pages.edge.listen')} extra={t('pages.edge.listenHint')}>
                        <Input
                          value={shown.listen}
                          onChange={(e) => setDraft({ ...shown, listen: e.target.value })}
                        />
                      </Form.Item>
                      <Form.Item
                        label={t('pages.edge.default')}
                        extra={t('pages.edge.defaultHint')}
                      >
                        <Select
                          value={shown.defaultAction}
                          onChange={(v) => setDraft({ ...shown, defaultAction: v })}
                          options={['tcp', 'panel', 'reject'].map((a) => ({
                            value: a,
                            label: t(`pages.edge.action_${a}`),
                          }))}
                        />
                      </Form.Item>
                      {shown.defaultAction === 'tcp' && (
                        <Row gutter={8}>
                          <Col span={16}>
                            <Form.Item label={t('pages.edge.target')}>
                              <Input
                                placeholder="127.0.0.1:8443"
                                value={shown.defaultTarget}
                                onChange={(e) =>
                                  setDraft({ ...shown, defaultTarget: e.target.value })
                                }
                              />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item label="PROXY">
                              <Select
                                value={shown.defaultProxyProtocol}
                                onChange={(v) => setDraft({ ...shown, defaultProxyProtocol: v })}
                                options={[0, 1, 2].map((v) => ({
                                  value: v,
                                  label: v === 0 ? t('pages.edge.ppOff') : `v${v}`,
                                }))}
                              />
                            </Form.Item>
                          </Col>
                        </Row>
                      )}
                      <Button
                        type="primary"
                        disabled={!draft}
                        onClick={async () => {
                          if (!draft) return;
                          const msg = await api.saveSettings(draft);
                          if (msg?.success) setDraft(null);
                        }}
                      >
                        {t('save')}
                      </Button>
                    </Form>
                  )}
                </Card>
              </Col>
              <Col xs={24} lg={14}>
                <Card
                  size="small"
                  title={t('pages.edge.routesTitle')}
                  extra={
                    <Space>
                      <Button icon={<RadarChartOutlined />} loading={checking} onClick={runCheck}>
                        {t('pages.edge.check')}
                      </Button>
                      <Button
                        type="primary"
                        icon={<PlusOutlined />}
                        onClick={() => setEdit({ ...emptyRoute })}
                      >
                        {t('pages.edge.addRoute')}
                      </Button>
                    </Space>
                  }
                >
                  <Table<EdgeRoute>
                    size="small"
                    rowKey="id"
                    loading={api.loading}
                    columns={columns}
                    dataSource={api.routes}
                    pagination={false}
                    locale={{ emptyText: t('pages.edge.noRoutes') }}
                  />
                </Card>
              </Col>
            </Row>
          </Layout.Content>
        </Layout>
        <RouteModal route={edit} onClose={() => setEdit(null)} save={api.saveRoute} />
      </Layout>
    </ConfigProvider>
  );
}

function RouteModal({
  route,
  onClose,
  save,
}: {
  route: EdgeRoute | null;
  onClose: () => void;
  save: (r: EdgeRoute) => Promise<{ success?: boolean } | undefined>;
}) {
  const { t } = useTranslation();
  const [v, setV] = useState<EdgeRoute | null>(route);
  const [seen, setSeen] = useState<EdgeRoute | null>(route);
  if (seen !== route) {
    setSeen(route);
    setV(route);
  }
  if (!v) return null;
  const set = (patch: Partial<EdgeRoute>) => setV({ ...v, ...patch });
  return (
    <Modal
      open={route !== null}
      title={v.id ? t('pages.edge.editRoute') : t('pages.edge.addRoute')}
      okText={t('save')}
      cancelText={t('cancel')}
      onCancel={onClose}
      onOk={async () => {
        const msg = await save(v);
        if (msg?.success) onClose();
      }}
    >
      <Form layout="vertical">
        <Form.Item label={t('pages.edge.kind')}>
          <Select
            value={v.kind}
            onChange={(kind) => set({ kind, action: kind === 'http' ? 'proxy' : 'tcp' })}
            options={[
              { value: 'sni', label: t('pages.edge.kindSni') },
              { value: 'http', label: t('pages.edge.kindHttp') },
            ]}
          />
        </Form.Item>
        <Form.Item label={t('pages.edge.name')}>
          <Input value={v.name} onChange={(e) => set({ name: e.target.value })} />
        </Form.Item>
        <Row gutter={8}>
          <Col span={v.kind === 'http' ? 14 : 24}>
            <Form.Item
              label={v.kind === 'sni' ? t('pages.edge.sni') : t('pages.edge.host')}
              extra={v.kind === 'sni' ? t('pages.edge.sniHint') : t('pages.edge.hostHint')}
            >
              <Input
                value={v.match}
                placeholder="de2.walk.boykisser.online"
                onChange={(e) => set({ match: e.target.value })}
              />
            </Form.Item>
          </Col>
          {v.kind === 'http' && (
            <Col span={10}>
              <Form.Item label={t('pages.edge.path')}>
                <Input
                  value={v.pathPrefix}
                  placeholder="/ws"
                  onChange={(e) => set({ pathPrefix: e.target.value })}
                />
              </Form.Item>
            </Col>
          )}
        </Row>
        {v.kind === 'sni' && (
          <Form.Item label={t('pages.edge.action')}>
            <Select
              value={v.action}
              onChange={(action) => set({ action })}
              options={['tcp', 'panel', 'reject'].map((a) => ({
                value: a,
                label: t(`pages.edge.action_${a}`),
              }))}
            />
          </Form.Item>
        )}
        {(v.kind === 'http' || v.action === 'tcp') && (
          <Row gutter={8}>
            <Col span={v.kind === 'sni' ? 16 : 24}>
              <Form.Item
                label={t('pages.edge.target')}
                extra={v.kind === 'http' ? t('pages.edge.urlHint') : undefined}
              >
                <Input
                  value={v.target}
                  placeholder={v.kind === 'http' ? 'http://127.0.0.1:10001' : '127.0.0.1:8443'}
                  onChange={(e) => set({ target: e.target.value })}
                />
              </Form.Item>
            </Col>
            {v.kind === 'sni' && (
              <Col span={8}>
                <Form.Item label="PROXY" extra={t('pages.edge.ppHint')}>
                  <InputNumber
                    min={0}
                    max={2}
                    value={v.proxyProtocol}
                    onChange={(n) => set({ proxyProtocol: n ?? 0 })}
                  />
                </Form.Item>
              </Col>
            )}
          </Row>
        )}
        {v.kind === 'http' && (
          <Form.Item label={t('pages.edge.stripPrefix')}>
            <Switch checked={v.stripPrefix} onChange={(stripPrefix) => set({ stripPrefix })} />
          </Form.Item>
        )}
      </Form>
    </Modal>
  );
}
