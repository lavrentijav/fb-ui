import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { NetworkGraphSchema, type CascadeLink, type NetworkGraph } from '@/schemas/network';

export type { CascadeLink, NetworkGraph };

const emptyGraph: NetworkGraph = { panels: [], links: [] };

async function fetchGraph(): Promise<NetworkGraph> {
  const msg = await HttpUtil.get('/panel/api/network/graph', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch the network graph');
  return parseMsg(msg, NetworkGraphSchema, 'network/graph').obj ?? emptyGraph;
}

export function useNetworkGraph() {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: keys.network.graph(), queryFn: fetchGraph });
  const invalidate = () => queryClient.invalidateQueries({ queryKey: keys.network.root() });

  const addLinkMut = useMutation({
    mutationFn: (payload: Partial<CascadeLink>) =>
      HttpUtil.post('/panel/api/network/link', payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const removeLinkMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/network/link/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const setLinkEnableMut = useMutation({
    mutationFn: ({ id, enable }: { id: number; enable: boolean }) =>
      HttpUtil.post(`/panel/api/network/link/setEnable/${id}`, { enable }),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  return {
    graph: query.data ?? emptyGraph,
    loading: query.isFetching,
    fetched: query.data !== undefined || query.isError,
    fetchError: query.error ? (query.error as Error).message : '',
    refetch: query.refetch,
    addLink: (payload: Partial<CascadeLink>) => addLinkMut.mutateAsync(payload),
    removeLink: (id: number) => removeLinkMut.mutateAsync(id),
    setLinkEnable: (id: number, enable: boolean) => setLinkEnableMut.mutateAsync({ id, enable }),
  };
}
