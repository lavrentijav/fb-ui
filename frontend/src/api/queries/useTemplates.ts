import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import {
  InboundTemplateResultSchema,
  InboundTemplateSchema,
  type InboundTemplate,
  type InboundTemplateResult,
} from '@/generated/zod';

export type { InboundTemplate, InboundTemplateResult };

// The slim list carries more than these; the page needs only these.
const PickInboundSchema = z.object({
  id: z.number(),
  remark: z.string().optional(),
  port: z.number().optional(),
  listen: z.string().optional(),
  protocol: z.string().optional(),
  nodeId: z.number().nullable().optional(),
});
export type PickInbound = z.infer<typeof PickInboundSchema>;

export function useTemplates() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: keys.templates.root(),
    queryFn: async () => {
      const msg = await HttpUtil.get('/panel/api/templates/list', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch templates');
      return parseMsg(msg, z.array(InboundTemplateSchema), 'templates/list').obj ?? [];
    },
  });
  const inbounds = useQuery({
    queryKey: ['templates', 'inbounds'],
    queryFn: async () => {
      const msg = await HttpUtil.get('/panel/api/inbounds/list/slim', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch inbounds');
      return z.array(PickInboundSchema).parse(msg.obj ?? []);
    },
  });
  const post = useMutation({
    mutationFn: ({ url, body }: { url: string; body?: unknown }) => HttpUtil.post(url, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.templates.root() }),
  });
  return {
    templates: list.data ?? [],
    inbounds: inbounds.data ?? [],
    loading: list.isFetching,
    create: (name: string, remark: string, inboundIds: number[]) =>
      post.mutateAsync({
        url: '/panel/api/templates/fromInbounds',
        body: { name, remark, inboundIds },
      }),
    remove: (id: number) => post.mutateAsync({ url: `/panel/api/templates/del/${id}` }),
    apply: async (id: number, nodeIds: number[]): Promise<InboundTemplateResult[] | null> => {
      const msg = await HttpUtil.post(`/panel/api/templates/apply/${id}`, { nodeIds });
      if (!msg?.success) return null;
      return parseMsg(msg, z.array(InboundTemplateResultSchema), 'templates/apply').obj ?? [];
    },
  };
}
