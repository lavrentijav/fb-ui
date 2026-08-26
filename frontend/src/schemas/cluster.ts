import { z } from 'zod';

export const ClusterPanelSchema = z.object({
  id: z.number(),
  guid: z.string(),
  name: z.string().optional(),
  address: z.string().optional(),
  version: z.string().optional(),
  // Unix seconds; the panel whose claim has not lapsed is the one in charge.
  leaderUntil: z.number().optional(),
  leaderTerm: z.number().optional(),
  lastSeen: z.number().optional(),
});

export const ClusterViewSchema = z.object({
  panels: z.array(ClusterPanelSchema).nullish(),
  leaderGuid: z.string().optional(),
  selfGuid: z.string().optional(),
});

export type ClusterPanel = z.infer<typeof ClusterPanelSchema>;
export type ClusterView = z.infer<typeof ClusterViewSchema>;
