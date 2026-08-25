import { memo } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Typography } from 'antd';
import { GlobalOutlined, StopOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';

import type { SinkNodeData } from '@/lib/network/graph-elements';

// Where filtered traffic ends up when it is not forwarded to another panel.
function SinkNodeComponent({ data }: NodeProps) {
  const { t } = useTranslation();
  const sink = data as SinkNodeData;
  const isBlock = sink.kind === 'block';

  return (
    <div className={`sink-node${isBlock ? ' is-block' : ' is-direct'}`}>
      <Handle type="target" position={Position.Left} id="in" className="panel-node-handle" />
      {isBlock ? <StopOutlined /> : <GlobalOutlined />}
      <Typography.Text strong>
        {isBlock ? t('pages.network.sinkBlock') : t('pages.network.sinkDirect')}
      </Typography.Text>
    </div>
  );
}

export const SinkNode = memo(SinkNodeComponent);
export default SinkNode;
