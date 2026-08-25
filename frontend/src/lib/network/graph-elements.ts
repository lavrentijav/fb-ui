import type { Edge, Node } from '@xyflow/react';

import type { FilterList, FilterRule } from '@/schemas/filter';
import type { CascadeLink, GraphInbound, NetworkGraph } from '@/schemas/network';

export const BLOCK_SINK_ID = 'sink:block';
export const DIRECT_SINK_ID = 'sink:direct';
// Source handle standing for "every inbound on this panel", which is what a
// filter chain with no explicit tags matches.
export const ALL_INBOUNDS_HANDLE = 'out:*';

export type StoredPositions = Record<string, { x: number; y: number }>;
export type Point = { x: number; y: number };

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

export interface SourceNodeData extends Record<string, unknown> {
  panelId: number;
  panelName: string;
  tags: string[];
  draft: boolean;
}

export interface FilterNodeData extends Record<string, unknown> {
  ruleId: number;
  name: string;
  action: string;
  enable: boolean;
  applied: boolean;
  listNames: string[];
  layer: number;
  draft: boolean;
}

export interface SinkNodeData extends Record<string, unknown> {
  kind: 'block' | 'direct';
}

/** A node pulled off the palette but not yet backed by a stored rule. */
export interface DraftSource {
  kind: 'source';
  id: string;
  panelId: number;
  tags: string[];
  position: Point;
}

export interface DraftFilter {
  kind: 'filter';
  id: string;
  position: Point;
  panelId?: number;
  tags?: string[];
  action?: string;
}

export type DraftNode = DraftSource | DraftFilter;

export interface BuildOptions {
  positions?: StoredPositions;
  drafts?: DraftNode[];
  sinks?: { block?: boolean; direct?: boolean };
}

export function panelNodeId(panelId: number): string {
  return `panel:${panelId}`;
}

export function filterNodeId(ruleId: number): string {
  return `filter:${ruleId}`;
}

/** Rules sharing a panel and a set of inbounds are one chain of layers. */
export function scopeKey(panelId: number, tags: string[] | null | undefined): string {
  return `${panelId}|${[...(tags ?? [])].sort().join(',')}`;
}

export function sourceNodeId(key: string): string {
  return `source:${key}`;
}

export function elementRef(elementId: string): { kind: string; rest: string } {
  const at = elementId.indexOf(':');
  return at < 0
    ? { kind: elementId, rest: '' }
    : { kind: elementId.slice(0, at), rest: elementId.slice(at + 1) };
}

function edgeClass(base: string, enabled: boolean): string {
  return enabled ? base : `${base} is-paused`;
}

/**
 * Projects the server graph onto React Flow elements.
 *
 * Filter rules that share a panel and a set of inbounds form one chain: the
 * inbound source feeds the first layer, each layer passes what it did not match
 * to the next, and sends what it did match to its own action — a cascade
 * target, the block terminal or the direct one. Xray evaluates routing rules in
 * order and takes the first match, so a row of layers is what the config
 * actually does, not a drawing convention.
 */
