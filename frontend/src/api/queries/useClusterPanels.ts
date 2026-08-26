import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import { ClusterViewSchema, type ClusterPanel, type ClusterView } from '@/schemas/cluster';

export type { ClusterPanel, ClusterView };

const emptyView: ClusterView = { panels: [], leaderGuid: '', selfGuid: '' };

async function fetchCluster(): Promise<ClusterView> {
  const msg = await HttpUtil.get('/panel/api/cluster/panels', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch the cluster');
  return parseMsg(msg, ClusterViewSchema, 'cluster/panels').obj ?? emptyView;
}

export function useClusterPanels() {
  const query = useQuery({
    queryKey: keys.cluster.panels(),
    queryFn: fetchCluster,
    // The lease turns over in tens of seconds; polling keeps the badge honest.
    refetchInterval: 15_000,
  });
  const view = query.data ?? emptyView;
  const panels = view.panels ?? [];
  const leader = panels.find((panel) => panel.guid === view.leaderGuid);
  return {
    panels,
    leader,
    leadsHere: !!view.leaderGuid && view.leaderGuid === view.selfGuid,
    fetched: query.data !== undefined || query.isError,
  };
}
