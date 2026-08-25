import { memo } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tag, Tooltip, Typography } from 'antd';
import { FilterOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';

import type { FilterNodeData } from '@/lib/network/graph-elements';

function actionColor(action: string): string {
  if (action === 'block') return 'red';
  if (action === 'cascade') return 'purple';
  return 'blue';
}

// A filter rule as a vertex: traffic arrives from the inbounds it matches on
// the left and leaves on the right towards whatever its action says.
function FilterNodeComponent({ data, selected }: NodeProps) {
  const { t } = useTranslation();
  const rule = data as FilterNodeData;

  return (
    <div
      className={`filter-node${rule.enable ? '' : ' is-disabled'}${selected ? ' is-selected' : ''}`}
    >
      <Handle type="target" position={Position.Left} id="in" className="panel-node-handle" />
      <div className="filter-node-head">
        <FilterOutlined />
        <Typography.Text strong ellipsis className="filter-node-title">
          {rule.name}
        </Typography.Text>
        <Tag color={actionColor(rule.action)}>{t(`pages.filters.action_${rule.action}`)}</Tag>
      </div>

      <div className="filter-node-lists">
        {rule.listNames.length === 0 ? (
          <Typography.Text type="secondary" className="filter-node-empty">
            {t('pages.filters.noLists')}
          </Typography.Text>
        ) : (
          rule.listNames.map((name) => (
            <Tag key={name} className="filter-node-list">
              {name}
            </Tag>
          ))
        )}
      </div>

      <Tooltip
        title={rule.scope.length > 0 ? rule.scope.join(', ') : t('pages.filters.allInbounds')}
      >
        <Typography.Text type="secondary" className="filter-node-scope" ellipsis>
          {rule.scope.length > 0 ? rule.scope.join(', ') : t('pages.filters.allInbounds')}
        </Typography.Text>
      </Tooltip>
      {!rule.applied && (
        <Tag color="orange" className="filter-node-pending">
          {t('pages.network.pending')}
        </Tag>
      )}

      <Handle type="source" position={Position.Right} id="out" className="panel-node-handle" />
    </div>
  );
}

export const FilterNode = memo(FilterNodeComponent);
export default FilterNode;
