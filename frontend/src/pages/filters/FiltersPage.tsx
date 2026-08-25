import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Button,
  Card,
  Col,
  ConfigProvider,
  Layout,
  Modal,
  Result,
  Row,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useFilters, type FilterList, type FilterRule } from '@/api/queries/useFilters';
import { useNetworkGraph } from '@/api/queries/useNetworkGraph';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import FilterListModal from './FilterListModal';
import FilterRuleModal from './FilterRuleModal';

export default function FiltersPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const { isMobile } = useMediaQuery();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const {
    lists,
    rules,
    loading,
    fetched,
    fetchError,
    refetch,
    createList,
    updateList,
    removeList,
    createRule,
    updateRule,
    removeRule,
    setRuleEnable,
  } = useFilters();
  // A rule points at a panel, its inbounds and a cascade link, so the form
  // needs the same topology the network editor draws.
  const { graph } = useNetworkGraph();

  const [formOpen, setFormOpen] = useState(false);
  const [formMode, setFormMode] = useState<'add' | 'edit'>('add');
  const [formList, setFormList] = useState<FilterList | null>(null);
  const [ruleOpen, setRuleOpen] = useState(false);
  const [ruleMode, setRuleMode] = useState<'add' | 'edit'>('add');
  const [ruleRecord, setRuleRecord] = useState<FilterRule | null>(null);

  const listNameById = useMemo(() => {
    const out = new Map<number, string>();
    for (const list of lists) out.set(list.id, list.name);
    return out;
  }, [lists]);

  const onAdd = useCallback(() => {
    setFormMode('add');
    setFormList(null);
    setFormOpen(true);
  }, []);

  const onEdit = useCallback((list: FilterList) => {
    setFormMode('edit');
    setFormList({ ...list });
    setFormOpen(true);
  }, []);

  const onSave = useCallback(
    async (payload: Partial<FilterList>) => {
      if (formMode === 'edit' && formList?.id) return updateList(formList.id, payload);
      return createList(payload);
    },
    [formMode, formList, updateList, createList],
  );

  const onDeleteList = useCallback(
    (list: FilterList) => {
      modal.confirm({
        title: t('pages.filters.deleteListConfirmTitle', { name: list.name }),
        content: t('pages.filters.deleteListConfirmContent'),
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: async () => {
          const msg = await removeList(list.id);
          if (msg?.success) messageApi.success(t('pages.filters.toasts.deletedList'));
        },
      });
    },
    [modal, t, removeList, messageApi],
  );

  const onAddRule = useCallback(() => {
    setRuleMode('add');
    setRuleRecord(null);
    setRuleOpen(true);
  }, []);

  const onEditRule = useCallback((rule: FilterRule) => {
    setRuleMode('edit');
    setRuleRecord({ ...rule });
    setRuleOpen(true);
  }, []);

  const onSaveRule = useCallback(
    async (payload: Partial<FilterRule>) => {
      if (ruleMode === 'edit' && ruleRecord?.id) return updateRule(ruleRecord.id, payload);
      return createRule(payload);
    },
    [ruleMode, ruleRecord, updateRule, createRule],
  );

  const onDeleteRule = useCallback(
    (rule: FilterRule) => {
      modal.confirm({
        title: t('pages.filters.deleteRuleConfirmTitle', { name: rule.name }),
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: async () => {
          const msg = await removeRule(rule.id);
          if (msg?.success) messageApi.success(t('pages.filters.toasts.deletedRule'));
        },
      });
    },
    [modal, t, removeRule, messageApi],
  );

  const listColumns = useMemo<ColumnsType<FilterList>>(
    () => [
      {
        title: t('pages.filters.name'),
        key: 'name',
        render: (_: unknown, list) => (
          <Space direction="vertical" size={0}>
            <Typography.Text strong>{list.name}</Typography.Text>
            {list.remark && <Typography.Text type="secondary">{list.remark}</Typography.Text>}
          </Space>
        ),
      },
      {
        title: t('pages.filters.kind'),
        key: 'kind',
        render: (_: unknown, list) => (
          <Tag color={list.kind === 'ip' ? 'geekblue' : 'green'}>
            {list.kind === 'ip' ? t('pages.filters.kindIp') : t('pages.filters.kindDomain')}
          </Tag>
        ),
      },
      {
        title: t('pages.filters.entriesCount'),
        key: 'entries',
        render: (_: unknown, list) => (
          <Tooltip title={(list.entries ?? []).slice(0, 20).join('\n')}>
            <span>{(list.entries ?? []).length}</span>
          </Tooltip>
        ),
      },
      {
        title: t('enable'),
        key: 'enable',
        render: (_: unknown, list) => (
          <Tag color={list.enable ? 'green' : 'default'}>
            {list.enable ? t('pages.filters.on') : t('pages.filters.off')}
          </Tag>
        ),
      },
      {
        title: t('pages.filters.actions'),
        key: 'actions',
        render: (_: unknown, list) => (
          <Space size={4}>
            <Tooltip title={t('edit')}>
              <Button
                size="small"
                icon={<EditOutlined />}
                onClick={() => onEdit(list)}
                aria-label={t('edit')}
              />
            </Tooltip>
            <Tooltip title={t('delete')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                onClick={() => onDeleteList(list)}
                aria-label={t('delete')}
              />
            </Tooltip>
          </Space>
        ),
      },
    ],
    [t, onEdit, onDeleteList],
  );

  const ruleColumns = useMemo<ColumnsType<FilterRule>>(
    () => [
      {
        title: t('pages.filters.name'),
        key: 'name',
        render: (_: unknown, rule) => <Typography.Text strong>{rule.name}</Typography.Text>,
      },
      {
        title: t('pages.filters.ruleAction'),
        key: 'action',
        render: (_: unknown, rule) => {
          const color =
            rule.action === 'block' ? 'red' : rule.action === 'cascade' ? 'purple' : 'blue';
          return <Tag color={color}>{t(`pages.filters.action_${rule.action ?? 'block'}`)}</Tag>;
        },
      },
      {
        title: t('pages.filters.ruleLists'),
        key: 'lists',
        render: (_: unknown, rule) => (
          <Space size={4} wrap>
            {(rule.listIds ?? []).map((id) => (
              <Tag key={id}>{listNameById.get(id) ?? id}</Tag>
            ))}
          </Space>
        ),
      },
      {
        title: t('pages.filters.ruleScope'),
        key: 'scope',
        render: (_: unknown, rule) =>
          (rule.sourceInboundTags ?? []).length === 0 ? (
            <Typography.Text type="secondary">{t('pages.filters.allInbounds')}</Typography.Text>
          ) : (
            <Space size={4} wrap>
              {(rule.sourceInboundTags ?? []).map((tag) => (
                <Tag key={tag}>{tag}</Tag>
              ))}
            </Space>
          ),
      },
      {
        title: t('enable'),
        key: 'enable',
        render: (_: unknown, rule) => (
          <Switch
            checked={!!rule.enable}
            onChange={(next) => setRuleEnable(rule.id, next)}
            aria-label={t('enable')}
          />
        ),
      },
      {
        title: t('pages.filters.actions'),
        key: 'actions',
        render: (_: unknown, rule) => (
          <Space size={4}>
            <Tooltip title={t('edit')}>
              <Button
                size="small"
                icon={<EditOutlined />}
                onClick={() => onEditRule(rule)}
                aria-label={t('edit')}
              />
            </Tooltip>
            <Tooltip title={t('delete')}>
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                onClick={() => onDeleteRule(rule)}
                aria-label={t('delete')}
              />
            </Tooltip>
          </Space>
        ),
      },
    ],
    [t, listNameById, setRuleEnable, onEditRule, onDeleteRule],
  );

  const pageClass = useMemo(() => {
    const classes = ['filters-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

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
                      title={t('pages.filters.listsTitle')}
                      extra={
                        <Button type="primary" icon={<PlusOutlined />} onClick={onAdd}>
                          {t('pages.filters.addList')}
                        </Button>
                      }
                    >
                      <Table<FilterList>
                        rowKey="id"
                        size="small"
                        columns={listColumns}
                        dataSource={lists}
                        loading={loading}
                        pagination={false}
                        scroll={isMobile ? { x: 'max-content' } : undefined}
                        locale={{ emptyText: t('pages.filters.noLists') }}
                      />
                    </Card>
                  </Col>
                  <Col span={24}>
                    <Card
                      size="small"
                      title={t('pages.filters.rulesTitle')}
                      extra={
                        <Button type="primary" icon={<PlusOutlined />} onClick={onAddRule}>
                          {t('pages.filters.addRule')}
                        </Button>
                      }
                    >
                      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
                        {t('pages.filters.rulesHint')}
                      </Typography.Paragraph>
                      <Table<FilterRule>
                        rowKey="id"
                        size="small"
                        columns={ruleColumns}
                        dataSource={rules}
                        loading={loading}
                        pagination={false}
                        scroll={isMobile ? { x: 'max-content' } : undefined}
                        locale={{ emptyText: t('pages.filters.noRules') }}
                      />
                    </Card>
                  </Col>
                </Row>
              )}
            </Spin>
          </Layout.Content>
        </Layout>

        <FilterRuleModal
          open={ruleOpen}
          mode={ruleMode}
          rule={ruleRecord}
          panels={graph.panels}
          links={graph.links ?? []}
          lists={lists}
          save={onSaveRule}
          onOpenChange={setRuleOpen}
        />

        <FilterListModal
          open={formOpen}
          mode={formMode}
          list={formList}
          save={onSave}
          onOpenChange={setFormOpen}
        />
      </Layout>
    </ConfigProvider>
  );
}
