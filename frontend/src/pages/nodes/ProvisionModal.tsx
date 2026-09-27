import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Col,
  Collapse,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Segmented,
  Select,
  Steps,
  Switch,
  Typography,
  message,
} from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { useDnsZones } from '@/api/queries/useCerts';
import {
  startProvision,
  useProvisionJob,
  type NodeProvisionRequest,
  type ProvisionStep,
} from '@/api/queries/useProvision';

interface ProvisionModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

type AuthKind = 'password' | 'key';

// Zone '' installs on the bare IP; the domain field overrides <name>.<zone>.
function defaults(): NodeProvisionRequest {
  return {
    name: '',
    host: '',
    sshPort: 22,
    sshUser: 'root',
    sshPassword: '',
    sshPrivateKey: '',
    hostKeySha256: '',
    domain: '',
    zone: '',
    dnsProviderId: 0,
    skipDns: false,
    nodeAcme: false,
    acmeEmail: '',
    panelPort: 0,
    installSource: 'self',
    authorizedKey: '',
    disablePasswordAuth: false,
    force: false,
    allowPrivateAddress: false,
  };
}

const stepStatus: Record<string, 'wait' | 'process' | 'finish' | 'error'> = {
  pending: 'wait',
  running: 'process',
  done: 'finish',
  skipped: 'finish',
  failed: 'error',
};

export default function ProvisionModal({ open, onOpenChange }: ProvisionModalProps) {
  const { t } = useTranslation();
  const methods = useForm<NodeProvisionRequest>({ defaultValues: defaults() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [auth, setAuth] = useState<AuthKind>('password');
  const [submitting, setSubmitting] = useState(false);
  const [jobId, setJobId] = useState<string | null>(null);
  const { job } = useProvisionJob(jobId);
  const { zones } = useDnsZones();
  const logRef = useRef<HTMLPreElement>(null);

  const [name, zone, domain] = useWatch({
    control: methods.control,
    name: ['name', 'zone', 'domain'],
  });
  const fqdn = domain.trim() || (zone && name.trim() ? `${name.trim().toLowerCase()}.${zone}` : '');

  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      methods.reset(defaults());
      setJobId(null);
      setAuth('password');
    }
  }

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
  }, [job?.log.length]);

  const zoneOptions = useMemo(
    () => [
      { value: '', label: t('pages.provision.noDomain') },
      ...zones.map((z) => ({
        value: z.zone,
        label: `${z.zone} · ${z.subdomain ? t('pages.provision.subdomainOf') : ''}${z.providerName}`,
      })),
    ],
    [zones, t],
  );

  async function onStart() {
    const v = methods.getValues();
    if (!v.name.trim() || !v.host.trim()) {
      messageApi.error(t('pages.provision.needNameHost'));
      return;
    }
    const secret = auth === 'password' ? v.sshPassword : v.sshPrivateKey;
    if (!secret.trim()) {
      messageApi.error(t('pages.provision.needAuth'));
      return;
    }
    setSubmitting(true);
    try {
      const started = await startProvision({
        ...v,
        sshPassword: auth === 'password' ? v.sshPassword : '',
        sshPrivateKey: auth === 'key' ? v.sshPrivateKey : '',
        disablePasswordAuth: auth === 'key' && v.disablePasswordAuth,
      });
      if (started) setJobId(started.id);
    } finally {
      setSubmitting(false);
    }
  }

  const running = job?.status === 'running';
  const footer = job ? (
    <Button type="primary" disabled={running} onClick={() => onOpenChange(false)}>
      {t('close')}
    </Button>
  ) : (
    <>
      <Button onClick={() => onOpenChange(false)}>{t('cancel')}</Button>
      <Button type="primary" loading={submitting} onClick={onStart}>
        {t('pages.provision.start')}
      </Button>
    </>
  );

  return (
    <>
      {messageContextHolder}
      <Modal
        open={open}
        title={t('pages.provision.title')}
        width="760px"
        mask={{ closable: false }}
        closable={!running}
        onCancel={() => !running && onOpenChange(false)}
        footer={footer}
      >
        {job ? (
          <ProvisionProgress job={job} logRef={logRef} />
        ) : (
          <FormProvider {...methods}>
            <Form layout="vertical">
              <Alert
                type="info"
                showIcon
                message={t('pages.provision.intro')}
                style={{ marginBottom: 16 }}
              />
              <Row gutter={12}>
                <Col xs={24} sm={8}>
                  <FormField
                    label={t('pages.provision.name')}
                    name="name"
                    extra={t('pages.provision.nameHint')}
                  >
                    <Input placeholder="msk2" />
                  </FormField>
                </Col>
                <Col xs={24} sm={10}>
                  <FormField label={t('pages.provision.host')} name="host">
                    <Input placeholder="203.0.113.10" />
                  </FormField>
                </Col>
                <Col xs={12} sm={6}>
                  <FormField label={t('pages.provision.sshPort')} name="sshPort">
                    <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                  </FormField>
                </Col>
              </Row>
              <Row gutter={12} align="bottom">
                <Col xs={24} sm={8}>
                  <FormField label={t('pages.provision.sshUser')} name="sshUser">
                    <Input />
                  </FormField>
                </Col>
                <Col xs={24} sm={16}>
                  <Form.Item label={t('pages.provision.auth')}>
                    <Segmented<AuthKind>
                      value={auth}
                      onChange={setAuth}
                      options={[
                        { value: 'password', label: t('pages.provision.authPassword') },
                        { value: 'key', label: t('pages.provision.authKey') },
                      ]}
                    />
                  </Form.Item>
                </Col>
              </Row>
              {auth === 'password' ? (
                <FormField label={t('pages.provision.sshPassword')} name="sshPassword">
                  <Input.Password autoComplete="new-password" />
                </FormField>
              ) : (
                <FormField label={t('pages.provision.sshPrivateKey')} name="sshPrivateKey">
                  <Input.TextArea rows={4} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
                </FormField>
              )}

              <Row gutter={12}>
                <Col xs={24} sm={12}>
                  <FormField
                    label={t('pages.provision.zone')}
                    name="zone"
                    extra={t('pages.provision.zoneHint')}
                  >
                    <Select options={zoneOptions} showSearch />
                  </FormField>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item label={t('pages.provision.fqdn')}>
                    <Typography.Text code copyable={!!fqdn}>
                      {fqdn || t('pages.provision.ipOnly')}
                    </Typography.Text>
                  </Form.Item>
                </Col>
              </Row>

              <Collapse
                size="small"
                items={[
                  {
                    key: 'advanced',
                    label: t('pages.provision.advanced'),
                    children: (
                      <>
                        <FormField
                          label={t('pages.provision.domain')}
                          name="domain"
                          extra={t('pages.provision.domainHint')}
                        >
                          <Input placeholder="msk2.corp.example" />
                        </FormField>
                        <Row gutter={12}>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.provision.installSource')}
                              name="installSource"
                            >
                              <Select
                                options={[
                                  { value: 'self', label: t('pages.provision.sourceSelf') },
                                  { value: 'github', label: t('pages.provision.sourceGithub') },
                                ]}
                              />
                            </FormField>
                          </Col>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.provision.panelPort')}
                              name="panelPort"
                              extra={t('pages.provision.panelPortHint')}
                            >
                              <InputNumber min={0} max={65535} style={{ width: '100%' }} />
                            </FormField>
                          </Col>
                        </Row>
                        <FormField
                          label={t('pages.provision.hostKey')}
                          name="hostKeySha256"
                          extra={t('pages.provision.hostKeyHint')}
                        >
                          <Input placeholder="SHA256:..." />
                        </FormField>
                        <FormField
                          label={t('pages.provision.authorizedKey')}
                          name="authorizedKey"
                          extra={t('pages.provision.authorizedKeyHint')}
                        >
                          <Input placeholder="ssh-ed25519 AAAA... ops" />
                        </FormField>
                        <Row gutter={12}>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.provision.disablePasswordAuth')}
                              name="disablePasswordAuth"
                              valueProp="checked"
                              extra={t('pages.provision.disablePasswordAuthHint')}
                            >
                              <Switch disabled={auth !== 'key'} />
                            </FormField>
                          </Col>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.provision.nodeAcme')}
                              name="nodeAcme"
                              valueProp="checked"
                              extra={t('pages.provision.nodeAcmeHint')}
                            >
                              <Switch />
                            </FormField>
                          </Col>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.provision.skipDns')}
                              name="skipDns"
                              valueProp="checked"
                              extra={t('pages.provision.skipDnsHint')}
                            >
                              <Switch />
                            </FormField>
                          </Col>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.provision.force')}
                              name="force"
                              valueProp="checked"
                              extra={t('pages.provision.forceHint')}
                            >
                              <Switch />
                            </FormField>
                          </Col>
                          <Col xs={24} sm={12}>
                            <FormField
                              label={t('pages.nodes.allowPrivateAddress')}
                              name="allowPrivateAddress"
                              valueProp="checked"
                            >
                              <Switch />
                            </FormField>
                          </Col>
                        </Row>
                      </>
                    ),
                  },
                ]}
              />
            </Form>
          </FormProvider>
        )}
      </Modal>
    </>
  );
}

