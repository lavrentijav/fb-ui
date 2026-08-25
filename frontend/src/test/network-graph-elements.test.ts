import { describe, expect, it } from 'vitest';

import {
  ALL_INBOUNDS_HANDLE,
  BLOCK_SINK_ID,
  DIRECT_SINK_ID,
  buildGraphElements,
} from '@/lib/network/graph-elements';
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

describe('network graph elements', () => {
  it('draws an unfiltered cascade as one edge between the two panels', () => {
    const { nodes, edges } = buildGraphElements(graph());
    expect(nodes.map((n) => n.id)).toEqual(['panel:0', 'panel:2']);
    const link = edges.find((e) => e.id === 'link:5');
    expect(link).toMatchObject({
      source: 'panel:0',
      sourceHandle: 'out:in-ru',
      target: 'panel:2',
      targetHandle: 'in:9',
    });
  });

  // The straight edge would claim everything on that inbound is forwarded,
  // which is exactly what the filter stops being true.
  it('routes a governed cascade through its filter instead of straight across', () => {
    const { nodes, edges } = buildGraphElements(
      graph({
        filters: [
          {
            id: 3,
            name: 'ads to exit',
            panelId: 0,
            sourceInboundTags: ['in-ru'],
            listIds: [7],
            action: 'cascade',
            cascadeLinkId: 5,
            enable: true,
          },
        ],
        lists: [{ id: 7, name: 'ads', kind: 'domain', entries: ['doubleclick.net'] }],
      }),
    );

    expect(nodes.find((n) => n.id === 'filter:3')?.data).toMatchObject({
      action: 'cascade',
      listNames: ['ads'],
      scope: ['in-ru'],
    });
    expect(edges.find((e) => e.id === 'link:5')).toBeUndefined();
    expect(edges.find((e) => e.id === 'rule-in:3:out:in-ru')).toMatchObject({
      source: 'panel:0',
      target: 'filter:3',
    });
    expect(edges.find((e) => e.id === 'rule-out:3')).toMatchObject({
      source: 'filter:3',
      target: 'panel:2',
      targetHandle: 'in:9',
    });
  });

  it('gives a block rule its own terminal and leaves the direct one out', () => {
    const { nodes, edges } = buildGraphElements(
      graph({
        filters: [
          {
            id: 4,
            name: 'block ads',
            panelId: 0,
            sourceInboundTags: [],
            listIds: [],
            action: 'block',
            enable: false,
          },
        ],
      }),
    );

    expect(nodes.some((n) => n.id === BLOCK_SINK_ID)).toBe(true);
    expect(nodes.some((n) => n.id === DIRECT_SINK_ID)).toBe(false);
    // No explicit tags means the rule matches every inbound on its panel.
    expect(edges.find((e) => e.id === `rule-in:4:${ALL_INBOUNDS_HANDLE}`)).toMatchObject({
      sourceHandle: ALL_INBOUNDS_HANDLE,
      target: 'filter:4',
    });
    expect(edges.find((e) => e.id === 'rule-out:4')?.className).toContain('is-paused');
    // The cascade it does not govern is still drawn.
    expect(edges.some((e) => e.id === 'link:5')).toBe(true);
  });

  it('skips a rule whose panel is gone and a cascade rule with no link', () => {
    const { nodes, edges } = buildGraphElements(
      graph({
        filters: [
          { id: 8, name: 'orphan', panelId: 99, listIds: [], action: 'block', enable: true },
          { id: 9, name: 'dangling', panelId: 0, listIds: [], action: 'cascade', enable: true },
        ],
      }),
    );
    expect(nodes.some((n) => n.id === 'filter:8')).toBe(false);
    expect(nodes.some((n) => n.id === 'filter:9')).toBe(true);
    expect(edges.some((e) => e.id === 'rule-out:9')).toBe(false);
  });
});
