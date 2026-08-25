import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Card,
  Col,
  Modal,
  Result,
  Row,
  Spin,
  Statistic,
  Typography,
  message,
} from 'antd';
import { CheckCircleOutlined, CloseCircleOutlined, CloudServerOutlined } from '@ant-design/icons';

import { usePeersQuery, type PeerRecord } from '@/api/queries/usePeersQuery';
import { usePeerMutations } from '@/api/queries/usePeerMutations';
import { useAllSettings } from '@/api/queries/useAllSettings';
import { keys } from '@/api/queryKeys';
import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { PeerIdentitySchema } from '@/schemas/peer';
import PeerList from './PeerList';
import PeerFormModal from './PeerFormModal';

// The master half of the nodes page: sibling panels this one falls back to,
// reached over their subscription endpoint rather than the node runtime.
export default function PeerPanels({ isMobile }: { isMobile: boolean }) {
  const { t } = useTranslation();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();

  const { peers, totals, loading, fetched, fetchError, refetch } = usePeersQuery();
  const { create, update, remove, setEnable, probe } = usePeerMutations();
  const { allSetting, fetched: settingsFetched } = useAllSettings();

  const { data: identityKey = '' } = useQuery({
    queryKey: keys.peers.identity(),
    queryFn: async () => {
      const raw = await HttpUtil.get('/panel/api/peers/identity', undefined, { silent: true });
      if (!raw?.success) return '';
      return parseMsg(raw, PeerIdentitySchema, 'peers/identity').obj?.key ?? '';
    },
    staleTime: 5 * 60 * 1000,
  });

  const [formOpen, setFormOpen] = useState(false);
  const [formMode, setFormMode] = useState<'add' | 'edit'>('add');
  const [formPeer, setFormPeer] = useState<PeerRecord | null>(null);

  const onAdd = useCallback(() => {
    setFormMode('add');
    setFormPeer(null);
    setFormOpen(true);
  }, []);

  const onEdit = useCallback((peer: PeerRecord) => {
    setFormMode('edit');
    setFormPeer({ ...peer });
    setFormOpen(true);
  }, []);

  const onSave = useCallback(
    async (payload: Partial<PeerRecord>) => {
      if (formMode === 'edit' && formPeer?.id) return update(formPeer.id, payload);
      return create(payload);
    },
    [formMode, formPeer, update, create],
  );

  const onDelete = useCallback(
    (peer: PeerRecord) => {
      modal.confirm({
        title: t('pages.peers.deleteConfirmTitle', { name: peer.name }),
        content: t('pages.peers.deleteConfirmContent'),
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: async () => {
          const msg = await remove(peer.id);
          if (msg?.success) messageApi.success(t('pages.peers.toasts.deleted'));
        },
      });
    },
    [modal, t, remove, messageApi],
  );

  const onProbe = useCallback(
    async (peer: PeerRecord) => {
      const msg = await probe(peer.id);
      if (msg?.success && msg.obj) {
        if (msg.obj.status === 'online') {
          messageApi.success(t('pages.peers.probeOk', { ms: msg.obj.latencyMs ?? 0 }));
        } else {
          messageApi.error(msg.obj.lastError || t('pages.peers.toasts.probeFailed'));
        }
      }
      refetch();
    },
    [probe, t, messageApi, refetch],
  );

  const onToggleEnable = useCallback(
    async (peer: PeerRecord, next: boolean) => {
      await setEnable(peer.id, next);
    },
    [setEnable],
  );

  const advertisingOff = settingsFetched && !allSetting.subFallbackEnable;

  return (
    <>
      {messageContextHolder}
      {modalContextHolder}
      <Spin spinning={!fetched} delay={200} description={t('loading')} size="large">
        {!fetched ? (
          <div className="loading-spacer" />
        ) : fetchError ? (
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
              <Card size="small" hoverable className="summary-card">
                <Row gutter={[16, isMobile ? 16 : 12]}>
                  <Col xs={12} sm={12} md={8}>
                    <Statistic
                      title={t('pages.peers.totalPeers')}
                      value={String(totals.total)}
                      prefix={<CloudServerOutlined />}
                    />
                  </Col>
                  <Col xs={12} sm={12} md={8}>
                    <Statistic
                      title={t('pages.peers.onlinePeers')}
                      value={String(totals.online)}
                      prefix={<CheckCircleOutlined style={{ color: 'var(--ant-color-success)' }} />}
                    />
                  </Col>
                  <Col xs={12} sm={12} md={8}>
                    <Statistic
                      title={t('pages.peers.advertisedPeers')}
                      value={String(totals.advertised)}
                      prefix={<CloseCircleOutlined style={{ color: 'var(--ant-color-error)' }} />}
                    />
                  </Col>
                </Row>
              </Card>
            </Col>

            {advertisingOff && (
              <Col span={24}>
                <Alert type="warning" showIcon title={t('pages.peers.advertisingOff')} />
              </Col>
            )}

            {identityKey && (
              <Col span={24}>
                <Card size="small" title={t('pages.peers.identityTitle')}>
                  <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
                    {t('pages.peers.identityHint')}
                  </Typography.Paragraph>
                  <Typography.Text code copyable={{ text: identityKey }}>
                    {identityKey}
                  </Typography.Text>
                </Card>
              </Col>
            )}

            <Col span={24}>
              <PeerList
                peers={peers}
                loading={loading}
                isMobile={isMobile}
                onAdd={onAdd}
                onEdit={onEdit}
                onDelete={onDelete}
                onProbe={onProbe}
                onToggleEnable={onToggleEnable}
              />
            </Col>
          </Row>
        )}
      </Spin>

      <PeerFormModal
        open={formOpen}
        mode={formMode}
        peer={formPeer}
        save={onSave}
        onOpenChange={setFormOpen}
      />
    </>
  );
}
