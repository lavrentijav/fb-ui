import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import {
  EdgeRouteSchema,
  EdgeSettingsSchema,
  EdgeStatusSchema,
  EdgeTargetCheckSchema,
  type EdgeRoute,
  type EdgeSettings,
  type EdgeStatus,
  type EdgeTargetCheck,
} from '@/generated/zod';

export type { EdgeRoute, EdgeSettings, EdgeStatus, EdgeTargetCheck };

async function get<T>(url: string, schema: z.ZodType<T>, label: string): Promise<T | null> {
  const msg = await HttpUtil.get(url, undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || `Failed to fetch ${label}`);
  return parseMsg(msg, schema, label).obj ?? null;
}

export function useEdge() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: keys.edge.settings(),
    queryFn: () => get('/panel/api/edge/settings', EdgeSettingsSchema, 'edge/settings'),
  });
  const routes = useQuery({
    queryKey: keys.edge.routes(),
    queryFn: () => get('/panel/api/edge/routes', z.array(EdgeRouteSchema), 'edge/routes'),
  });
  const status = useQuery({
    queryKey: keys.edge.status(),
    queryFn: () => get('/panel/api/edge/status', EdgeStatusSchema, 'edge/status'),
    refetchInterval: 10000,
  });
  const mutate = useMutation({
    mutationFn: ({ url, body }: { url: string; body?: unknown }) => HttpUtil.post(url, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.edge.root() }),
  });
  return {
    settings: settings.data ?? null,
    routes: routes.data ?? [],
    status: status.data ?? null,
    loading: settings.isFetching || routes.isFetching,
    fetchError: settings.error ? (settings.error as Error).message : '',
    saveSettings: (v: EdgeSettings) =>
      mutate.mutateAsync({ url: '/panel/api/edge/settings', body: v }),
    saveRoute: (r: EdgeRoute) =>
      mutate.mutateAsync({ url: '/panel/api/edge/routes/save', body: r }),
    removeRoute: (id: number) => mutate.mutateAsync({ url: `/panel/api/edge/routes/del/${id}` }),
    check: async (): Promise<EdgeTargetCheck[]> => {
      const msg = await HttpUtil.post('/panel/api/edge/check', undefined, { silentSuccess: true });
      if (!msg?.success) return [];
      return parseMsg(msg, z.array(EdgeTargetCheckSchema), 'edge/check').obj ?? [];
    },
  };
}
