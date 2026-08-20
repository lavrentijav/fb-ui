import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Col, Form, Input, InputNumber, Modal, Row, Select, Switch, message } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { PeerRecord } from '@/api/queries/usePeersQuery';
import type { Msg } from '@/utils';
import { PeerFormSchema, type PeerFormValues } from '@/schemas/peer';
import { FormField, rhfZodValidate } from '@/components/form/rhf';

type Mode = 'add' | 'edit';

interface PeerFormModalProps {
  open: boolean;
  mode: Mode;
  peer: PeerRecord | null;
  save: (payload: Partial<PeerRecord>) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaultValues(): PeerFormValues {
  return {
    id: 0,
    name: '',
    remark: '',
    scheme: 'https',
    domain: '',
    port: 2096,
    subPath: '/sub/',
    basePath: '/',
    ipsText: '',
    enable: true,
    allowPrivateAddress: false,
    isSelf: false,
  };
}

export default function PeerFormModal({
  open,
  mode,
  peer,
  save,
  onOpenChange,
}: PeerFormModalProps) {
  const { t } = useTranslation();
  const methods = useForm<PeerFormValues>({ defaultValues: defaultValues() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<{ mode: string; peer: PeerRecord | null } | null>(null);
  if (!open) {
    if (synced) setSynced(null);
  } else if (!synced || synced.mode !== mode || synced.peer !== (peer ?? null)) {
    setSynced({ mode, peer: peer ?? null });
    const base = defaultValues();
    methods.reset(
      mode === 'edit' && peer
        ? {
            ...base,
            id: peer.id,
            name: peer.name ?? '',
            remark: peer.remark ?? '',
            scheme: (peer.scheme as 'http' | 'https') || base.scheme,
            domain: peer.domain ?? '',
            port: peer.port ?? base.port,
            subPath: peer.subPath || base.subPath,
            basePath: peer.basePath || base.basePath,
            ipsText: (peer.ips ?? []).join('\n'),
            enable: peer.enable ?? true,
            allowPrivateAddress: peer.allowPrivateAddress ?? false,
            isSelf: peer.isSelf ?? false,
          }
        : base,
    );
  }

  const title = useMemo(
    () => (mode === 'edit' ? t('pages.peers.editPeer') : t('pages.peers.addPeer')),
    [mode, t],
  );

  function buildPayload(values: PeerFormValues): Partial<PeerRecord> {
    return {
      id: values.id || 0,
      name: values.name.trim(),
      remark: values.remark?.trim() || '',
      scheme: values.scheme,
      domain: values.domain.trim(),
      port: values.port,
      subPath: values.subPath.trim() || '/sub/',
      basePath: values.basePath.trim() || '/',
      ips: values.ipsText
        .split(/[\n,]/)
        .map((ip) => ip.trim())
        .filter(Boolean),
      enable: values.enable,
      allowPrivateAddress: values.allowPrivateAddress,
      isSelf: values.isSelf,
    };
  }

  async function onFinish(values: PeerFormValues) {
    const result = PeerFormSchema.safeParse(values);
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.peers.toasts.fillRequired'));
      return;
    }
    setSubmitting(true);
    try {
      const msg = await save(buildPayload(result.data));
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
        title={title}
        confirmLoading={submitting}
        okText={t('save')}
        cancelText={t('cancel')}
        mask={{ closable: false }}
        width="640px"
        onOk={methods.handleSubmit(onFinish)}
        onCancel={() => onOpenChange(false)}
      >
        <FormProvider {...methods}>
          <Form layout="vertical">
            <Row gutter={16}>
              <Col xs={24} md={12}>
                <FormField
                  label={t('pages.peers.name')}
                  name="name"
                  rules={{ validate: rhfZodValidate(PeerFormSchema.shape.name) }}
                >
                  <Input placeholder={t('pages.peers.namePlaceholder')} />
                </FormField>
              </Col>
              <Col xs={24} md={12}>
                <FormField label={t('pages.peers.remark')} name="remark">
                  <Input />
                </FormField>
              </Col>
            </Row>

            <Row gutter={16}>
              <Col xs={24} md={6}>
                <FormField label={t('pages.peers.scheme')} name="scheme">
                  <Select
                    options={[
                      { value: 'https', label: 'https' },
                      { value: 'http', label: 'http' },
                    ]}
                  />
                </FormField>
              </Col>
              <Col xs={24} md={12}>
                <FormField
                  label={t('pages.peers.domain')}
                  name="domain"
                  rules={{ validate: rhfZodValidate(PeerFormSchema.shape.domain) }}
                >
                  <Input placeholder={t('pages.peers.domainPlaceholder')} />
                </FormField>
              </Col>
              <Col xs={24} md={6}>
                <FormField label={t('pages.peers.port')} name="port">
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </FormField>
              </Col>
            </Row>

            <Row gutter={16}>
              <Col xs={24} md={12}>
                <FormField
                  label={t('pages.peers.subPath')}
                  name="subPath"
                  extra={t('pages.peers.subPathHint')}
                >
                  <Input placeholder="/sub/" />
                </FormField>
              </Col>
              <Col xs={24} md={12}>
                <FormField label={t('pages.peers.basePath')} name="basePath">
                  <Input placeholder="/" />
                </FormField>
              </Col>
            </Row>

            <FormField label={t('pages.peers.ips')} name="ipsText" extra={t('pages.peers.ipsHint')}>
              <Input.TextArea rows={3} placeholder={'185.51.100.2\n2001:db8::2'} />
            </FormField>

            <Row gutter={16}>
              <Col xs={24} md={8}>
                <FormField label={t('enable')} name="enable" valueProp="checked">
                  <Switch />
                </FormField>
              </Col>
              <Col xs={24} md={8}>
                <FormField
                  label={t('pages.peers.isSelf')}
                  name="isSelf"
                  valueProp="checked"
                  extra={t('pages.peers.isSelfHint')}
                >
                  <Switch />
                </FormField>
              </Col>
              <Col xs={24} md={8}>
                <FormField
                  label={t('pages.peers.allowPrivateAddress')}
                  name="allowPrivateAddress"
                  valueProp="checked"
                >
                  <Switch />
                </FormField>
              </Col>
            </Row>
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
