import { memo } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Badge, Tag, Tooltip, Typography } from 'antd';
import { useTranslation } from 'react-i18next';

import { ALL_INBOUNDS_HANDLE, type PanelNodeData } from '@/lib/network/graph-elements';

export type { PanelNodeData };

function statusColor(status: string): string {
  if (status === 'online') return 'var(--ant-color-success)';
  if (status === 'offline') return 'var(--ant-color-error)';
  return 'var(--ant-color-text-quaternary)';
}

// One panel on the canvas. Every inbound is a port: drag from its right handle
// (traffic leaving that inbound) onto another panel's left handle (the inbound
// that traffic should be forwarded to) to draw a cascade.
function PanelNodeComponent({ data, selected }: NodeProps) {
  const { t } = useTranslation();
  const panel = data as PanelNodeData;

  return (
    <div
      className={`panel-node${panel.enable ? '' : ' is-disabled'}${selected ? ' is-selected' : ''}`}
    >
      <div className="panel-node-head">
        <Badge color={statusColor(panel.status)} />
        <Typography.Text strong ellipsis className="panel-node-title">
          {panel.name}
        </Typography.Text>
        {panel.self ? (
          <Tag color="blue">{t('pages.network.thisPanel')}</Tag>
        ) : (
          <Tag color={panel.role === 'master' ? 'purple' : 'default'}>
            {panel.role === 'master' ? t('pages.network.roleMaster') : t('pages.network.roleNode')}
          </Tag>
        )}
      </div>
      {panel.address && (
        <Typography.Text type="secondary" className="panel-node-address" ellipsis>
          {panel.address}
        </Typography.Text>
      )}

      <div className="panel-node-ports">
        {panel.inbounds.length === 0 && (
          <Typography.Text type="secondary" className="panel-node-empty">
            {t('pages.network.noInbounds')}
          </Typography.Text>
        )}
        {panel.inbounds.map((inbound) => (
          <div
            key={inbound.id}
            className={`panel-node-port${inbound.enable ? '' : ' is-disabled'}`}
          >
            <Handle
              type="target"
              position={Position.Left}
              id={`in:${inbound.id}`}
              className="panel-node-handle"
            />
            <Tooltip title={`${inbound.protocol ?? ''} · :${inbound.port ?? 0} · ${inbound.tag}`}>
              <span className="panel-node-port-label">{inbound.remark || inbound.tag}</span>
            </Tooltip>
            <span className="panel-node-port-meta">:{inbound.port ?? 0}</span>
            <Handle
              type="source"
              position={Position.Right}
              id={`out:${inbound.tag}`}
              className="panel-node-handle"
            />
          </div>
        ))}
      </div>

      <div className="panel-node-all">
        <span className="panel-node-port-label">{t('pages.network.allInbounds')}</span>
        <Handle
          type="source"
          position={Position.Right}
          id={ALL_INBOUNDS_HANDLE}
          className="panel-node-handle"
        />
      </div>
    </div>
  );
}

export const PanelNode = memo(PanelNodeComponent);
export default PanelNode;
