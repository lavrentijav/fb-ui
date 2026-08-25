import type { Edge, Node } from '@xyflow/react';

import type { FilterList, FilterRule } from '@/schemas/filter';
import type { CascadeLink, GraphInbound, NetworkGraph } from '@/schemas/network';

export const BLOCK_SINK_ID = 'sink:block';
export const DIRECT_SINK_ID = 'sink:direct';
// Source handle standing for "every inbound on this panel", which is what a
// filter rule with no explicit tags matches.
export const ALL_INBOUNDS_HANDLE = 'out:*';

export type StoredPositions = Record<string, { x: number; y: number }>;

export interface PanelNodeData extends Record<string, unknown> {
  panelId: number;
  name: string;
  role: string;
  status: string;
  address: string;
  self: boolean;
  enable: boolean;
  inbounds: GraphInbound[];
}

export interface FilterNodeData extends Record<string, unknown> {
  ruleId: number;
  name: string;
  action: string;
  enable: boolean;
  applied: boolean;
  listNames: string[];
  scope: string[];
}

export interface SinkNodeData extends Record<string, unknown> {
  kind: 'block' | 'direct';
}

export function panelNodeId(panelId: number): string {
  return `panel:${panelId}`;
}

export function filterNodeId(ruleId: number): string {
  return `filter:${ruleId}`;
}

export function panelIdFromNode(nodeId: string): number | null {
  const match = /^panel:(-?\d+)$/.exec(nodeId);
  return match ? Number(match[1]) : null;
}

function sinkNode(
  id: string,
  kind: 'block' | 'direct',
  positions: StoredPositions,
  y: number,
): Node {
  return {
    id,
    type: 'sink',
    position: positions[id] ?? { x: 900, y },
    data: { kind } satisfies SinkNodeData,
  };
}

function edgeClass(base: string, enabled: boolean): string {
  return enabled ? base : `${base} is-paused`;
}

/**
 * Projects the server graph onto React Flow elements. A filter rule is a vertex
 * of its own: traffic reaches it from the inbounds it matches and leaves it
 * towards a cascade target, the block sink or the direct sink — so a link that
 * a filter governs is drawn *through* that filter, never as a straight edge
 * that would claim everything on it is forwarded.
 */
export function buildGraphElements(
  graph: NetworkGraph,
  positions: StoredPositions = {},
): { nodes: Node[]; edges: Edge[] } {
  const panels = graph.panels ?? [];
  const links: CascadeLink[] = graph.links ?? [];
  const rules: FilterRule[] = graph.filters ?? [];
  const lists: FilterList[] = graph.lists ?? [];

  const listNameById = new Map<number, string>(lists.map((list) => [list.id, list.name]));
  const linkById = new Map<number, CascadeLink>(links.map((link) => [link.id, link]));
  const knownPanels = new Set(panels.map((panel) => panel.id));

  const nodes: Node[] = panels.map((panel, index) => ({
    id: panelNodeId(panel.id),
    type: 'panel',
    position: positions[panelNodeId(panel.id)] ?? {
      x: (index % 3) * 340,
      y: Math.floor(index / 3) * 280,
    },
    data: {
      panelId: panel.id,
      name: panel.name || `panel ${panel.id}`,
      role: panel.role ?? 'node',
      status: panel.status ?? 'unknown',
      address: panel.address ?? '',
      self: panel.self ?? false,
      enable: panel.enable ?? true,
      inbounds: panel.inbounds ?? [],
    } satisfies PanelNodeData,
  }));

  const edges: Edge[] = [];
  const governedLinks = new Set<number>();
  let usesBlock = false;
  let usesDirect = false;

  rules.forEach((rule, index) => {
    if (!knownPanels.has(rule.panelId ?? -1)) return;
    const id = filterNodeId(rule.id);
    const action = rule.action ?? 'block';
    const enabled = rule.enable !== false;
    const scope = rule.sourceInboundTags ?? [];

    nodes.push({
      id,
      type: 'filter',
      position: positions[id] ?? { x: 560, y: index * 140 },
      data: {
        ruleId: rule.id,
        name: rule.name,
        action,
        enable: enabled,
        applied: (rule.applied ?? 0) > 0,
        listNames: (rule.listIds ?? []).map((listId) => listNameById.get(listId) ?? `#${listId}`),
        scope,
      } satisfies FilterNodeData,
    });

    const sourceHandles =
      scope.length > 0 ? scope.map((tag) => `out:${tag}`) : [ALL_INBOUNDS_HANDLE];
    for (const handle of sourceHandles) {
      edges.push({
        id: `rule-in:${rule.id}:${handle}`,
        source: panelNodeId(rule.panelId ?? 0),
        sourceHandle: handle,
        target: id,
        targetHandle: 'in',
        className: edgeClass('filter-edge', enabled),
      });
    }

    if (action === 'cascade') {
      const link = rule.cascadeLinkId ? linkById.get(rule.cascadeLinkId) : undefined;
      if (!link) return;
      governedLinks.add(link.id);
      edges.push({
        id: `rule-out:${rule.id}`,
        source: id,
        sourceHandle: 'out',
        target: panelNodeId(link.targetPanelId),
        targetHandle: `in:${link.targetInboundId}`,
        animated: enabled,
        className: edgeClass('cascade-edge', enabled),
      });
      return;
    }

    const sink = action === 'direct' ? DIRECT_SINK_ID : BLOCK_SINK_ID;
    if (sink === DIRECT_SINK_ID) usesDirect = true;
    else usesBlock = true;
    edges.push({
      id: `rule-out:${rule.id}`,
      source: id,
      sourceHandle: 'out',
      target: sink,
      className: edgeClass(action === 'direct' ? 'direct-edge' : 'block-edge', enabled),
    });
  });

  for (const link of links) {
    if (governedLinks.has(link.id)) continue;
    const enabled = link.enable !== false;
    edges.push({
      id: `link:${link.id}`,
      source: panelNodeId(link.sourcePanelId),
      sourceHandle: `out:${link.sourceInboundTag}`,
      target: panelNodeId(link.targetPanelId),
      targetHandle: `in:${link.targetInboundId}`,
      animated: enabled,
      label: link.remark || undefined,
      className: edgeClass('cascade-edge', enabled),
    });
  }

  if (usesBlock) nodes.push(sinkNode(BLOCK_SINK_ID, 'block', positions, 0));
  if (usesDirect) nodes.push(sinkNode(DIRECT_SINK_ID, 'direct', positions, 160));

  return { nodes, edges };
}
