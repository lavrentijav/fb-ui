import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import {
  AcmeAccountViewSchema,
  CertificateSchema,
  DnsProviderSchema,
  DnsZoneSchema,
  type AcmeAccountRequest,
  type AcmeAccountView,
  type Certificate,
  type CertificateRequest,
  type DnsProvider,
  type DnsProviderRequest,
  type DnsZone,
} from '@/generated/zod';

export type {
  AcmeAccountRequest,
  AcmeAccountView,
  Certificate,
  CertificateRequest,
  DnsProvider,
  DnsProviderRequest,
  DnsZone,
};

const CertificatesSchema = z.array(CertificateSchema);
const DnsProvidersSchema = z.array(DnsProviderSchema);
const DnsZonesSchema = z.array(DnsZoneSchema);

async function fetchCerts(): Promise<Certificate[]> {
  const msg = await HttpUtil.get('/panel/api/certs/list', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch certificates');
  return parseMsg(msg, CertificatesSchema, 'certs/list').obj ?? [];
}

async function fetchDns(): Promise<DnsProvider[]> {
  const msg = await HttpUtil.get('/panel/api/dns/list', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch DNS providers');
  return parseMsg(msg, DnsProvidersSchema, 'dns/list').obj ?? [];
}

async function fetchZones(): Promise<DnsZone[]> {
  const msg = await HttpUtil.get('/panel/api/dns/zones', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch DNS zones');
  return parseMsg(msg, DnsZonesSchema, 'dns/zones').obj ?? [];
}

// Every zone the panel can write, for zone pickers outside the certs page.
export function useDnsZones() {
  const q = useQuery({ queryKey: keys.certs.zones(), queryFn: fetchZones });
  return { zones: q.data ?? [], loading: q.isFetching };
}

async function fetchAccount(): Promise<AcmeAccountView | null> {
  const msg = await HttpUtil.get('/panel/api/certs/account', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch ACME account');
  return parseMsg(msg, AcmeAccountViewSchema, 'certs/account').obj ?? null;
}

const JSON_HEADERS = { headers: { 'Content-Type': 'application/json' } };

function post(url: string, data?: unknown) {
  return HttpUtil.post(url, data);
}

function useCertMutation<A>(fn: (arg: A) => ReturnType<typeof post>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (msg) => {
      if (msg?.success) queryClient.invalidateQueries({ queryKey: keys.certs.root() });
    },
  });
}

export function useCerts() {
  const certsQuery = useQuery({
    queryKey: keys.certs.list(),
    queryFn: fetchCerts,
    // An order runs in the background; keep polling while one is in flight.
    refetchInterval: (q) =>
      (q.state.data ?? []).some((c) => c.status === 'issuing') ? 3000 : false,
  });
  const dnsQuery = useQuery({ queryKey: keys.certs.dns(), queryFn: fetchDns });
  const accountQuery = useQuery({ queryKey: keys.certs.account(), queryFn: fetchAccount });

  const createCert = useCertMutation((p: CertificateRequest) => post('/panel/api/certs/add', p));
  const updateCert = useCertMutation(({ id, p }: { id: number; p: CertificateRequest }) =>
    post(`/panel/api/certs/update/${id}`, p),
  );
  const removeCert = useCertMutation((id: number) => post(`/panel/api/certs/del/${id}`));
  const issueCert = useCertMutation((id: number) => post(`/panel/api/certs/issue/${id}`));
  const scanCerts = useCertMutation((_: void) => post('/panel/api/certs/scan'));
  const applyCert = useCertMutation(({ id, target }: { id: number; target: 'web' | 'sub' }) =>
    post(`/panel/api/certs/apply/${id}`, { target }),
  );
  const saveAccount = useCertMutation((p: AcmeAccountRequest) =>
    post('/panel/api/certs/account', p),
  );
  const createDns = useCertMutation((p: DnsProviderRequest) =>
    HttpUtil.post('/panel/api/dns/add', p, JSON_HEADERS),
  );
  const updateDns = useCertMutation(({ id, p }: { id: number; p: DnsProviderRequest }) =>
    HttpUtil.post(`/panel/api/dns/update/${id}`, p, JSON_HEADERS),
  );
  const removeDns = useCertMutation((id: number) => post(`/panel/api/dns/del/${id}`));
  const syncDns = useCertMutation((id: number) => post(`/panel/api/dns/sync/${id}`));

  return {
    certs: certsQuery.data ?? [],
    dns: dnsQuery.data ?? [],
    account: accountQuery.data ?? null,
    loading: certsQuery.isFetching || dnsQuery.isFetching,
    fetched: certsQuery.data !== undefined || certsQuery.isError,
    fetchError: certsQuery.error ? (certsQuery.error as Error).message : '',
    refetch: () => {
      certsQuery.refetch();
      dnsQuery.refetch();
      accountQuery.refetch();
    },
    createCert: (p: CertificateRequest) => createCert.mutateAsync(p),
    updateCert: (id: number, p: CertificateRequest) => updateCert.mutateAsync({ id, p }),
    removeCert: (id: number) => removeCert.mutateAsync(id),
    issueCert: (id: number) => issueCert.mutateAsync(id),
    scanCerts: () => scanCerts.mutateAsync(),
    applyCert: (id: number, target: 'web' | 'sub') => applyCert.mutateAsync({ id, target }),
    saveAccount: (p: AcmeAccountRequest) => saveAccount.mutateAsync(p),
    createDns: (p: DnsProviderRequest) => createDns.mutateAsync(p),
    updateDns: (id: number, p: DnsProviderRequest) => updateDns.mutateAsync({ id, p }),
    removeDns: (id: number) => removeDns.mutateAsync(id),
    syncDns: (id: number) => syncDns.mutateAsync(id),
    testDns: (id: number, p: DnsProviderRequest) =>
      HttpUtil.post('/panel/api/dns/test', { id, ...p }, JSON_HEADERS),
  };
}
