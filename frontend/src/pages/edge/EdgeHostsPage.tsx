import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router';
import { ConfigProvider, Layout, Tabs } from 'antd';
import { GatewayOutlined, GlobalOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { EmbeddedProvider } from '@/hooks/useEmbedded';
import AppSidebar from '@/layouts/AppSidebar';
import EdgePage from './EdgePage';
import HostsPage from '@/pages/hosts/HostsPage';

// Both pages describe what the internet reaches on 443: the edge routes the
// connection, the hosts table rewrites its names.
export default function EdgeHostsPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const location = useLocation();
  const navigate = useNavigate();
  const active = location.hash === '#hosts' ? 'hosts' : 'edge';

  const pageClass = useMemo(() => {
    const classes = ['edge-hosts-page'];
    if (isDark) classes.push('is-dark');
    if (isUltra) classes.push('is-ultra');
    return classes.join(' ');
  }, [isDark, isUltra]);

  return (
    <ConfigProvider theme={antdThemeConfig}>
      <Layout className={pageClass}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Tabs
              destroyOnHidden
              activeKey={active}
              onChange={(key) =>
                navigate(key === 'hosts' ? '/edge#hosts' : '/edge', { replace: true })
              }
              items={[
                {
                  key: 'edge',
                  icon: <GatewayOutlined />,
                  label: t('menu.edge'),
                  children: (
                    <EmbeddedProvider>
                      <EdgePage />
                    </EmbeddedProvider>
                  ),
                },
                {
                  key: 'hosts',
                  icon: <GlobalOutlined />,
                  label: t('menu.hosts'),
                  children: (
                    <EmbeddedProvider>
                      <HostsPage />
                    </EmbeddedProvider>
                  ),
                },
              ]}
            />
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}
