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

// One layer of a filter chain. What it matches leaves through the upper output
// towards its action; what it does not match passes down to the next layer.
function FilterNodeComponent({ data, selected }: NodeProps) {
  const { t } = useTranslation();
  const rule = data as FilterNodeData;
  const classes = ['filter-node'];
  if (!rule.enable) classes.push('is-disabled');
  if (selected) classes.push('is-selected');
  if (rule.draft) classes.push('is-draft');

  return (
    <div className={classes.join(' ')}>
      <Handle type="target" position={Position.Left} id="in" className="panel-node-handle" />
      <div className="filter-node-head">
        <FilterOutlined />
        <Typography.Text strong ellipsis className="filter-node-title">
          {rule.draft ? t('pages.network.draftFilter') : rule.name}
        </Typography.Text>
        {rule.layer > 0 && <Tag className="filter-node-layer">L{rule.layer}</Tag>}
      </div>

      <div className="filter-node-lists">
        {rule.listNames.length === 0 ? (
          <Typography.Text type="secondary" className="filter-node-empty">
            {rule.draft ? t('pages.network.draftFilterHint') : t('pages.filters.noLists')}
          </Typography.Text>
        ) : (
          rule.listNames.map((name) => (
            <Tag key={name} className="filter-node-list">
              {name}
            </Tag>
          ))
        )}
      </div>

      <div className="filter-node-outputs">
        <Tooltip title={t('pages.network.matchHint')}>
          <span className="filter-node-output">
            <Tag color={actionColor(rule.action)}>{t(`pages.filters.action_${rule.action}`)}</Tag>
          </span>
        </Tooltip>
        <Tooltip title={t('pages.network.passHint')}>
          <span className="filter-node-output is-pass">{t('pages.network.pass')}</span>
        </Tooltip>
      </div>
      {!rule.draft && !rule.applied && (
        <Tag color="orange" className="filter-node-pending">
          {t('pages.network.pending')}
        </Tag>
      )}

      <Handle
        type="source"
        position={Position.Right}
        id="match"
        className="panel-node-handle filter-handle-match"
      />
      <Handle
        type="source"
        position={Position.Right}
        id="pass"
        className="panel-node-handle filter-handle-pass"
      />
    </div>
  );
}

export const FilterNode = memo(FilterNodeComponent);
export default FilterNode;
