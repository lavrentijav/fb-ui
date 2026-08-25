import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type Node,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import {
  Alert,
  Button,
  Card,
  ConfigProvider,
  Layout,
  Modal,
  Result,
  Space,
  Spin,
  message,
} from 'antd';
import { FilterOutlined, PlusOutlined, TagsOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { useNetworkGraph } from '@/api/queries/useNetworkGraph';
import { useFilters } from '@/api/queries/useFilters';
import { useNodeMutations } from '@/api/queries/useNodeMutations';
import { usePeerMutations } from '@/api/queries/usePeerMutations';
import { useNodesQuery, type NodeRecord } from '@/api/queries/useNodesQuery';
import { usePeersQuery, type PeerRecord } from '@/api/queries/usePeersQuery';
import type { FilterRule } from '@/schemas/filter';
import type { CascadeLink, GraphPanel } from '@/schemas/network';
import {
  ALL_INBOUNDS_HANDLE,
  buildGraphElements,
  type StoredPositions,
} from '@/lib/network/graph-elements';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import NodeFormModal from '@/pages/nodes/NodeFormModal';
import PeerFormModal from '@/pages/nodes/PeerFormModal';
import NodeRoleModal from '@/pages/nodes/NodeRoleModal';
import FilterRuleModal from '@/pages/filters/FilterRuleModal';
import FilterListModal from '@/pages/filters/FilterListModal';
import PanelNode from './PanelNode';
import FilterNode from './FilterNode';
import SinkNode from './SinkNode';
import NetworkInspector, { type Selection } from './NetworkInspector';
import './NetworkPage.css';

const POSITIONS_KEY = 'network-graph-positions';
const nodeTypes = { panel: PanelNode, filter: FilterNode, sink: SinkNode };

function loadPositions(): StoredPositions {
  try {
    const raw = window.localStorage.getItem(POSITIONS_KEY);
    return raw ? (JSON.parse(raw) as StoredPositions) : {};
  } catch {
    return {};
  }
}

function savePositions(nodes: Node[]): void {
  const out: StoredPositions = {};
  for (const node of nodes) out[node.id] = { x: node.position.x, y: node.position.y };
  try {
    window.localStorage.setItem(POSITIONS_KEY, JSON.stringify(out));
  } catch {
    // A full or blocked storage only costs the remembered layout.
  }
}

function selectionOf(elementId: string): Selection {
  const [kind, id] = elementId.split(':');
  if (kind === 'panel') return { kind: 'panel', id: Number(id) };
  if (kind === 'filter') return { kind: 'filter', id: Number(id) };
  if (kind === 'link') return { kind: 'link', id: Number(id) };
  // Both edges of a filter belong to the rule, so clicking either selects it.
  if (kind === 'rule-in' || kind === 'rule-out') return { kind: 'filter', id: Number(id) };
  return null;
}

function NetworkCanvas() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { isDark, antdThemeConfig } = useTheme();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { graph, loading, fetched, fetchError, refetch, addLink, removeLink, setLinkEnable } =
    useNetworkGraph();
  const { lists, createList, createRule, updateRule, removeRule, setRuleEnable } = useFilters();
  const nodeMutations = useNodeMutations();
  const peerMutations = usePeerMutations();
  const { nodes: nodeRecords } = useNodesQuery();
  const { peers: peerRecords } = usePeersQuery();

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [busy, setBusy] = useState(false);
  const [selection, setSelection] = useState<Selection>(null);

  const [nodeFormOpen, setNodeFormOpen] = useState(false);
  const [nodeFormMode, setNodeFormMode] = useState<'add' | 'edit'>('add');
  const [nodeFormRecord, setNodeFormRecord] = useState<NodeRecord | null>(null);
  const [peerFormOpen, setPeerFormOpen] = useState(false);
  const [peerFormMode, setPeerFormMode] = useState<'add' | 'edit'>('add');
  const [peerFormRecord, setPeerFormRecord] = useState<PeerRecord | null>(null);
  const [roleOpen, setRoleOpen] = useState(false);
  const [roleTarget, setRoleTarget] = useState<'node' | 'master'>('master');
  const [roleRecord, setRoleRecord] = useState<NodeRecord | PeerRecord | null>(null);
  const [ruleOpen, setRuleOpen] = useState(false);
  const [ruleMode, setRuleMode] = useState<'add' | 'edit'>('add');
  const [ruleRecord, setRuleRecord] = useState<FilterRule | null>(null);
  const [listOpen, setListOpen] = useState(false);

  useEffect(() => {
    const { nodes: nextNodes, edges: nextEdges } = buildGraphElements(graph, loadPositions());
    setNodes(nextNodes);
    setEdges(nextEdges);
  }, [graph, setNodes, setEdges]);

  const onConnect = useCallback(
    async (connection: Connection) => {
      if (connection.sourceHandle === ALL_INBOUNDS_HANDLE) {
        messageApi.error(t('pages.network.toasts.allInboundsHint'));
        return;
      }
      const sourceTag = connection.sourceHandle?.replace(/^out:/, '') ?? '';
      const targetInboundId = Number(connection.targetHandle?.replace(/^in:/, '') ?? '');
      if (!sourceTag || !targetInboundId) {
        messageApi.error(t('pages.network.toasts.dragHint'));
        return;
      }
      if (connection.source === connection.target) {
        messageApi.error(t('pages.network.toasts.samePanel'));
        return;
      }
      setBusy(true);
      try {
        const msg = await addLink({
          sourcePanelId: Number(connection.source.replace(/^panel:/, '')),
          sourceInboundTag: sourceTag,
          targetPanelId: Number(connection.target.replace(/^panel:/, '')),
          targetInboundId,
          enable: true,
        });
        if (msg?.success) messageApi.success(t('pages.network.toasts.linked'));
      } finally {
        setBusy(false);
      }
    },
    [addLink, messageApi, t],
  );

  const confirmDelete = useCallback(
    (title: string, content: string, onOk: () => Promise<void>) => {
      modal.confirm({
        title,
        content,
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk,
      });
    },
    [modal, t],
  );

  const recordFor = useCallback(
    (panel: GraphPanel) =>
      panel.role === 'master'
        ? (peerRecords.find((p) => p.id === panel.id) ?? null)
        : (nodeRecords.find((n) => n.id === panel.id) ?? null),
    [peerRecords, nodeRecords],
  );

  const actions = useMemo(
    () => ({
      editPanel: (panel: GraphPanel) => {
        const record = recordFor(panel);
        if (!record) {
          messageApi.error(t('pages.network.toasts.panelMissing'));
          return;
        }
        if (panel.role === 'master') {
          setPeerFormMode('edit');
          setPeerFormRecord(record as PeerRecord);
          setPeerFormOpen(true);
        } else {
          setNodeFormMode('edit');
          setNodeFormRecord(record as NodeRecord);
          setNodeFormOpen(true);
        }
      },
      changeRole: (panel: GraphPanel) => {
        const record = recordFor(panel);
        if (!record) {
          messageApi.error(t('pages.network.toasts.panelMissing'));
          return;
        }
        setRoleTarget(panel.role === 'master' ? 'node' : 'master');
        setRoleRecord(record);
        setRoleOpen(true);
      },
      togglePanel: async (panel: GraphPanel, next: boolean) => {
        const msg =
          panel.role === 'master'
            ? await peerMutations.setEnable(panel.id, next)
            : await nodeMutations.setEnable(panel.id, next);
        if (msg?.success) refetch();
      },
      probePanel: async (panel: GraphPanel) => {
        const msg =
          panel.role === 'master'
            ? await peerMutations.probe(panel.id)
            : await nodeMutations.probe(panel.id);
        if (msg?.success && msg.obj?.status === 'online') {
          messageApi.success(t('pages.nodes.connectionOk', { ms: msg.obj.latencyMs ?? 0 }));
        } else if (msg?.success) {
          messageApi.error(t('pages.nodes.toasts.probeFailed'));
        }
        refetch();
      },
      deletePanel: (panel: GraphPanel) =>
        confirmDelete(
          t('pages.nodes.deleteConfirmTitle', { name: panel.name ?? '' }),
          t('pages.nodes.deleteConfirmContent'),
          async () => {
            const msg =
              panel.role === 'master'
                ? await peerMutations.remove(panel.id)
                : await nodeMutations.remove(panel.id);
            if (msg?.success) {
              setSelection(null);
              refetch();
            }
          },
        ),
      openInbounds: () => navigate('/inbounds'),
      editFilter: (rule: FilterRule) => {
        setRuleMode('edit');
        setRuleRecord(rule);
        setRuleOpen(true);
      },
      toggleFilter: (rule: FilterRule, next: boolean) => setRuleEnable(rule.id, next),
      deleteFilter: (rule: FilterRule) =>
        confirmDelete(
          t('pages.filters.deleteRuleConfirmTitle', { name: rule.name }),
          '',
          async () => {
            const msg = await removeRule(rule.id);
            if (msg?.success) setSelection(null);
          },
        ),
      toggleLink: (link: CascadeLink, next: boolean) => setLinkEnable(link.id, next),
      deleteLink: (link: CascadeLink) =>
        confirmDelete(
          t('pages.network.unlinkConfirmTitle'),
          t('pages.network.unlinkConfirmContent'),
          async () => {
            const msg = await removeLink(link.id);
            if (msg?.success) {
              setSelection(null);
              messageApi.success(t('pages.network.toasts.unlinked'));
            }
          },
        ),
      // Pre-wires the new rule to the link the operator clicked, so the filter
      // lands on that exact edge instead of being described again by hand.
      filterLink: (link: CascadeLink) => {
        setRuleMode('add');
        setRuleRecord({
          id: 0,
          name: '',
          panelId: link.sourcePanelId,
          sourceInboundTags: [link.sourceInboundTag],
          listIds: [],
          action: 'cascade',
          cascadeLinkId: link.id,
          enable: true,
        });
        setRuleOpen(true);
      },
    }),
    [
      confirmDelete,
      messageApi,
      navigate,
      nodeMutations,
      peerMutations,
      recordFor,
      refetch,
      removeLink,
      removeRule,
      setLinkEnable,
      setRuleEnable,
      t,
    ],
  );

  const onAddFilter = useCallback(() => {
    setRuleMode('add');
    setRuleRecord(null);
    setRuleOpen(true);
  }, []);

  const saveRule = useCallback(
    async (payload: Partial<FilterRule>) => {
      const msg =
        ruleMode === 'edit' && ruleRecord?.id
          ? await updateRule(ruleRecord.id, payload)
          : await createRule(payload);
      if (msg?.success) refetch();
      return msg;
    },
    [ruleMode, ruleRecord, updateRule, createRule, refetch],
  );

  const savePanel = useCallback(
    async (payload: Partial<NodeRecord>) => {
      const msg =
        nodeFormMode === 'edit' && nodeFormRecord?.id
          ? await nodeMutations.update(nodeFormRecord.id, payload)
          : await nodeMutations.create(payload);
      if (msg?.success) refetch();
      return msg;
    },
    [nodeFormMode, nodeFormRecord, nodeMutations, refetch],
  );

  const saveMaster = useCallback(
    async (payload: Partial<PeerRecord>) => {
      const msg =
        peerFormMode === 'edit' && peerFormRecord?.id
          ? await peerMutations.update(peerFormRecord.id, payload)
          : await peerMutations.create(payload);
      if (msg?.success) refetch();
      return msg;
    },
    [peerFormMode, peerFormRecord, peerMutations, refetch],
  );

  const onNodeDragStop = useCallback(() => savePositions(nodes), [nodes]);

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      {modalContextHolder}
      <Layout className={`network-page${isDark ? ' is-dark' : ''}`}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Spin spinning={!fetched || busy} delay={200} description={t('loading')} size="large">
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
                <Card
                  size="small"
                  title={t('pages.network.title')}
                  extra={
                    <Space wrap>
                      <Button
                        icon={<PlusOutlined />}
                        onClick={() => {
                          setNodeFormMode('add');
                          setNodeFormRecord(null);
                          setNodeFormOpen(true);
                        }}
                      >
                        {t('pages.nodes.addNode')}
                      </Button>
                      <Button
                        icon={<PlusOutlined />}
                        onClick={() => {
                          setPeerFormMode('add');
                          setPeerFormRecord(null);
                          setPeerFormOpen(true);
                        }}
                      >
                        {t('pages.peers.addPeer')}
                      </Button>
                      <Button icon={<FilterOutlined />} onClick={onAddFilter}>
                        {t('pages.filters.addRule')}
                      </Button>
                      <Button icon={<TagsOutlined />} onClick={() => setListOpen(true)}>
                        {t('pages.filters.addList')}
                      </Button>
                      <Button onClick={() => refetch()} loading={loading}>
                        {t('refresh')}
                      </Button>
                    </Space>
                  }
                >
                  <Alert
                    type="info"
                    showIcon
                    className="network-hint"
                    title={t('pages.network.hint')}
                  />
                  <div className="network-canvas">
                    <ReactFlow
                      nodes={nodes}
                      edges={edges}
                      nodeTypes={nodeTypes}
                      onNodesChange={onNodesChange}
                      onEdgesChange={onEdgesChange}
                      onConnect={onConnect}
                      onNodeClick={(_event, node) => setSelection(selectionOf(node.id))}
                      onEdgeClick={(_event, edge) => setSelection(selectionOf(edge.id))}
                      onPaneClick={() => setSelection(null)}
                      onNodeDragStop={onNodeDragStop}
                      colorMode={isDark ? 'dark' : 'light'}
                      fitView={graph.panels.length > 0}
                      proOptions={{ hideAttribution: false }}
                    >
                      <Background />
                      <Controls />
                      <MiniMap pannable zoomable />
                    </ReactFlow>
                  </div>
                </Card>
              )}
            </Spin>
          </Layout.Content>
        </Layout>

        <NetworkInspector
          selection={selection}
          panels={graph.panels}
          links={graph.links ?? []}
          filters={graph.filters ?? []}
          lists={lists}
          actions={actions}
          onClose={() => setSelection(null)}
        />

        <FilterRuleModal
          open={ruleOpen}
          mode={ruleMode}
          rule={ruleRecord}
          panels={graph.panels}
          links={graph.links ?? []}
          lists={lists}
          save={saveRule}
          onOpenChange={setRuleOpen}
        />

        <FilterListModal
          open={listOpen}
          mode="add"
          list={null}
          save={(payload) => createList(payload)}
          onOpenChange={setListOpen}
        />

        <NodeFormModal
          open={nodeFormOpen}
          mode={nodeFormMode}
          node={nodeFormRecord}
          testConnection={nodeMutations.testConnection}
          fetchFingerprint={nodeMutations.fetchFingerprint}
          fetchInbounds={nodeMutations.fetchInbounds}
          save={savePanel}
          onOpenChange={setNodeFormOpen}
        />

        <PeerFormModal
          open={peerFormOpen}
          mode={peerFormMode}
          peer={peerFormRecord}
          save={saveMaster}
          onOpenChange={setPeerFormOpen}
        />

        <NodeRoleModal
          open={roleOpen}
          target={roleTarget}
          record={roleRecord}
          save={async (id, payload) => {
            const msg = await nodeMutations.setRole(id, payload);
            if (msg?.success) refetch();
            return msg;
          }}
          onOpenChange={setRoleOpen}
        />
      </Layout>
    </ConfigProvider>
  );
}

export default function NetworkPage() {
  return (
    <ReactFlowProvider>
      <NetworkCanvas />
    </ReactFlowProvider>
  );
}
