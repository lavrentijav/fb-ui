import { z } from 'zod';

// The form keeps domains as one textarea; the API takes an array.
export const CertFormSchema = z.object({
  domainsText: z.string().trim().min(1, 'pages.certs.toasts.needDomain'),
  challenge: z.enum(['dns01', 'http01', 'imported']),
  dnsProviderId: z.number(),
  autoRenew: z.boolean(),
  nodeIds: z.array(z.number()),
});

// An empty zone manages every zone the token can see; apiUrl may be a bare
// host, and apiPort 0 keeps Technitium's default (5380).
export const DnsProviderFormSchema = z
  .object({
    name: z.string().trim(),
    type: z.enum(['cloudflare', 'technitium']),
    zone: z.string().trim(),
    apiUrl: z.string().trim(),
    apiPort: z.number().int().min(0).max(65535),
    credential: z.string(),
  })
  .superRefine((val, ctx) => {
    if (val.type === 'technitium' && !/^(https?:\/\/)?[^\s/]+/.test(val.apiUrl)) {
      ctx.addIssue({ code: 'custom', path: ['apiUrl'], message: 'pages.certs.toasts.needApiUrl' });
    }
  });

export type CertFormValues = z.infer<typeof CertFormSchema>;
export type DnsProviderFormValues = z.infer<typeof DnsProviderFormSchema>;

export function splitDomains(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((d) => d.trim())
    .filter(Boolean);
}
