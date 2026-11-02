import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Button, Descriptions, Form, Input, Space, Tag, Typography } from 'antd';
import { LoginOutlined, LogoutOutlined, TeamOutlined } from '@ant-design/icons';

import { SizeFormatter } from '@/utils';

interface Member {
  id: string;
  name?: string;
  ip: string;
  role: string;
  node?: string;
}

interface Me {
  user: string;
  email: string;
  links: string[];
  usedBytes?: number;
  totalBytes?: number;
  expire?: number;
  vnet?: {
    ip?: string;
    name?: string;
    network?: string;
    role?: string;
    members?: Member[];
    error?: string;
  };
}

const manageBase = (): string => window.__SUB_MANAGE__ || '/api/v1/sub';

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(manageBase() + path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });
  const body = (await res.json().catch(() => ({}))) as T & { error?: string };
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
}

export default function SubAccountTab() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [form] = Form.useForm<{ user: string; password: string }>();
  const [title, setTitle] = useState('');
  const [invite, setInvite] = useState('');
  const [code, setCode] = useState('');

  const { data: me, refetch } = useQuery<Me | null>({
    queryKey: ['sub-account'],
    queryFn: async () => {
      try {
        return await api<Me>('/me');
      } catch {
        return null; // not logged in
      }
    },
    retry: false,
    staleTime: 30_000,
  });

  const load = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ['sub-account'] });
    await refetch();
  }, [queryClient, refetch]);

  const login = useCallback(async () => {
    setBusy(true);
    try {
      const values = await form.validateFields();
      await api('/login', { method: 'POST', body: JSON.stringify(values) });
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [form, load]);

  const logout = useCallback(async () => {
    await api('/logout', { method: 'POST' });
    await load();
  }, [load]);

  const vnet = useCallback(
    async (action: string, body?: unknown) => {
      setBusy(true);
      try {
        await api(`/vnet${action}`, { method: 'POST', body: JSON.stringify(body ?? {}) });
        await load();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    },
    [load],
  );

  if (!me) {
    return (
      <div className="sub-rows">
        <div className="sub-row">
          <div className="sub-row-main">
            <Typography.Paragraph type="secondary">{t('subscription.accountHint')}</Typography.Paragraph>
            {error && <Alert type="error" showIcon title={error} />}
            <Form form={form} layout="vertical" onFinish={login}>
              <Form.Item
                name="user"
                label={t('subscription.accountUser')}
                rules={[{ required: true }]}
              >
                <Input autoComplete="username" autoFocus />
              </Form.Item>
              <Form.Item
                name="password"
                label={t('subscription.accountPassword')}
                rules={[{ required: true }]}
              >
                <Input.Password autoComplete="current-password" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={busy} icon={<LoginOutlined />}>
                {t('login')}
              </Button>
            </Form>
          </div>
        </div>
      </div>
    );
  }

  const used = me.usedBytes || 0;
  const total = me.totalBytes || 0;
  const net = me.vnet || {};

  return (
    <div className="sub-rows">
      <div className="sub-row">
        <div className="sub-row-main">
          <Space style={{ justifyContent: 'space-between', width: '100%' }}>
            <Typography.Text strong>{me.user}</Typography.Text>
            <Button size="small" icon={<LogoutOutlined />} onClick={logout}>
              {t('logout')}
            </Button>
          </Space>
          <Descriptions size="small" column={1} style={{ marginTop: 12 }}>
            <Descriptions.Item label={t('pages.settings.apiToken')}>{me.email}</Descriptions.Item>
            <Descriptions.Item label={t('subscription.totalQuota')}>
              {SizeFormatter.sizeFormat(used)} / {total > 0 ? SizeFormatter.sizeFormat(total) : t('subscription.unlimited')}
            </Descriptions.Item>
            <Descriptions.Item label={t('subscription.expiry')}>
              {me.expire ? new Date(me.expire * 1000).toLocaleDateString() : t('subscription.noExpiry')}
            </Descriptions.Item>
          </Descriptions>
        </div>
      </div>

      <div className="sub-row">
        <div className="sub-row-main">
          <Typography.Text strong>
            <TeamOutlined /> {t('subscription.vnetTitle')}
          </Typography.Text>
          {net.error && <Alert type="error" showIcon title={net.error} />}
          {net.ip && (
            <Typography.Paragraph type="secondary" style={{ marginTop: 8 }}>
              {t('subscription.vnetAddress')}: <Tag>{net.ip}</Tag>
              {net.network && (
                <>
                  {t('subscription.vnetNetwork')}: <Tag>{net.network}</Tag>
                  {t('subscription.vnetRole')}: <Tag>{net.role}</Tag>
                </>
              )}
            </Typography.Paragraph>
          )}
          {net.members && net.members.length > 0 && (
            <Descriptions size="small" column={1}>
              {net.members.map((m) => (
                <Descriptions.Item key={m.id} label={m.name || m.id}>
                  <Tag>{m.ip}</Tag>
                  {m.role} {m.node ? <Tag color="green">{m.node}</Tag> : null}
                </Descriptions.Item>
              ))}
            </Descriptions>
          )}
          {!net.network && (
            <Space direction="vertical" style={{ width: '100%', marginTop: 8 }}>
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  placeholder={t('subscription.vnetNewTitle')}
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                />
                <Button loading={busy} onClick={() => vnet('', { title })}>
                  {t('subscription.vnetCreate')}
                </Button>
              </Space.Compact>
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  placeholder={t('subscription.vnetInviteCode')}
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                />
                <Button loading={busy} onClick={() => vnet('/join', { code })}>
                  {t('subscription.vnetJoin')}
                </Button>
              </Space.Compact>
            </Space>
          )}
          {net.network && (
            <Space direction="vertical" style={{ width: '100%', marginTop: 8 }}>
              <Space.Compact style={{ width: '100%' }}>
                <Input placeholder={t('subscription.vnetInviteCode')} value={invite} readOnly />
                <Button
                  loading={busy}
                  onClick={async () => {
                    try {
                      const out = await api<{ code: string }>('/vnet/invites', {
                        method: 'POST',
                        body: JSON.stringify({ ttl: '24h', uses: 1 }),
                      });
                      setInvite(out.code);
                    } catch (e) {
                      setError(e instanceof Error ? e.message : String(e));
                    }
                  }}
                >
                  {t('subscription.vnetInvite')}
                </Button>
              </Space.Compact>
              <Button size="small" loading={busy} onClick={() => vnet('/leave')}>
                {t('subscription.vnetLeave')}
              </Button>
            </Space>
          )}
          {error && <Alert type="error" showIcon title={error} />}
        </div>
      </div>
    </div>
  );
}
