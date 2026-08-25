import { useTranslation } from 'react-i18next';
import { Button, Descriptions, Drawer, Empty, Space, Switch, Tag, Typography } from 'antd';
import {
  DeleteOutlined,
  EditOutlined,
  FilterOutlined,
  SwapOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';

import type { FilterList, FilterRule } from '@/schemas/filter';
import type { CascadeLink, GraphPanel } from '@/schemas/network';

export type Selection =
  | { kind: 'panel'; id: number }
  | { kind: 'filter'; id: number }
  | { kind: 'link'; id: number }
  | null;

export interface InspectorActions {
  editPanel: (panel: GraphPanel) => void;
  changeRole: (panel: GraphPanel) => void;
  togglePanel: (panel: GraphPanel, next: boolean) => void;
  probePanel: (panel: GraphPanel) => void;
  deletePanel: (panel: GraphPanel) => void;
  openInbounds: (panel: GraphPanel) => void;
  editFilter: (rule: FilterRule) => void;
  toggleFilter: (rule: FilterRule, next: boolean) => void;
  deleteFilter: (rule: FilterRule) => void;
  toggleLink: (link: CascadeLink, next: boolean) => void;
  deleteLink: (link: CascadeLink) => void;
  filterLink: (link: CascadeLink) => void;
}

interface NetworkInspectorProps {
  selection: Selection;
  panels: GraphPanel[];
  links: CascadeLink[];
  filters: FilterRule[];
  lists: FilterList[];
  actions: InspectorActions;
  onClose: () => void;
}

// The canvas shows the shape of the network; this is where a selected piece of
// it is actually operated on.
export default function NetworkInspector({
  selection,
  panels,
  links,
  filters,
  lists,
  actions,
  onClose,
}: NetworkInspectorProps) {
  const { t } = useTranslation();
  const panel = selection?.kind === 'panel' ? panels.find((p) => p.id === selection.id) : undefined;
  const rule =
    selection?.kind === 'filter' ? filters.find((f) => f.id === selection.id) : undefined;
  const link = selection?.kind === 'link' ? links.find((l) => l.id === selection.id) : undefined;
  const panelName = (id: number) => panels.find((p) => p.id === id)?.name || `panel ${id}`;

  return (
    <Drawer
      open={selection !== null}
      onClose={onClose}
      mask={false}
      width={360}
      title={t('pages.network.inspectorTitle')}
    >
      {panel && (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t('pages.filters.name')}>
              {panel.name || `panel ${panel.id}`}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.nodes.status')}>
              {panel.status ?? 'unknown'}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.nodes.address')}>
              {panel.address || '—'}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.network.roleLabel')}>
              <Tag color={panel.role === 'master' ? 'purple' : 'default'}>
                {panel.role === 'master'
                  ? t('pages.network.roleMaster')
                  : t('pages.network.roleNode')}
              </Tag>
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.network.inboundCount')}>
              {(panel.inbounds ?? []).length}
            </Descriptions.Item>
          </Descriptions>

          {panel.self ? (
            <Typography.Text type="secondary">{t('pages.network.selfHint')}</Typography.Text>
          ) : (
            <>
              <Space>
                <Typography.Text>{t('enable')}</Typography.Text>
                <Switch
                  checked={panel.enable !== false}
                  onChange={(next) => actions.togglePanel(panel, next)}
                />
              </Space>
              <Space wrap>
                <Button icon={<EditOutlined />} onClick={() => actions.editPanel(panel)}>
                  {t('edit')}
                </Button>
                <Button icon={<SwapOutlined />} onClick={() => actions.changeRole(panel)}>
                  {panel.role === 'master'
                    ? t('pages.nodes.role.makeNode')
                    : t('pages.nodes.role.makeMaster')}
                </Button>
                <Button icon={<ThunderboltOutlined />} onClick={() => actions.probePanel(panel)}>
                  {t('pages.nodes.probe')}
                </Button>
                <Button danger icon={<DeleteOutlined />} onClick={() => actions.deletePanel(panel)}>
                  {t('delete')}
                </Button>
              </Space>
            </>
          )}
          <Button block onClick={() => actions.openInbounds(panel)}>
            {t('pages.network.manageInbounds')}
          </Button>
        </Space>
      )}

      {rule && (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t('pages.filters.name')}>{rule.name}</Descriptions.Item>
            <Descriptions.Item label={t('pages.filters.rulePanel')}>
              {panelName(rule.panelId ?? 0)}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.filters.ruleAction')}>
              <Tag
                color={
                  rule.action === 'block' ? 'red' : rule.action === 'cascade' ? 'purple' : 'blue'
                }
              >
                {t(`pages.filters.action_${rule.action ?? 'block'}`)}
              </Tag>
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.filters.ruleLists')}>
              <Space size={4} wrap>
                {(rule.listIds ?? []).map((id) => (
                  <Tag key={id}>{lists.find((l) => l.id === id)?.name ?? `#${id}`}</Tag>
                ))}
              </Space>
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.filters.ruleScope')}>
              {(rule.sourceInboundTags ?? []).length === 0
                ? t('pages.filters.allInbounds')
                : (rule.sourceInboundTags ?? []).join(', ')}
            </Descriptions.Item>
          </Descriptions>
          <Space>
            <Typography.Text>{t('enable')}</Typography.Text>
            <Switch
              checked={rule.enable !== false}
              onChange={(next) => actions.toggleFilter(rule, next)}
            />
          </Space>
          <Space wrap>
            <Button icon={<EditOutlined />} onClick={() => actions.editFilter(rule)}>
              {t('edit')}
            </Button>
            <Button danger icon={<DeleteOutlined />} onClick={() => actions.deleteFilter(rule)}>
              {t('delete')}
            </Button>
          </Space>
        </Space>
      )}

      {link && (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t('pages.network.linkSource')}>
              {panelName(link.sourcePanelId)} · {link.sourceInboundTag}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.network.linkTarget')}>
              {panelName(link.targetPanelId)} · #{link.targetInboundId}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.filters.remark')}>
              {link.remark || '—'}
            </Descriptions.Item>
            <Descriptions.Item label={t('pages.network.applied')}>
              {(link.applied ?? 0) > 0 ? t('pages.network.appliedYes') : t('pages.network.pending')}
            </Descriptions.Item>
          </Descriptions>
          <Space>
            <Typography.Text>{t('enable')}</Typography.Text>
            <Switch
              checked={link.enable !== false}
              onChange={(next) => actions.toggleLink(link, next)}
            />
          </Space>
          <Space wrap>
            <Button icon={<FilterOutlined />} onClick={() => actions.filterLink(link)}>
              {t('pages.network.insertFilter')}
            </Button>
            <Button danger icon={<DeleteOutlined />} onClick={() => actions.deleteLink(link)}>
              {t('delete')}
            </Button>
          </Space>
        </Space>
      )}

      {selection !== null && !panel && !rule && !link && <Empty description={t('noData')} />}
    </Drawer>
  );
}
