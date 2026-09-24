import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { z } from 'zod';

import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { keys } from '@/api/queryKeys';
import {
  AcmeAccountViewSchema,
  CertificateSchema,
  DnsProviderSchema,
  type AcmeAccountRequest,
  type AcmeAccountView,
  type Certificate,
  type CertificateRequest,
  type DnsProvider,
  type DnsProviderRequest,
} from '@/generated/zod';

export type {
  AcmeAccountRequest,
  AcmeAccountView,
  Certificate,
  CertificateRequest,
  DnsProvider,
  DnsProviderRequest,
};

const CertificatesSchema = z.array(CertificateSchema);
const DnsProvidersSchema = z.array(DnsProviderSchema);

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

async function fetchAccount(): Promise<AcmeAccountView | null> {
  const msg = await HttpUtil.get('/panel/api/certs/account', undefined, { silent: true });
  if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch ACME account');
  return parseMsg(msg, AcmeAccountViewSchema, 'certs/account').obj ?? null;
}

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
  const createDns = useCertMutation((p: DnsProviderRequest) => post('/panel/api/dns/add', p));
  const updateDns = useCertMutation(({ id, p }: { id: number; p: DnsProviderRequest }) =>
    post(`/panel/api/dns/update/${id}`, p),
  );
  const removeDns = useCertMutation((id: number) => post(`/panel/api/dns/del/${id}`));

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
    testDns: (id: number, p: DnsProviderRequest) => post('/panel/api/dns/test', { id, ...p }),
  };
}
