import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router';
import { ConfigProvider, Layout, Tabs } from 'antd';
import { TagsOutlined, TeamOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import { EmbeddedProvider } from '@/hooks/useEmbedded';
import AppSidebar from '@/layouts/AppSidebar';
import ClientsPage from './ClientsPage';
import GroupsPage from '@/pages/groups/GroupsPage';

// Clients and groups are one area of the sidebar: both are about who has
// access, and a group is only an organisation of clients.
export default function ClientsGroupsPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const location = useLocation();
  const navigate = useNavigate();
  const active = location.hash === '#groups' ? 'groups' : 'clients';

  const pageClass = useMemo(() => {
    const classes = ['clients-groups-page'];
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
                navigate(key === 'groups' ? '/clients#groups' : '/clients', { replace: true })
              }
              items={[
                {
                  key: 'clients',
                  icon: <TeamOutlined />,
                  label: t('menu.clients'),
                  children: (
                    <EmbeddedProvider>
                      <ClientsPage />
                    </EmbeddedProvider>
                  ),
                },
                {
                  key: 'groups',
                  icon: <TagsOutlined />,
                  label: t('menu.groups'),
                  children: (
                    <EmbeddedProvider>
                      <GroupsPage />
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
