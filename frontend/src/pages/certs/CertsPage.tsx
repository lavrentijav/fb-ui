import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  AutoComplete,
  Button,
  Card,
  Col,
  ConfigProvider,
  Dropdown,
  Input,
  Layout,
  Modal,
  Result,
  Row,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  CloudDownloadOutlined,
  DeleteOutlined,
  EditOutlined,
  ImportOutlined,
  PlusOutlined,
  SafetyCertificateOutlined,
  SyncOutlined,
} from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import {
  useCerts,
  type Certificate,
  type CertificateRequest,
  type DnsProvider,
  type DnsProviderRequest,
} from '@/api/queries/useCerts';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import CertFormModal from './CertFormModal';
import DnsProviderModal from './DnsProviderModal';
import { useNodesQuery } from '@/api/queries/useNodesQuery';

const LE_PROD = 'https://acme-v02.api.letsencrypt.org/directory';
const LE_STAGING = 'https://acme-staging-v02.api.letsencrypt.org/directory';
const DAY = 86400;

function daysLeft(notAfter: number): number {
  return Math.floor((notAfter - Date.now() / 1000) / DAY);
}

export default function CertsPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { isMobile } = useMediaQuery();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const api = useCerts();
  const { certs, dns, account, loading, fetched, fetchError, refetch } = api;

  const [certOpen, setCertOpen] = useState(false);
  const [certRecord, setCertRecord] = useState<Certificate | null>(null);
  const [dnsOpen, setDnsOpen] = useState(false);
  const [dnsRecord, setDnsRecord] = useState<DnsProvider | null>(null);
  const [email, setEmail] = useState<string | null>(null);
  const [directory, setDirectory] = useState<string | null>(null);

  const dnsNameById = useMemo(() => new Map(dns.map((d) => [d.id, d.name])), [dns]);
  const { nodes } = useNodesQuery();
  const nodeNameById = useMemo(() => new Map(nodes.map((n) => [n.id, n.name])), [nodes]);

  const saveCert = useCallback(
    (p: CertificateRequest) => (certRecord ? api.updateCert(certRecord.id, p) : api.createCert(p)),
    [certRecord, api],
  );
  const saveDns = useCallback(
    (p: DnsProviderRequest) => (dnsRecord ? api.updateDns(dnsRecord.id, p) : api.createDns(p)),
    [dnsRecord, api],
  );

  const confirmDelete = useCallback(
    (title: string, run: () => Promise<{ success?: boolean } | undefined>) => {
      modal.confirm({
        title,
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: run,
      });
    },
    [modal, t],
  );

  const onScan = useCallback(async () => {
    const msg = await api.scanCerts();
    const added = (msg?.obj as { added?: number } | undefined)?.added ?? 0;
    if (msg?.success) messageApi.success(t('pages.certs.scanResult', { count: added }));
  }, [api, messageApi, t]);

  const certColumns = useMemo<ColumnsType<Certificate>>(
    () => [
      {
        title: t('pages.certs.domains'),
        key: 'domains',
        render: (_: unknown, c) => (
          <Space direction="vertical" size={0}>
            {c.domains.map((d) => (
              <Typography.Text key={d} strong={d === c.domains[0]}>
                {d}
              </Typography.Text>
            ))}
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {c.certFile}
            </Typography.Text>
          </Space>
        ),
      },
      {
        title: t('pages.certs.challenge'),
        key: 'challenge',
        render: (_: unknown, c) =>
          c.challenge === 'imported' ? (
            <Tag>{t('pages.certs.imported')}</Tag>
          ) : (
            <Space size={4} wrap>
              <Tag color="blue">{c.challenge === 'dns01' ? 'DNS-01' : 'HTTP-01'}</Tag>
              {c.dnsProviderId > 0 && <Tag>{dnsNameById.get(c.dnsProviderId) ?? '?'}</Tag>}
              {(c.deliveries ?? []).map((d) => (
                <Tooltip key={d.nodeId} title={d.pushError || undefined}>
                  <Tag color={d.pushError ? 'red' : d.pushedAt > 0 ? 'green' : 'gold'}>
                    {d.nodeId === 0
                      ? t('pages.certs.thisPanel')
                      : t('pages.certs.onNode', { name: nodeNameById.get(d.nodeId) ?? d.nodeId })}
                  </Tag>
                </Tooltip>
              ))}
            </Space>
          ),
      },
      {
        title: t('pages.certs.status'),
        key: 'status',
        render: (_: unknown, c) => {
          const color =
            c.status === 'valid'
              ? 'green'
              : c.status === 'error'
                ? 'red'
                : c.status === 'issuing'
                  ? 'processing'
                  : 'default';
          const tag = <Tag color={color}>{t(`pages.certs.status_${c.status}`)}</Tag>;
          return c.lastError ? <Tooltip title={c.lastError}>{tag}</Tooltip> : tag;
        },
      },
      {
        title: t('pages.certs.expires'),
        key: 'expires',
        render: (_: unknown, c) => {
          if (!c.notAfter) return <Typography.Text type="secondary">—</Typography.Text>;
          const left = daysLeft(c.notAfter);
          const type = left < 7 ? 'danger' : left < 30 ? 'warning' : undefined;
          return (
            <Tooltip title={c.issuer}>
              <Space direction="vertical" size={0}>
                <span>{new Date(c.notAfter * 1000).toLocaleDateString()}</span>
                <Typography.Text type={type} style={{ fontSize: 12 }}>
                  {t('pages.certs.daysLeft', { count: left })}
                  {c.autoRenew && c.challenge !== 'imported'
                    ? ` · ${t('pages.certs.autoRenew')}`
                    : ''}
                </Typography.Text>
              </Space>
            </Tooltip>
          );
        },
      },
      {
        title: t('pages.certs.actions'),
        key: 'actions',
        render: (_: unknown, c) => (
          <Space size={4}>
            {c.challenge !== 'imported' && (
              <Tooltip title={c.notAfter ? t('pages.certs.renew') : t('pages.certs.issue')}>
                <Button
                  size="small"
                  type="primary"
                  icon={<SafetyCertificateOutlined />}
                  loading={c.status === 'issuing'}
                  onClick={async () => {
                    const msg = await api.issueCert(c.id);
                    if (msg?.success) messageApi.info(t('pages.certs.toasts.issueStarted'));
                  }}
                  aria-label={t('pages.certs.issue')}
                />
              </Tooltip>
            )}
            <Dropdown
              trigger={['click']}
              disabled={!c.notAfter}
              menu={{
                items: [
                  { key: 'web', label: t('pages.certs.applyWeb') },
                  { key: 'sub', label: t('pages.certs.applySub') },
                ],
                onClick: async ({ key }) => {
                  const msg = await api.applyCert(c.id, key as 'web' | 'sub');
                  if (msg?.success) messageApi.success(t('pages.certs.appliedRestart'));
                },
              }}
            >
              <Button size="small">{t('pages.certs.apply')}</Button>
            </Dropdown>
            <Tooltip title={t('edit')}>
              <Button
                size="small"
                icon={<EditOutlined />}
                onClick={() => {
                  setCertRecord(c);
                  setCertOpen(true);
                }}
                aria-label={t('edit')}
              />
            </Tooltip>
            <Tooltip title={t('delete')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                onClick={() =>
                  confirmDelete(t('pages.certs.deleteCertConfirm', { name: c.domains[0] }), () =>
                    api.removeCert(c.id),
                  )
                }
                aria-label={t('delete')}
              />
            </Tooltip>
          </Space>
        ),
      },
    ],
    [t, dnsNameById, nodeNameById, api, messageApi, confirmDelete],
  );

  const dnsColumns = useMemo<ColumnsType<DnsProvider>>(
    () => [
      {
        title: t('pages.certs.dnsAccount'),
        key: 'zone',
        render: (_: unknown, d) => {
          const zones = d.zone ? [d.zone] : (d.zones ?? []);
          return (
            <Space direction="vertical" size={2}>
              <Typography.Text strong>{d.name}</Typography.Text>
              <Space size={[4, 4]} wrap>
                {zones.map((z) => (
                  <Tag key={z}>{z}</Tag>
                ))}
                {zones.length === 0 && (
                  <Tag color="red">{t('pages.certs.zonesFound', { count: 0 })}</Tag>
                )}
              </Space>
            </Space>
          );
        },
      },
      {
        title: t('pages.certs.dnsType'),
        key: 'type',
        render: (_: unknown, d) => (
          <Space direction="vertical" size={0}>
            <Tag color={d.type === 'cloudflare' ? 'orange' : 'geekblue'}>
              {d.type === 'cloudflare' ? 'Cloudflare' : 'Technitium'}
            </Tag>
            {d.apiUrl && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {d.apiPort > 0 ? `${d.apiUrl} · ${d.apiPort}` : d.apiUrl}
              </Typography.Text>
            )}
          </Space>
        ),
      },
      {
        title: t('pages.certs.credential'),
        key: 'cred',
        render: (_: unknown, d) =>
          d.hasCredential ? (
            <Tag color="green">{t('pages.certs.credentialSet')}</Tag>
          ) : (
            <Tag color="red">{t('pages.certs.credentialMissing')}</Tag>
          ),
      },
      {
        title: t('pages.certs.actions'),
        key: 'actions',
        render: (_: unknown, d) => (
          <Space size={4}>
            <Tooltip title={t('pages.certs.dnsSync')}>
              <Button
                size="small"
                icon={<SyncOutlined />}
                onClick={() => api.syncDns(d.id)}
                aria-label={t('pages.certs.dnsSync')}
              />
            </Tooltip>
            <Tooltip title={t('edit')}>
              <Button
                size="small"
                icon={<EditOutlined />}
                onClick={() => {
                  setDnsRecord(d);
                  setDnsOpen(true);
                }}
                aria-label={t('edit')}
              />
            </Tooltip>
            <Tooltip title={t('delete')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                onClick={() =>
                  confirmDelete(t('pages.certs.deleteDnsConfirm', { name: d.name }), () =>
                    api.removeDns(d.id),
                  )
                }
                aria-label={t('delete')}
              />
            </Tooltip>
          </Space>
        ),
      },
    ],
    [t, api, confirmDelete],
  );

  const pageClass = useMemo(() => {
    const classes = ['certs-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  const shownEmail = email ?? account?.email ?? '';
  const shownDirectory = directory ?? account?.directory ?? LE_PROD;

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Spin spinning={!fetched} delay={200} description={t('loading')} size="large">
              {fetchError ? (
                <Result
                  status="error"
                  title={t('somethingWentWrong')}
                  subTitle={fetchError}
                  extra={
                    <Button type="primary" loading={loading} onClick={() => refetch()}>
                      {t('refresh')}
                    </Button>
                  }
                />
              ) : (
                <Row gutter={[isMobile ? 8 : 16, isMobile ? 8 : 12]}>
                  <Col span={24}>
                    <Card
                      size="small"
                      title={t('pages.certs.certsTitle')}
                      extra={
                        <Space wrap>
                          <Button icon={<ImportOutlined />} onClick={onScan}>
                            {t('pages.certs.scan')}
                          </Button>
                          <Button
                            type="primary"
                            icon={<PlusOutlined />}
                            onClick={() => {
                              setCertRecord(null);
                              setCertOpen(true);
                            }}
                          >
                            {t('pages.certs.addCert')}
                          </Button>
                        </Space>
                      }
                    >
                      <Table<Certificate>
                        rowKey="id"
                        size="small"
                        columns={certColumns}
                        dataSource={certs}
                        loading={loading}
                        pagination={false}
                        scroll={isMobile ? { x: 'max-content' } : undefined}
                        locale={{ emptyText: t('pages.certs.noCerts') }}
                      />
                    </Card>
                  </Col>
                  <Col xs={24} lg={14}>
                    <Card
                      size="small"
                      title={t('pages.certs.dnsTitle')}
                      extra={
                        <Button
                          type="primary"
                          icon={<PlusOutlined />}
                          onClick={() => {
                            setDnsRecord(null);
                            setDnsOpen(true);
                          }}
                        >
                          {t('pages.certs.addDns')}
                        </Button>
                      }
                    >
                      <Table<DnsProvider>
                        rowKey="id"
                        size="small"
                        columns={dnsColumns}
                        dataSource={dns}
                        loading={loading}
                        pagination={false}
                        scroll={isMobile ? { x: 'max-content' } : undefined}
                        locale={{ emptyText: t('pages.certs.noDns') }}
                      />
                    </Card>
                  </Col>
                  <Col xs={24} lg={10}>
                    <Card size="small" title={t('pages.certs.accountTitle')}>
                      <Space direction="vertical" style={{ width: '100%' }}>
                        <Typography.Text type="secondary">
                          {t('pages.certs.accountHint')}
                        </Typography.Text>
                        <Input
                          placeholder="ops@example.com"
                          value={shownEmail}
                          onChange={(e) => setEmail(e.target.value)}
                          aria-label={t('pages.certs.email')}
                        />
                        <AutoComplete
                          style={{ width: '100%' }}
                          value={shownDirectory}
                          onChange={(v) => setDirectory(v)}
                          options={[
                            { value: LE_PROD, label: "Let's Encrypt" },
                            { value: LE_STAGING, label: "Let's Encrypt (staging)" },
                          ]}
                          aria-label={t('pages.certs.directory')}
                        />
                        <Space>
                          <Button
                            type="primary"
                            icon={<CloudDownloadOutlined />}
                            onClick={async () => {
                              const msg = await api.saveAccount({
                                email: shownEmail,
                                directory: shownDirectory,
                              });
                              if (msg?.success) {
                                setEmail(null);
                                setDirectory(null);
                                messageApi.success(t('pages.certs.toasts.save'));
                              }
                            }}
                          >
                            {t('save')}
                          </Button>
                          {account?.registered && (
                            <Tag color="green">{t('pages.certs.registered')}</Tag>
                          )}
                        </Space>
                      </Space>
                    </Card>
                  </Col>
                </Row>
              )}
            </Spin>
          </Layout.Content>
        </Layout>

        <CertFormModal
          open={certOpen}
          cert={certRecord}
          dns={dns}
          save={saveCert}
          onOpenChange={setCertOpen}
        />
        <DnsProviderModal
          open={dnsOpen}
          provider={dnsRecord}
          save={saveDns}
          test={api.testDns}
          onOpenChange={setDnsOpen}
        />
      </Layout>
    </ConfigProvider>
  );
}