export function buildGraphElements(
  graph: NetworkGraph,
  options: BuildOptions = {},
): { nodes: Node[]; edges: Edge[] } {
  const positions = options.positions ?? {};
  const drafts = options.drafts ?? [];
  const panels = graph.panels ?? [];
  const links: CascadeLink[] = graph.links ?? [];
  const rules: FilterRule[] = graph.filters ?? [];
  const lists: FilterList[] = graph.lists ?? [];

  const listNameById = new Map<number, string>(lists.map((list) => [list.id, list.name]));
  const linkById = new Map<number, CascadeLink>(links.map((link) => [link.id, link]));
  const panelById = new Map(panels.map((panel) => [panel.id, panel]));

  const nodes: Node[] = panels.map((panel, index) => ({
    id: panelNodeId(panel.id),
    type: 'panel',
    position: positions[panelNodeId(panel.id)] ?? {
      x: 0,
      y: index * 300,
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
  let usesBlock = options.sinks?.block ?? false;
  let usesDirect = options.sinks?.direct ?? false;

  // Group the stored rules into chains, keeping the order the server sent —
  // it is already sort_order, which is the order Xray will evaluate them in.
  const chains = new Map<string, FilterRule[]>();
  for (const rule of rules) {
    if (!panelById.has(rule.panelId ?? -1)) continue;
    const key = scopeKey(rule.panelId ?? 0, rule.sourceInboundTags);
    const chain = chains.get(key);
    if (chain) chain.push(rule);
    else chains.set(key, [rule]);
  }

  for (const draft of drafts) {
    if (draft.kind !== 'source') continue;
    const key = scopeKey(draft.panelId, draft.tags);
    if (!chains.has(key)) chains.set(key, []);
  }

  let row = 0;
  for (const [key, chain] of chains) {
    const [panelPart, tagPart] = key.split('|');
    const panelId = Number(panelPart);
    const tags = tagPart ? tagPart.split(',') : [];
    const sourceId = sourceNodeId(key);
    const draftSource = drafts.find(
      (d): d is DraftSource => d.kind === 'source' && scopeKey(d.panelId, d.tags) === key,
    );

    nodes.push({
      id: sourceId,
      type: 'source',
      position: positions[sourceId] ?? draftSource?.position ?? { x: 380, y: row * 220 },
      data: {
        panelId,
        panelName: panelById.get(panelId)?.name || `panel ${panelId}`,
        tags,
        draft: chain.length === 0,
      } satisfies SourceNodeData,
    });

    const handles = tags.length > 0 ? tags.map((tag) => `out:${tag}`) : [ALL_INBOUNDS_HANDLE];
    for (const handle of handles) {
      edges.push({
        id: `feed:${key}:${handle}`,
        source: panelNodeId(panelId),
        sourceHandle: handle,
        target: sourceId,
        targetHandle: 'in',
        className: 'feed-edge',
      });
    }

    chain.forEach((rule, layer) => {
      const id = filterNodeId(rule.id);
      const action = rule.action ?? 'block';
      const enabled = rule.enable !== false;

      nodes.push({
        id,
        type: 'filter',
        position: positions[id] ?? { x: 660 + layer * 280, y: row * 220 },
        data: {
          ruleId: rule.id,
          name: rule.name,
          action,
          enable: enabled,
          applied: (rule.applied ?? 0) > 0,
          listNames: (rule.listIds ?? []).map((listId) => listNameById.get(listId) ?? `#${listId}`),
          layer: layer + 1,
          draft: false,
        } satisfies FilterNodeData,
      });

      const previous = layer === 0 ? sourceId : filterNodeId(chain[layer - 1].id);
      edges.push({
        id: `pass:${rule.id}`,
        source: previous,
        sourceHandle: layer === 0 ? 'out' : 'pass',
        target: id,
        targetHandle: 'in',
        className: edgeClass('pass-edge', enabled),
      });

      if (action === 'cascade') {
        const link = rule.cascadeLinkId ? linkById.get(rule.cascadeLinkId) : undefined;
        if (!link) return;
        governedLinks.add(link.id);
        edges.push({
          id: `match:${rule.id}`,
          source: id,
          sourceHandle: 'match',
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
        id: `match:${rule.id}`,
        source: id,
        sourceHandle: 'match',
        target: sink,
        targetHandle: 'in',
        className: edgeClass(action === 'direct' ? 'direct-edge' : 'block-edge', enabled),
      });
    });

    row += 1;
  }

  for (const draft of drafts) {
    if (draft.kind !== 'filter') continue;
    nodes.push({
      id: draft.id,
      type: 'filter',
      position: positions[draft.id] ?? draft.position,
      data: {
        ruleId: 0,
        name: '',
        action: draft.action ?? 'block',
        enable: true,
        applied: false,
        listNames: [],
        layer: 0,
        draft: true,
      } satisfies FilterNodeData,
    });
  }

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

  if (usesBlock) {
    nodes.push({
      id: BLOCK_SINK_ID,
      type: 'sink',
      position: positions[BLOCK_SINK_ID] ?? { x: 1320, y: 0 },
      data: { kind: 'block' } satisfies SinkNodeData,
    });
  }
  if (usesDirect) {
    nodes.push({
      id: DIRECT_SINK_ID,
      type: 'sink',
      position: positions[DIRECT_SINK_ID] ?? { x: 1320, y: 140 },
      data: { kind: 'direct' } satisfies SinkNodeData,
    });
  }

  return { nodes, edges };
}
