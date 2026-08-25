import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import {
  FilterListsSchema,
  FilterRulesSchema,
  type FilterList,
  type FilterRule,
} from '@/schemas/filter';

export type { FilterList, FilterRule };

async function fetchLists(): Promise<FilterList[]> {
  const msg = await HttpUtil.get('/panel/api/filters/lists', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch filter lists');
  return parseMsg(msg, FilterListsSchema, 'filters/lists').obj ?? [];
}

async function fetchRules(): Promise<FilterRule[]> {
  const msg = await HttpUtil.get('/panel/api/filters/rules', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch filter rules');
  return parseMsg(msg, FilterRulesSchema, 'filters/rules').obj ?? [];
}

export function useFilters() {
  const queryClient = useQueryClient();
  const listsQuery = useQuery({ queryKey: keys.filters.lists(), queryFn: fetchLists });
  const rulesQuery = useQuery({ queryKey: keys.filters.rules(), queryFn: fetchRules });

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: keys.filters.root() });
    // A rule change re-pushes its panel, which the topology view shows.
    queryClient.invalidateQueries({ queryKey: keys.network.root() });
  };

  const createListMut = useMutation({
    mutationFn: (payload: Partial<FilterList>) =>
      HttpUtil.post('/panel/api/filters/lists/add', payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const updateListMut = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: Partial<FilterList> }) =>
      HttpUtil.post(`/panel/api/filters/lists/update/${id}`, payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const removeListMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/filters/lists/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const createRuleMut = useMutation({
    mutationFn: (payload: Partial<FilterRule>) =>
      HttpUtil.post('/panel/api/filters/rules/add', payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const updateRuleMut = useMutation({
    mutationFn: ({ id, payload }: { id: number; payload: Partial<FilterRule> }) =>
      HttpUtil.post(`/panel/api/filters/rules/update/${id}`, payload),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const reorderRulesMut = useMutation({
    mutationFn: (ids: number[]) => HttpUtil.post('/panel/api/filters/rules/reorder', { ids }),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const removeRuleMut = useMutation({
    mutationFn: (id: number) => HttpUtil.post(`/panel/api/filters/rules/del/${id}`),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  const setRuleEnableMut = useMutation({
    mutationFn: ({ id, enable }: { id: number; enable: boolean }) =>
      HttpUtil.post(`/panel/api/filters/rules/setEnable/${id}`, { enable }),
    onSuccess: (msg) => {
      if (msg?.success) invalidate();
    },
  });

  return {
    lists: listsQuery.data ?? [],
    rules: rulesQuery.data ?? [],
    loading: listsQuery.isFetching || rulesQuery.isFetching,
    fetched: listsQuery.data !== undefined || listsQuery.isError,
    fetchError: listsQuery.error ? (listsQuery.error as Error).message : '',
    refetch: () => {
      listsQuery.refetch();
      rulesQuery.refetch();
    },
    createList: (payload: Partial<FilterList>) => createListMut.mutateAsync(payload),
    updateList: (id: number, payload: Partial<FilterList>) =>
      updateListMut.mutateAsync({ id, payload }),
    removeList: (id: number) => removeListMut.mutateAsync(id),
    createRule: (payload: Partial<FilterRule>) => createRuleMut.mutateAsync(payload),
    updateRule: (id: number, payload: Partial<FilterRule>) =>
      updateRuleMut.mutateAsync({ id, payload }),
    reorderRules: (ids: number[]) => reorderRulesMut.mutateAsync(ids),
    removeRule: (id: number) => removeRuleMut.mutateAsync(id),
    setRuleEnable: (id: number, enable: boolean) => setRuleEnableMut.mutateAsync({ id, enable }),
  };
}
