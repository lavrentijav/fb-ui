import { z } from 'zod';

export const PeerRecordSchema = z
  .object({
    id: z.number(),
    name: z.string().optional(),
    remark: z.string().optional(),
    scheme: z.string().optional(),
    role: z.string().optional(),
    address: z.string().optional(),
    subDomain: z.string().optional(),
    subPort: z.number().optional(),
    subPath: z.string().optional(),
    basePath: z.string().optional(),
    // Serialized as null for a master saved without any static address.
    subIps: z.array(z.string()).nullish(),
    enable: z.boolean().optional(),
    allowPrivateAddress: z.boolean().optional(),
    isSelf: z.boolean().optional(),
    publicKey: z.string().optional(),
    status: z.string().optional(),
    lastHeartbeat: z.number().optional(),
    latencyMs: z.number().optional(),
    lastError: z.string().optional(),
    createdAt: z.number().optional(),
    updatedAt: z.number().optional(),
  })
  .loose();

export const PeerListSchema = z.array(PeerRecordSchema);

export const PeerIdentitySchema = z
  .object({
    alg: z.string(),
    key: z.string(),
  })
  .loose();

// The probe response is the health patch the server just stored.
export const PeerProbeResultSchema = z
  .object({
    status: z.string(),
    latencyMs: z.number().optional(),
    publicKey: z.string().optional(),
    isSelf: z.boolean().optional(),
    lastError: z.string().optional(),
  })
  .loose();

export const PeerFormSchema = z.object({
  id: z.number().optional(),
  name: z.string().trim().min(1, 'pages.peers.toasts.fillRequired'),
  remark: z.string().optional(),
  scheme: z.enum(['http', 'https']),
  subDomain: z.string().trim().min(1, 'pages.peers.toasts.fillRequired'),
  subPort: z.number().int().min(1).max(65535),
  subPath: z.string(),
  basePath: z.string(),
  // One address per line in the textarea; split before submit.
  ipsText: z.string(),
  enable: z.boolean(),
  allowPrivateAddress: z.boolean(),
  isSelf: z.boolean(),
});

export type PeerFormValues = z.infer<typeof PeerFormSchema>;

export type PeerRecord = z.infer<typeof PeerRecordSchema>;
export type PeerIdentity = z.infer<typeof PeerIdentitySchema>;
export type PeerProbeResult = z.infer<typeof PeerProbeResultSchema>;
