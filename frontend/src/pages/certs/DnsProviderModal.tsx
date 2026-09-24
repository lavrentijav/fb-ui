import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Form, Input, Modal, Select, message } from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import type { Msg } from '@/utils';
import type { DnsProvider, DnsProviderRequest } from '@/api/queries/useCerts';
import { DnsProviderFormSchema, type DnsProviderFormValues } from '@/schemas/cert';
import { FormField } from '@/components/form/rhf';

interface DnsProviderModalProps {
  open: boolean;
  provider: DnsProvider | null;
  save: (payload: DnsProviderRequest) => Promise<Msg<unknown>>;
  test: (id: number, payload: DnsProviderRequest) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaults(): DnsProviderFormValues {
  return { name: '', type: 'cloudflare', zone: '', apiUrl: '', credential: '' };
}

export default function DnsProviderModal({
  open,
  provider,
  save,
  test,
  onOpenChange,
}: DnsProviderModalProps) {
  const { t } = useTranslation();
  const methods = useForm<DnsProviderFormValues>({ defaultValues: defaults() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);
  const [testing, setTesting] = useState(false);
  const type = useWatch({ control: methods.control, name: 'type' });

  const [synced, setSynced] = useState<DnsProvider | null | undefined>(undefined);
  if (!open) {
    if (synced !== undefined) setSynced(undefined);
  } else if (synced !== provider) {
    setSynced(provider);
    methods.reset(
      provider
        ? {
            name: provider.name,
            type: provider.type,
            zone: provider.zone,
            apiUrl: provider.apiUrl,
            credential: '',
          }
        : defaults(),
    );
  }

  function validated(): DnsProviderRequest | null {
    const result = DnsProviderFormSchema.safeParse(methods.getValues());
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.certs.toasts.needZone'));
      return null;
    }
    return { ...result.data, credential: result.data.credential.trim() };
  }

  async function onTest() {
    const payload = validated();
    if (!payload) return;
    setTesting(true);
    try {
      const msg = await test(provider?.id ?? 0, payload);
      if (msg?.success) messageApi.success(t('pages.certs.dnsTestOk'));
    } finally {
      setTesting(false);
    }
  }

  async function onOk() {
    const payload = validated();
    if (!payload) return;
    setSubmitting(true);
    try {
      const msg = await save(payload);
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
        title={provider ? t('pages.certs.editDns') : t('pages.certs.addDns')}
        confirmLoading={submitting}
        okText={t('save')}
        cancelText={t('cancel')}
        mask={{ closable: false }}
        width="560px"
        onOk={onOk}
        onCancel={() => onOpenChange(false)}
        footer={(_, { OkBtn, CancelBtn }) => (
          <>
            <Button loading={testing} onClick={onTest}>
              {t('pages.certs.dnsTest')}
            </Button>
            <CancelBtn />
            <OkBtn />
          </>
        )}
      >
        <FormProvider {...methods}>
          <Form layout="vertical">
            <FormField label={t('pages.certs.dnsType')} name="type">
              <Select
                options={[
                  { value: 'cloudflare', label: 'Cloudflare' },
                  { value: 'technitium', label: 'Technitium DNS Server' },
                ]}
              />
            </FormField>
            <FormField label={t('pages.certs.zone')} name="zone">
              <Input placeholder="fire-core.ru" />
            </FormField>
            <FormField label={t('pages.certs.dnsName')} name="name">
              <Input placeholder={t('pages.certs.dnsNameHint')} />
            </FormField>
            {type === 'technitium' && (
              <FormField
                label={t('pages.certs.apiUrl')}
                name="apiUrl"
                extra={t('pages.certs.apiUrlHint')}
              >
                <Input placeholder="https://ns1.example.com:53443" />
              </FormField>
            )}
            <FormField
              label={t('pages.certs.credential')}
              name="credential"
              extra={
                type === 'technitium'
                  ? t('pages.certs.credentialHintTechnitium')
                  : t('pages.certs.credentialHintCloudflare')
              }
            >
              <Input.Password
                autoComplete="off"
                placeholder={provider?.hasCredential ? t('pages.certs.credentialKeep') : ''}
              />
            </FormField>
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
