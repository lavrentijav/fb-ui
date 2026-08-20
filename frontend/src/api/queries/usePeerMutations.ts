import { useMutation, useQueryClient } from '@tanstack/react-query';

import { HttpUtil, type Msg } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { PeerProbeResultSchema, type PeerProbeResult, type PeerRecord } from '@/schemas/peer';

export type { PeerProbeResult };

export function usePeerMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: keys.peers.root() });
  };

  const createMut = useMutation({
    mutationFn: (payload: Partial<PeerRecord>) => HttpUtil.post('/panel/api/peers/add', payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: Partial<PeerRecord> }) =>
      HttpUtil.post(`/panel/api/peers/update/${id}`, payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const removeMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/peers/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const setEnableMut = useMutation({
    mutationFn: ({ id, enable }: { id: number; enable: boolean }) =>
      HttpUtil.post(`/panel/api/peers/setEnable/${id}`, { enable }),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const probeMut = useMutation({
    mutationFn: async (id: number): Promise<Msg<PeerProbeResult>> => {
      const raw = await HttpUtil.post(`/panel/api/peers/probe/${id}`);
      return parseMsg(raw, PeerProbeResultSchema, 'peers/probe');
    },
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  return {
    create: (payload: Partial<PeerRecord>) => createMut.mutateAsync(payload),
    update: (id: number, payload: Partial<PeerRecord>) => updateMut.mutateAsync({ id, payload }),
    remove: (id: number) => removeMut.mutateAsync(id),
    setEnable: (id: number, enable: boolean) => setEnableMut.mutateAsync({ id, enable }),
    probe: (id: number) => probeMut.mutateAsync(id),
  };
}
