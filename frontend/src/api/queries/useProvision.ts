import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import {
  ProvisionJobSchema,
  type NodeProvisionRequest,
  type ProvisionJob,
  type ProvisionStep,
} from '@/generated/zod';

export type { NodeProvisionRequest, ProvisionJob, ProvisionStep };

export async function startProvision(req: NodeProvisionRequest): Promise<ProvisionJob | null> {
  const msg = await HttpUtil.post('/panel/api/provision/start', req, { silentSuccess: true });
  if (!msg?.success) return null;
  return parseMsg(msg, ProvisionJobSchema, 'provision/start').obj ?? null;
}

async function fetchJob(id: string): Promise<ProvisionJob | null> {
  const msg = await HttpUtil.get(`/panel/api/provision/get/${id}`, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch provisioning job');
  return parseMsg(msg, ProvisionJobSchema, 'provision/get').obj ?? null;
}

// Polls a job while it runs and refreshes the node list once it registers one.
export function useProvisionJob(id: string | null) {
  const queryClient = useQueryClient();
  const q = useQuery({
    queryKey: keys.provision.job(id ?? ''),
    queryFn: () => fetchJob(id ?? ''),
    enabled: id !== null,
    refetchInterval: (query) => (query.state.data?.status === 'running' ? 1500 : false),
  });
  const status = q.data?.status;
  useEffect(() => {
    if (status && status !== 'running') {
      queryClient.invalidateQueries({ queryKey: keys.nodes.root() });
    }
  }, [status, queryClient]);
  return { job: q.data ?? null, error: q.error ? (q.error as Error).message : '' };
}