function ProvisionProgress({
  job,
  logRef,
}: {
  job: NonNullable<ReturnType<typeof useProvisionJob>['job']>;
  logRef: React.RefObject<HTMLPreElement | null>;
}) {
  const { t } = useTranslation();
  const items = job.steps.map((s: ProvisionStep) => ({
    title: t(`pages.provision.step_${s.name}`),
    status: stepStatus[s.status] ?? 'wait',
    description: s.status === 'skipped' ? `${t('pages.provision.skipped')}: ${s.detail}` : s.detail,
  }));
  return (
    <>
      {job.status === 'failed' && (
        <Alert type="error" showIcon message={job.error} style={{ marginBottom: 12 }} />
      )}
      {job.status === 'done' && (
        <Alert
          type="success"
          showIcon
          message={t('pages.provision.done', { name: job.name })}
          style={{ marginBottom: 12 }}
        />
      )}
      <Descriptions size="small" column={1} style={{ marginBottom: 12 }}>
        <Descriptions.Item label={t('pages.provision.host')}>{job.host}</Descriptions.Item>
        {job.domain && (
          <Descriptions.Item label={t('pages.provision.fqdn')}>{job.domain}</Descriptions.Item>
        )}
        {job.hostKey && (
          <Descriptions.Item label={t('pages.provision.hostKey')}>
            <Typography.Text code copyable>
              {job.hostKey}
            </Typography.Text>
          </Descriptions.Item>
        )}
        {job.panelUrl && (
          <Descriptions.Item label={t('pages.provision.panelUrl')}>
            <Typography.Link href={job.panelUrl} target="_blank" rel="noreferrer">
              {job.panelUrl}
            </Typography.Link>
          </Descriptions.Item>
        )}
      </Descriptions>
      <Steps direction="vertical" size="small" items={items} />
      <pre
        ref={logRef}
        style={{
          maxHeight: 240,
          overflow: 'auto',
          fontSize: 12,
          padding: 8,
          background: 'var(--ant-color-fill-quaternary)',
          borderRadius: 6,
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-all',
        }}
      >
        {job.log.join('\n')}
      </pre>
    </>
  );
}
