import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  addEdge,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type Node,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { Alert, Button, Card, ConfigProvider, Layout, Modal, Result, Spin, message } from 'antd';

import { useTheme } from '@/hooks/useTheme';
import { useNetworkGraph, type CascadeLink } from '@/api/queries/useNetworkGraph';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import PanelNode, { type PanelNodeData } from './PanelNode';
import './NetworkPage.css';

const POSITIONS_KEY = 'network-graph-positions';
const nodeTypes = { panel: PanelNode };

type StoredPositions = Record<string, { x: number; y: number }>;

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

function NetworkCanvas() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [modal, modalContextHolder] = Modal.useModal();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { graph, loading, fetched, fetchError, refetch, addLink, removeLink } = useNetworkGraph();
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [busy, setBusy] = useState(false);

  // Rebuild the canvas from the server graph, keeping whatever layout the
  // operator dragged into place.
  useEffect(() => {
    const stored = loadPositions();
    const nextNodes: Node[] = graph.panels.map((panel, index) => ({
      id: String(panel.id),
      type: 'panel',
      position: stored[String(panel.id)] ?? {
        x: (index % 3) * 320,
        y: Math.floor(index / 3) * 260,
      },
      data: {
        name: panel.name || `panel ${panel.id}`,
        role: panel.role ?? 'node',
        status: panel.status ?? 'unknown',
        address: panel.address ?? '',
        self: panel.self ?? false,
        enable: panel.enable ?? true,
        inbounds: panel.inbounds ?? [],
      } satisfies PanelNodeData,
    }));

    const nextEdges: Edge[] = (graph.links ?? []).map((link: CascadeLink) => ({
      id: String(link.id),
      source: String(link.sourcePanelId),
      target: String(link.targetPanelId),
      sourceHandle: `out:${link.sourceInboundTag}`,
      targetHandle: `in:${link.targetInboundId}`,
      animated: link.enable !== false,
      label: link.remark || undefined,
      className: link.enable === false ? 'cascade-edge is-paused' : 'cascade-edge',
    }));

    setNodes(nextNodes);
    setEdges(nextEdges);
  }, [graph, setNodes, setEdges]);

  const onConnect = useCallback(
    async (connection: Connection) => {
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
          sourcePanelId: Number(connection.source),
          sourceInboundTag: sourceTag,
          targetPanelId: Number(connection.target),
          targetInboundId,
          enable: true,
        });
        if (msg?.success) {
          setEdges((current) => addEdge({ ...connection, animated: true }, current));
          messageApi.success(t('pages.network.toasts.linked'));
        }
      } finally {
        setBusy(false);
      }
    },
    [addLink, messageApi, setEdges, t],
  );

  const onEdgeClick = useCallback(
    (_event: React.MouseEvent, edge: Edge) => {
      modal.confirm({
        title: t('pages.network.unlinkConfirmTitle'),
        content: t('pages.network.unlinkConfirmContent'),
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: async () => {
          const msg = await removeLink(Number(edge.id));
          if (msg?.success) messageApi.success(t('pages.network.toasts.unlinked'));
        },
      });
    },
    [modal, removeLink, messageApi, t],
  );

  const onNodeDragStop = useCallback(() => savePositions(nodes), [nodes]);

  const panelCount = useMemo(() => graph.panels.length, [graph.panels]);

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
                    <Button onClick={() => refetch()} loading={loading}>
                      {t('refresh')}
                    </Button>
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
                      onEdgeClick={onEdgeClick}
                      onNodeDragStop={onNodeDragStop}
                      colorMode={isDark ? 'dark' : 'light'}
                      fitView={panelCount > 0}
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
