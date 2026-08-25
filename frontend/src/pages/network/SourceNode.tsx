import { memo } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tag, Typography } from 'antd';
import { ImportOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';

import type { SourceNodeData } from '@/lib/network/graph-elements';

// The head of a filter chain: the inbounds of one panel whose traffic the
// layers behind it are applied to.
function SourceNodeComponent({ data, selected }: NodeProps) {
  const { t } = useTranslation();
  const source = data as SourceNodeData;

  return (
    <div
      className={`source-node${selected ? ' is-selected' : ''}${source.draft ? ' is-draft' : ''}`}
    >
      <Handle type="target" position={Position.Left} id="in" className="panel-node-handle" />
      <div className="source-node-head">
        <ImportOutlined />
        <Typography.Text strong ellipsis>
          {source.panelName}
        </Typography.Text>
      </div>
      <div className="source-node-tags">
        {source.tags.length === 0 ? (
          <Tag>{t('pages.network.allInbounds')}</Tag>
        ) : (
          source.tags.map((tag) => <Tag key={tag}>{tag}</Tag>)
        )}
      </div>
      <Handle type="source" position={Position.Right} id="out" className="panel-node-handle" />
    </div>
  );
}

export const SourceNode = memo(SourceNodeComponent);
export default SourceNode;
