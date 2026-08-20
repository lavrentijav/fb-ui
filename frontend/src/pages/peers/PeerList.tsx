import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge, Button, Card, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd';
import type { BadgeProps } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { DeleteOutlined, EditOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons';

import type { PeerRecord } from '@/api/queries/usePeersQuery';

export interface PeerListProps {
  peers: PeerRecord[];
  loading: boolean;
  isMobile: boolean;
  onAdd: () => void;
  onEdit: (peer: PeerRecord) => void;
  onDelete: (peer: PeerRecord) => void;
  onProbe: (peer: PeerRecord) => void;
  onToggleEnable: (peer: PeerRecord, next: boolean) => void;
}

function statusBadge(status?: string): BadgeProps['status'] {
  if (status === 'online') return 'success';
  if (status === 'offline') return 'error';
  return 'default';
}

function peerEndpoint(peer: PeerRecord): string {
  const scheme = peer.scheme || 'https';
  const subPath = peer.subPath || '/sub/';
  return `${scheme}://${peer.domain ?? ''}:${peer.port ?? 0}${subPath}`;
}

export default function PeerList({
  peers,
  loading,
  isMobile,
  onAdd,
  onEdit,
  onDelete,
  onProbe,
  onToggleEnable,
}: PeerListProps) {
  const { t } = useTranslation();

  const columns = useMemo<ColumnsType<PeerRecord>>(
    () => [
      {
        title: t('pages.peers.columns.name'),
        dataIndex: 'name',
        key: 'name',
        render: (_: unknown, peer) => (
          <Space direction="vertical" size={0}>
            <Space size={4}>
              <Typography.Text strong>{peer.name}</Typography.Text>
              {peer.isSelf && <Tag color="blue">{t('pages.peers.self')}</Tag>}
            </Space>
            {peer.remark && <Typography.Text type="secondary">{peer.remark}</Typography.Text>}
          </Space>
        ),
      },
      {
        title: t('pages.peers.columns.endpoint'),
        key: 'endpoint',
        render: (_: unknown, peer) => (
          <Typography.Text copyable={{ text: peerEndpoint(peer) }}>
            {peerEndpoint(peer)}
          </Typography.Text>
        ),
      },
      {
        title: t('pages.peers.columns.ips'),
        key: 'ips',
        render: (_: unknown, peer) =>
          peer.ips && peer.ips.length > 0 ? (
            <Space size={4} wrap>
              {peer.ips.map((ip) => (
                <Tag key={ip}>{ip}</Tag>
              ))}
            </Space>
          ) : (
            <Typography.Text type="secondary">—</Typography.Text>
          ),
      },
      {
        title: t('pages.peers.columns.status'),
        key: 'status',
        render: (_: unknown, peer) => (
          <Tooltip title={peer.lastError || undefined}>
            <Badge
              status={statusBadge(peer.status)}
              text={
                peer.status === 'online' && peer.latencyMs
                  ? `${t('pages.peers.statusOnline')} · ${peer.latencyMs} ms`
                  : peer.status === 'offline'
                    ? t('pages.peers.statusOffline')
                    : t('pages.peers.statusUnknown')
              }
            />
          </Tooltip>
        ),
      },
      {
        title: t('pages.peers.columns.advertised'),
        key: 'advertised',
        render: (_: unknown, peer) =>
          peer.enable && peer.status === 'online' && !peer.isSelf ? (
            <Tag color="green">{t('pages.peers.advertisedYes')}</Tag>
          ) : (
            <Tag>{t('pages.peers.advertisedNo')}</Tag>
          ),
      },
      {
        title: t('enable'),
        key: 'enable',
        render: (_: unknown, peer) => (
          <Switch
            checked={!!peer.enable}
            onChange={(next) => onToggleEnable(peer, next)}
            aria-label={t('pages.peers.columns.enable')}
          />
        ),
      },
      {
        title: t('pages.peers.columns.actions'),
        key: 'actions',
        render: (_: unknown, peer) => (
          <Space size={4}>
            <Tooltip title={t('pages.peers.probe')}>
              <Button
                size="small"
                icon={<ThunderboltOutlined />}
                onClick={() => onProbe(peer)}
                aria-label={t('pages.peers.probe')}
              />
            </Tooltip>
            <Tooltip title={t('edit')}>
              <Button
                size="small"
                icon={<EditOutlined />}
                onClick={() => onEdit(peer)}
                aria-label={t('edit')}
              />
            </Tooltip>
            <Tooltip title={t('delete')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                onClick={() => onDelete(peer)}
                aria-label={t('delete')}
              />
            </Tooltip>
          </Space>
        ),
      },
    ],
    [t, onEdit, onDelete, onProbe, onToggleEnable],
  );

  return (
    <Card
      size="small"
      title={t('pages.peers.title')}
      extra={
        <Button type="primary" icon={<PlusOutlined />} onClick={onAdd}>
          {t('pages.peers.add')}
        </Button>
      }
    >
      <Table<PeerRecord>
        rowKey="id"
        size="small"
        columns={columns}
        dataSource={peers}
        loading={loading}
        pagination={false}
        scroll={isMobile ? { x: 'max-content' } : undefined}
        locale={{ emptyText: t('pages.peers.empty') }}
      />
    </Card>
  );
}
