import { useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation } from 'react-router';
import { Alert, Button, Card, ConfigProvider, Layout, Space, Spin, Tabs, message } from 'antd';
import { BellOutlined, DiscordOutlined, MailOutlined, MessageOutlined } from '@ant-design/icons';

import { useTheme } from '@/hooks/useTheme';
import AppSidebar from '@/layouts/AppSidebar';
import { setMessageInstance } from '@/utils/messageBus';
import { useAllSettings } from '@/api/queries/useAllSettings';
import TelegramTab from '@/pages/settings/TelegramTab';
import EmailTab from '@/pages/settings/EmailTab';
import DiscordTab from '@/pages/settings/DiscordTab';

const SLUGS = ['telegram', 'email', 'discord'] as const;
type Slug = (typeof SLUGS)[number];

export default function NotificationsPage() {
  const { t } = useTranslation();
  const { isDark, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  const location = useLocation();

  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const { allSetting, updateSetting, fetched, spinning, saveDisabled, saveAll } = useAllSettings();

  const slug = location.hash.replace(/^#/, '');
  const active = (SLUGS as readonly string[]).includes(slug) ? (slug as Slug) : 'telegram';

  const items = useMemo(
    () => [
      {
        key: 'telegram',
        icon: <MessageOutlined />,
        label: t('pages.settings.TGBotSettings'),
        children: <TelegramTab allSetting={allSetting} updateSetting={updateSetting} />,
      },
      {
        key: 'email',
        icon: <MailOutlined />,
        label: t('pages.settings.emailSettings'),
        children: <EmailTab allSetting={allSetting} updateSetting={updateSetting} />,
      },
      {
        key: 'discord',
        icon: <DiscordOutlined />,
        label: t('pages.settings.discordSettings'),
        children: <DiscordTab allSetting={allSetting} updateSetting={updateSetting} />,
      },
    ],
    [allSetting, t, updateSetting],
  );

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContextHolder}
      <Layout className={isDark ? 'notifications-page is-dark' : 'notifications-page'}>
        <AppSidebar />
        <Layout className="content-shell">
          <Layout.Content id="content-layout" className="content-area">
            <Spin
              spinning={spinning || !fetched}
              delay={200}
              description={t('loading')}
              size="large"
            >
              {!fetched ? (
                <div className="loading-spacer" />
              ) : (
                <Card
                  size="small"
                  title={
                    <Space>
                      <BellOutlined />
                      {t('pages.notifications.title')}
                    </Space>
                  }
                  extra={
                    <Button type="primary" disabled={saveDisabled} onClick={() => void saveAll()}>
                      {t('pages.settings.save')}
                    </Button>
                  }
                >
                  <Alert
                    type="info"
                    showIcon
                    message={t('pages.notifications.intro')}
                    style={{ marginBottom: 12 }}
                  />
                  <Tabs
                    items={items}
                    activeKey={active}
                    onChange={(key) => {
                      window.location.hash = key;
                    }}
                  />
                </Card>
              )}
            </Spin>
          </Layout.Content>
        </Layout>
      </Layout>
    </ConfigProvider>
  );
}
