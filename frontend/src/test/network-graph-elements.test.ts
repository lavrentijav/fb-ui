import { describe, expect, it } from 'vitest';

import {
  ALL_INBOUNDS_HANDLE,
  BLOCK_SINK_ID,
  DIRECT_SINK_ID,
  buildGraphElements,
  scopeKey,
  sourceNodeId,
} from '@/lib/network/graph-elements';
import type { FilterRule } from '@/schemas/filter';
import type { NetworkGraph } from '@/schemas/network';

function graph(overrides: Partial<NetworkGraph> = {}): NetworkGraph {
  return {
    panels: [
      {
        id: 0,
        name: 'entry',
        self: true,
        inbounds: [{ id: 1, tag: 'in-ru', port: 443, enable: true }],
      },
      {
        id: 2,
        name: 'exit',
        inbounds: [{ id: 9, tag: 'in-fi', port: 443, enable: true }],
      },
    ],
    links: [
      {
        id: 5,
        sourcePanelId: 0,
        sourceInboundTag: 'in-ru',
        targetPanelId: 2,
        targetInboundId: 9,
        enable: true,
      },
    ],
    ...overrides,
  };
}

function rule(over: Partial<FilterRule> & { id: number; name: string }): FilterRule {
  return {
    panelId: 0,
    sourceInboundTags: ['in-ru'],
    listIds: [7],
    action: 'block',
    enable: true,
    ...over,
  };
}

const CHAIN = sourceNodeId(scopeKey(0, ['in-ru']));

describe('network graph elements', () => {
  it('draws an unfiltered cascade as one edge between the two panels', () => {
    const { nodes, edges } = buildGraphElements(graph());
    expect(nodes.map((n) => n.id)).toEqual(['panel:0', 'panel:2']);
    expect(edges.find((e) => e.id === 'link:5')).toMatchObject({
      source: 'panel:0',
      sourceHandle: 'out:in-ru',
      target: 'panel:2',
      targetHandle: 'in:9',
    });
  });

  // Xray takes the first matching rule, so rules over the same inbounds are a
  // row of layers: each passes what it did not match to the next one.
  it('chains rules over the same inbounds into layers behind one source', () => {
    const { nodes, edges } = buildGraphElements(
      graph({
        filters: [
          rule({ id: 3, name: 'block ads' }),
          rule({ id: 4, name: 'ru direct', action: 'direct' }),
          rule({ id: 6, name: 'rest to exit', action: 'cascade', cascadeLinkId: 5 }),
        ],
        lists: [{ id: 7, name: 'ads', kind: 'domain', entries: ['doubleclick.net'] }],
      }),
    );

    expect(nodes.find((n) => n.id === CHAIN)?.data).toMatchObject({
      panelId: 0,
      tags: ['in-ru'],
      draft: false,
    });
    expect(nodes.find((n) => n.id === 'filter:4')?.data).toMatchObject({ layer: 2 });

    // The inbound feeds the first layer; every later layer is fed by the
    // pass-through of the one before it.
    expect(edges.find((e) => e.id === 'pass:3')).toMatchObject({
      source: CHAIN,
      sourceHandle: 'out',
      target: 'filter:3',
    });
    expect(edges.find((e) => e.id === 'pass:4')).toMatchObject({
      source: 'filter:3',
      sourceHandle: 'pass',
      target: 'filter:4',
    });
    expect(edges.find((e) => e.id === 'pass:6')).toMatchObject({
      source: 'filter:4',
      sourceHandle: 'pass',
      target: 'filter:6',
    });

    // What each layer matches leaves through its own output.
    expect(edges.find((e) => e.id === 'match:3')).toMatchObject({
      sourceHandle: 'match',
      target: BLOCK_SINK_ID,
    });
    expect(edges.find((e) => e.id === 'match:4')?.target).toBe(DIRECT_SINK_ID);
    expect(edges.find((e) => e.id === 'match:6')).toMatchObject({
      target: 'panel:2',
      targetHandle: 'in:9',
    });
    // The cascade is drawn through its filter, never also as a straight edge.
    expect(edges.some((e) => e.id === 'link:5')).toBe(false);
  });

  it('separates chains that watch different inbounds', () => {
    const { nodes } = buildGraphElements(
      graph({
        filters: [
          rule({ id: 3, name: 'on ru' }),
          rule({ id: 4, name: 'panel wide', sourceInboundTags: [] }),
        ],
      }),
    );
    expect(nodes.some((n) => n.id === CHAIN)).toBe(true);
    expect(nodes.some((n) => n.id === sourceNodeId(scopeKey(0, [])))).toBe(true);
  });

  it('feeds a panel-wide chain from the all-inbounds port', () => {
    const { edges } = buildGraphElements(
      graph({ filters: [rule({ id: 4, name: 'panel wide', sourceInboundTags: [] })] }),
    );
    const key = scopeKey(0, []);
    expect(edges.find((e) => e.id === `feed:${key}:${ALL_INBOUNDS_HANDLE}`)).toMatchObject({
      source: 'panel:0',
      target: sourceNodeId(key),
    });
  });

  it('places palette drafts and keeps a dropped source visible without rules', () => {
    const { nodes } = buildGraphElements(graph(), {
      drafts: [
        {
          kind: 'source',
          id: 'draft-source:1',
          panelId: 0,
          tags: ['in-ru'],
          position: { x: 5, y: 5 },
        },
        { kind: 'filter', id: 'draft-filter:1', position: { x: 10, y: 20 } },
      ],
      sinks: { block: true },
    });
    expect(nodes.find((n) => n.id === CHAIN)?.data).toMatchObject({ draft: true });
    expect(nodes.find((n) => n.id === 'draft-filter:1')).toMatchObject({
      position: { x: 10, y: 20 },
      data: { draft: true },
    });
    // A terminal dropped from the palette shows up before anything uses it.
    expect(nodes.some((n) => n.id === BLOCK_SINK_ID)).toBe(true);
    expect(nodes.some((n) => n.id === DIRECT_SINK_ID)).toBe(false);
  });

  it('skips a rule whose panel is gone and a cascade rule with no link', () => {
    const { nodes, edges } = buildGraphElements(
      graph({
        filters: [
          rule({ id: 8, name: 'orphan', panelId: 99 }),
          rule({ id: 9, name: 'dangling', action: 'cascade', cascadeLinkId: 0 }),
        ],
      }),
    );
    expect(nodes.some((n) => n.id === 'filter:8')).toBe(false);
    expect(nodes.some((n) => n.id === 'filter:9')).toBe(true);
    expect(edges.some((e) => e.id === 'match:9')).toBe(false);
  });
});
