import { z } from 'zod';

// The form keeps domains as one textarea; the API takes an array.
export const CertFormSchema = z.object({
  domainsText: z.string().trim().min(1, 'pages.certs.toasts.needDomain'),
  challenge: z.enum(['dns01', 'http01', 'imported']),
  dnsProviderId: z.number(),
  autoRenew: z.boolean(),
});

export const DnsProviderFormSchema = z
  .object({
    name: z.string().trim(),
    type: z.enum(['cloudflare', 'technitium']),
    zone: z.string().trim().min(3, 'pages.certs.toasts.needZone'),
    apiUrl: z.string().trim(),
    credential: z.string(),
  })
  .superRefine((val, ctx) => {
    if (val.type === 'technitium' && !/^https?:\/\/\S+$/.test(val.apiUrl)) {
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
