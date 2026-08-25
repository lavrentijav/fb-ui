import { z } from 'zod';

export const GraphInboundSchema = z.object({
  id: z.number(),
  tag: z.string(),
  remark: z.string().optional(),
  protocol: z.string().optional(),
  port: z.number().optional(),
  enable: z.boolean().optional(),
  clients: z.number().optional(),
});

export const GraphPanelSchema = z.object({
  id: z.number(),
  name: z.string().optional(),
  role: z.string().optional(),
  status: z.string().optional(),
  address: z.string().optional(),
  self: z.boolean().optional(),
  enable: z.boolean().optional(),
  // Serialized as null for a panel that owns no inbounds.
  inbounds: z.array(GraphInboundSchema).nullish(),
});

export const CascadeLinkSchema = z.object({
  id: z.number(),
  remark: z.string().optional(),
  sourcePanelId: z.number(),
  sourceInboundTag: z.string(),
  targetPanelId: z.number(),
  targetInboundId: z.number(),
  targetClientEmail: z.string().optional(),
  enable: z.boolean().optional(),
  applied: z.number().optional(),
});

export const NetworkGraphSchema = z.object({
  panels: z.array(GraphPanelSchema),
  links: z.array(CascadeLinkSchema).nullish(),
});

export type GraphInbound = z.infer<typeof GraphInboundSchema>;
export type GraphPanel = z.infer<typeof GraphPanelSchema>;
export type CascadeLink = z.infer<typeof CascadeLinkSchema>;
export type NetworkGraph = z.infer<typeof NetworkGraphSchema>;
