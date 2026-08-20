import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { PeerListSchema, type PeerRecord } from '@/schemas/peer';
import { keys } from '@/api/queryKeys';

export type { PeerRecord };

export interface PeerTotals {
  total: number;
  online: number;
  offline: number;
  advertised: number;
}

async function fetchPeers(): Promise<PeerRecord[]> {
  const msg = await HttpUtil.get('/panel/api/peers/list', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch peers');
  const validated = parseMsg(msg, PeerListSchema, 'peers/list');
  return Array.isArray(validated.obj) ? validated.obj : [];
}

export function usePeersQuery() {
  const query = useQuery({
    queryKey: keys.peers.list(),
    queryFn: fetchPeers,
  });

  const peers = useMemo(() => query.data ?? [], [query.data]);

  const totals = useMemo<PeerTotals>(() => {
    let online = 0;
    let offline = 0;
    let advertised = 0;
    for (const p of peers) {
      if (p.status === 'online') online += 1;
      else if (p.status === 'offline') offline += 1;
      // Mirrors the server-side filter behind the fallback headers.
      if (p.enable && p.status === 'online' && !p.isSelf) advertised += 1;
    }
    return { total: peers.length, online, offline, advertised };
  }, [peers]);

  return {
    peers,
    totals,
    loading: query.isFetching,
    fetched: query.data !== undefined || query.isError,
    fetchError: query.error ? (query.error as Error).message : '',
    refetch: query.refetch,
  };
}
