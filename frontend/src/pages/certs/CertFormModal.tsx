import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Form, Input, Modal, Select, Switch, message } from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import type { Msg } from '@/utils';
import type { Certificate, CertificateRequest, DnsProvider } from '@/api/queries/useCerts';
import { CertFormSchema, splitDomains, type CertFormValues } from '@/schemas/cert';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { useNodesQuery } from '@/api/queries/useNodesQuery';

interface CertFormModalProps {
  open: boolean;
  cert: Certificate | null;
  dns: DnsProvider[];
  save: (payload: CertificateRequest) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaults(): CertFormValues {
  return { domainsText: '', challenge: 'dns01', dnsProviderId: 0, autoRenew: true, nodeIds: [] };
}

export default function CertFormModal({ open, cert, dns, save, onOpenChange }: CertFormModalProps) {
  const { t } = useTranslation();
  const methods = useForm<CertFormValues>({ defaultValues: defaults() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);
  const challenge = useWatch({ control: methods.control, name: 'challenge' });
  const { nodes } = useNodesQuery();
  const imported = cert?.challenge === 'imported';

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<Certificate | null | undefined>(undefined);
  if (!open) {
    if (synced !== undefined) setSynced(undefined);
  } else if (synced !== cert) {
    setSynced(cert);
    methods.reset(
      cert
        ? {
            domainsText: cert.domains.join('\n'),
            challenge: cert.challenge,
            dnsProviderId: cert.dnsProviderId,
            autoRenew: cert.autoRenew,
            nodeIds: (cert.deliveries ?? []).map((d) => d.nodeId),
          }
        : defaults(),
    );
  }

  async function onFinish(values: CertFormValues) {
    const result = CertFormSchema.safeParse(values);
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.certs.toasts.needDomain'));
      return;
    }
    setSubmitting(true);
    try {
      const msg = await save({
        domains: splitDomains(result.data.domainsText),
        challenge: result.data.challenge,
        dnsProviderId: result.data.challenge === 'dns01' ? result.data.dnsProviderId : 0,
        autoRenew: result.data.autoRenew,
        nodeIds: result.data.challenge === 'dns01' ? result.data.nodeIds : [],
      });
      if (msg?.success) onOpenChange(false);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      {messageContextHolder}
      <Modal
        open={open}
        title={cert ? t('pages.certs.editCert') : t('pages.certs.addCert')}
        confirmLoading={submitting}
        okText={t('save')}
        cancelText={t('cancel')}
        mask={{ closable: false }}
        width="560px"
        onOk={methods.handleSubmit(onFinish)}
        onCancel={() => onOpenChange(false)}
      >
        <FormProvider {...methods}>
          <Form layout="vertical">
            <FormField
              label={t('pages.certs.domains')}
              name="domainsText"
              extra={t('pages.certs.domainsHint')}
              rules={{ validate: rhfZodValidate(CertFormSchema.shape.domainsText) }}
            >
              <Input.TextArea
                rows={3}
                disabled={imported}
                placeholder={'msk1.corp.example\n*.msk1.corp.example'}
              />
            </FormField>
            {!imported && (
              <FormField
                label={t('pages.certs.challenge')}
                name="challenge"
                extra={t('pages.certs.challengeHint')}
              >
                <Select
                  options={[
                    { value: 'dns01', label: 'DNS-01' },
                    { value: 'http01', label: 'HTTP-01 (:80)' },
                  ]}
                />
              </FormField>
            )}
            {!imported && challenge === 'dns01' && (
              <FormField label={t('pages.certs.dnsProvider')} name="dnsProviderId">
                <Select
                  options={[
                    { value: 0, label: t('pages.certs.dnsAuto') },
                    ...dns.map((d) => ({
                      value: d.id,
                      label: `${d.name} (${d.zone || (d.zones ?? []).join(', ')})`,
                    })),
                  ]}
                />
              </FormField>
            )}
            {!imported && challenge === 'dns01' && (
              <FormField
                label={t('pages.certs.forNode')}
                name="nodeIds"
                extra={t('pages.certs.forNodeHint')}
              >
                <Select
                  mode="multiple"
                  allowClear
                  placeholder={t('pages.certs.thisPanelOnly')}
                  options={[
                    { value: 0, label: t('pages.certs.thisPanel') },
                    ...nodes.map((n) => ({ value: n.id, label: `${n.name} (${n.address})` })),
                  ]}
                />
              </FormField>
            )}
            <FormField label={t('pages.certs.autoRenew')} name="autoRenew" valueProp="checked">
              <Switch />
            </FormField>
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
