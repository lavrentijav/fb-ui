import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Col, Form, Input, InputNumber, Modal, Row, Select, Switch, message } from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import type { Msg } from '@/utils';
import type { NodeRoleChangePayload } from '@/api/queries/useNodeMutations';
import { NodeRoleFormSchema, type NodeRoleFormValues } from '@/schemas/node';
import { FormField, rhfZodValidate } from '@/components/form/rhf';

export interface RoleChangeTarget {
  id: number;
  name?: string;
  scheme?: string;
  basePath?: string;
  allowPrivateAddress?: boolean;
  address?: string;
  port?: number;
  tlsVerifyMode?: string;
  pinnedCertSha256?: string;
  subDomain?: string;
  subPort?: number;
  subPath?: string;
  subIps?: string[] | null;
}

interface NodeRoleModalProps {
  open: boolean;
  target: 'node' | 'master';
  record: RoleChangeTarget | null;
  save: (id: number, payload: NodeRoleChangePayload) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaultValues(
  target: 'node' | 'master',
  record: RoleChangeTarget | null,
): NodeRoleFormValues {
  return {
    role: target,
    scheme: (record?.scheme as 'http' | 'https') || 'https',
    basePath: record?.basePath || '/',
    allowPrivateAddress: record?.allowPrivateAddress ?? false,
    // Its panel address is the best first guess at its subscription host and
    // the other way round: one machine usually serves both.
    address: record?.address || record?.subDomain || '',
    port: record?.port || 2053,
    apiToken: '',
    tlsVerifyMode: (record?.tlsVerifyMode as NodeRoleFormValues['tlsVerifyMode']) || 'verify',
    pinnedCertSha256: record?.pinnedCertSha256 || '',
    subDomain: record?.subDomain || record?.address || '',
    subPort: record?.subPort || 2096,
    subPath: record?.subPath || '/sub/',
    ipsText: (record?.subIps ?? []).join('\n'),
  };
}

// Turns one registered panel into the other kind. Only the target role's fields
// are asked for; what the old role knew stays on the row.
export default function NodeRoleModal({
  open,
  target,
  record,
  save,
  onOpenChange,
}: NodeRoleModalProps) {
  const { t } = useTranslation();
  const methods = useForm<NodeRoleFormValues>({ defaultValues: defaultValues(target, record) });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<{ target: string; record: RoleChangeTarget | null } | null>(
    null,
  );
  if (!open) {
    if (synced) setSynced(null);
  } else if (!synced || synced.target !== target || synced.record !== (record ?? null)) {
    setSynced({ target, record: record ?? null });
    methods.reset(defaultValues(target, record ?? null));
  }

  const toMaster = target === 'master';
  const tlsMode = useWatch({ control: methods.control, name: 'tlsVerifyMode' }) ?? 'verify';

  const title = useMemo(
    () =>
      toMaster
        ? t('pages.nodes.role.toMasterTitle', { name: record?.name ?? '' })
        : t('pages.nodes.role.toNodeTitle', { name: record?.name ?? '' }),
    [toMaster, record, t],
  );

  async function onFinish(values: NodeRoleFormValues) {
    const result = NodeRoleFormSchema.safeParse(values);
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.nodes.role.fillRequired'));
      return;
    }
    if (!record) return;
    const v = result.data;
    const payload: NodeRoleChangePayload = toMaster
      ? {
          role: 'master',
          scheme: v.scheme,
          basePath: v.basePath.trim() || '/',
          allowPrivateAddress: v.allowPrivateAddress,
          subDomain: v.subDomain,
          subPort: v.subPort,
          subPath: v.subPath.trim() || '/sub/',
          subIps: v.ipsText
            .split(/[\n,]/)
            .map((ip) => ip.trim())
            .filter(Boolean),
        }
      : {
          role: 'node',
          scheme: v.scheme,
          basePath: v.basePath.trim() || '/',
          allowPrivateAddress: v.allowPrivateAddress,
          address: v.address,
          port: v.port,
          apiToken: v.apiToken,
          tlsVerifyMode: v.tlsVerifyMode,
          pinnedCertSha256: v.pinnedCertSha256.trim(),
        };
    setSubmitting(true);
    try {
      const msg = await save(record.id, payload);
      if (msg?.success) {
        messageApi.success(t('pages.nodes.role.changed'));
        onOpenChange(false);
      }
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
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          title={toMaster ? t('pages.nodes.role.toMasterHint') : t('pages.nodes.role.toNodeHint')}
        />
        <FormProvider {...methods}>
          <Form layout="vertical">
            <Row gutter={16}>
              <Col xs={24} md={6}>
                <FormField label={t('pages.nodes.scheme')} name="scheme">
                  <Select
                    options={[
                      { value: 'https', label: 'https' },
                      { value: 'http', label: 'http' },
                    ]}
                  />
                </FormField>
              </Col>
              {toMaster ? (
                <>
                  <Col xs={24} md={12}>
                    <FormField
                      label={t('pages.peers.domain')}
                      name="subDomain"
                      rules={{ validate: rhfZodValidate(NodeRoleFormSchema.shape.subDomain) }}
                    >
                      <Input placeholder={t('pages.peers.domainPlaceholder')} />
                    </FormField>
                  </Col>
                  <Col xs={24} md={6}>
                    <FormField label={t('pages.peers.port')} name="subPort">
                      <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                    </FormField>
                  </Col>
                </>
              ) : (
                <>
                  <Col xs={24} md={12}>
                    <FormField
                      label={t('pages.nodes.address')}
                      name="address"
                      rules={{ validate: rhfZodValidate(NodeRoleFormSchema.shape.address) }}
                    >
                      <Input placeholder={t('pages.nodes.addressPlaceholder')} />
                    </FormField>
                  </Col>
                  <Col xs={24} md={6}>
                    <FormField label={t('pages.nodes.port')} name="port">
                      <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                    </FormField>
                  </Col>
                </>
              )}
            </Row>

            {toMaster ? (
              <>
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
                <FormField
                  label={t('pages.peers.ips')}
                  name="ipsText"
                  extra={t('pages.peers.ipsHint')}
                >
                  <Input.TextArea rows={3} placeholder={'185.51.100.2\n2001:db8::2'} />
                </FormField>
              </>
            ) : (
              <>
                <Row gutter={16}>
                  <Col xs={24} md={12}>
                    <FormField label={t('pages.nodes.basePath')} name="basePath">
                      <Input placeholder="/" />
                    </FormField>
                  </Col>
                  <Col xs={24} md={12}>
                    <FormField
                      label={t('pages.nodes.tlsVerifyMode')}
                      name="tlsVerifyMode"
                      extra={t('pages.nodes.tlsVerifyModeHint')}
                    >
                      <Select
                        options={[
                          { value: 'verify', label: t('pages.nodes.tlsVerify') },
                          { value: 'skip', label: t('pages.nodes.tlsSkip') },
                          { value: 'pin', label: t('pages.nodes.tlsPin') },
                          { value: 'mtls', label: t('pages.nodes.tlsMtls') },
                        ]}
                      />
                    </FormField>
                  </Col>
                </Row>
                {tlsMode === 'pin' && (
                  <FormField
                    label={t('pages.nodes.pinnedCert')}
                    name="pinnedCertSha256"
                    extra={t('pages.nodes.pinnedCertHint')}
                  >
                    <Input placeholder={t('pages.nodes.pinnedCertPlaceholder')} />
                  </FormField>
                )}
                <FormField
                  label={t('pages.nodes.apiToken')}
                  name="apiToken"
                  extra={t('pages.nodes.apiTokenHint')}
                  rules={{ validate: rhfZodValidate(NodeRoleFormSchema.shape.apiToken) }}
                >
                  <Input.Password placeholder={t('pages.nodes.apiTokenPlaceholder')} />
                </FormField>
              </>
            )}

            <FormField
              label={t('pages.nodes.allowPrivateAddress')}
              name="allowPrivateAddress"
              valueProp="checked"
              extra={t('pages.nodes.allowPrivateAddressHint')}
            >
              <Switch />
            </FormField>
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
