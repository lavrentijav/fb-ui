import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Card, ConfigProvider, Layout, Progress, Tabs, message } from 'antd';
import type { TabsProps } from 'antd';
import {
  AppstoreOutlined,
  ClockCircleOutlined,
  CustomerServiceOutlined,
  LinkOutlined,
  SettingOutlined,
  UnorderedListOutlined,
} from '@ant-design/icons';

import { ClipboardManager, LanguageManager, SizeFormatter } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';
import { useTheme } from '@/hooks/useTheme';
import SubAccountTab from './SubAccountTab';
import SubAppsTab from './SubAppsTab';
import SubConfigsTab from './SubConfigsTab';
import SubHeader from './SubHeader';
import SubHero from './SubHero';
import SubLinksTab from './SubLinksTab';
import { buildSubApps, daysUntil, detectPlatform, resolveSubStatus } from './subPageModel';
import './SubPage.css';

const subData = window.__SUB_PAGE_DATA__ || {};

const sId = subData.sId || '';
const subUrl = subData.subUrl || '';
const subJsonUrl = subData.subJsonUrl || '';
const subClashUrl = subData.subClashUrl || '';
const subTitle = subData.subTitle || '';
const subSupportUrl = subData.subSupportUrl || '';
const updateHours = Number(subData.subUpdates || 0);
const announce = subData.announce || '';
const announceUrl = subData.announceUrl || '';
const announceColor = subData.announceColor || '';
const announceButton = subData.announceButtonText
  ? { text: subData.announceButtonText, link: subData.announceButtonLink || '' }
  : null;
const webPageUrl = subData.webPageUrl || '';
const refillDate = Number(subData.refillDate || 0);
const hwidLimit = Number(subData.hwidLimit || 0);
const backupUrls: string[] = Array.isArray(subData.backupUrls) ? subData.backupUrls : [];
const servers: { name: string; upload: number; download: number }[] = Array.isArray(subData.servers)
  ? subData.servers
  : [];
const links: string[] = Array.isArray(subData.links) ? subData.links : [];
const linkEmails: string[] = Array.isArray(subData.emails) ? subData.emails : [];
const totalByte = Number(subData.totalByte || 0);
const usedByte =
  Number(subData.usedByte || 0) ||
  Number(subData.downloadByte || 0) + Number(subData.uploadByte || 0);
const expireMs = Number(subData.expire || 0) * 1000;
const clientEmail = [...new Set(linkEmails.filter(Boolean))].join(', ');
const loadedAt = Date.now();

const heroData = {
  status: resolveSubStatus({ enabled: !!subData.enabled, usedByte, totalByte, expireMs }, loadedAt),
  daysLeft: daysUntil(expireMs, loadedAt),
  usedByte,
  totalByte,
  expireMs,
  lastOnlineMs: Number(subData.lastOnline || 0),
  download: subData.download || '0',
  upload: subData.upload || '0',
  used: subData.used || '0',
  total: subData.total || '∞',
  remained: subData.remained || '',
  datepicker: subData.datepicker || 'gregorian',
};

const apps = buildSubApps({ subUrl, sId, subTitle });
const initialPlatform = detectPlatform(navigator.userAgent);
const RTL_LANGUAGES = new Set(['fa-IR', 'ar-EG']);

// The banner colour is whatever the operator wrote; map the common names onto
// antd's alert types and leave an unknown value as information.
function announceType(color: string): 'info' | 'success' | 'warning' | 'error' {
  switch (color.toLowerCase()) {
    case 'green':
    case 'success':
      return 'success';
    case 'red':
    case 'danger':
    case 'warning':
      return 'warning';
    case 'yellow':
      return 'warning';
    default:
      return 'info';
  }
}

// The sub page runs its own violet accent, so every antd control on it picks the
// hue up instead of the panel blue useTheme pins. Mirrored in SubPage.css.
const ACCENT = {
  light: {
    primary: '#7c3aed',
    hover: '#8b5cf6',
    active: '#6d28d9',
    rail: 'rgba(124, 58, 237, 0.16)',
  },
  dark: {
    primary: '#a78bfa',
    hover: '#c4b5fd',
    active: '#8b5cf6',
    rail: 'rgba(167, 139, 250, 0.18)',
  },
};

export default function SubPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);
  const [lang, setLang] = useState<string>(() => LanguageManager.getLanguage('subscription'));

  const onLangChange = useCallback((next: string) => {
    setLang(next);
    LanguageManager.setLanguage(next, 'subscription');
  }, []);

  const copy = useCallback(
    async (value: string, toast?: string) => {
      if (!value) return;
      const ok = await ClipboardManager.copyText(value);
      if (ok) messageApi.success(toast ?? t('copied'));
    },
    [t, messageApi],
  );

  const open = useCallback((url: string) => {
    if (url) window.open(url, '_blank');
  }, []);

  const tabs = useMemo(() => {
    const items: NonNullable<TabsProps['items']> = [];
    if (subUrl || subJsonUrl || subClashUrl) {
      items.push({
        key: 'subscription',
        icon: <LinkOutlined />,
        label: t('subscription.tabLinks'),
        children: (
          <SubLinksTab
            subUrl={subUrl}
            subJsonUrl={subJsonUrl}
            subClashUrl={subClashUrl}
            backupUrls={backupUrls}
            onCopy={copy}
          />
        ),
      });
    }
    if (subUrl) {
      items.push({
        key: 'apps',
        icon: <AppstoreOutlined />,
        label: t('subscription.tabApps'),
        children: <SubAppsTab apps={apps} initialPlatform={initialPlatform} onOpen={open} />,
      });
    }
    if (links.length > 0) {
      items.push({
        key: 'configs',
        icon: <UnorderedListOutlined />,
        label: (
          <>
            {t('subscription.tabConfigs')}
            <span className="sub-tab-count">{links.length}</span>
          </>
        ),
        children: <SubConfigsTab links={links} onCopy={copy} />,
      });
    }
    // management exists only where the master provides the API (the native one)
    if (window.__SUB_MANAGE__) {
      items.push({
        key: 'account',
        icon: <SettingOutlined />,
        label: t('subscription.tabAccount'),
        children: <SubAccountTab />,
      });
    }
    return items;
  }, [t, copy, open]);

  const direction = RTL_LANGUAGES.has(lang) ? 'rtl' : 'ltr';
  const pageClass = ['subscription-page', isDark && 'is-dark', isUltra && 'is-ultra']
    .filter(Boolean)
    .join(' ');

  const themeConfig = useMemo(() => {
    const accent = isDark ? ACCENT.dark : ACCENT.light;
    const primary = {
      colorPrimary: accent.primary,
      colorPrimaryHover: accent.hover,
      colorPrimaryActive: accent.active,
    };
    return {
      ...antdThemeConfig,
      token: {
        ...antdThemeConfig.token,
        ...primary,
        colorLink: accent.primary,
        colorInfo: accent.primary,
      },
      components: {
        ...antdThemeConfig.components,
        Button: { ...antdThemeConfig.components?.Button, ...primary },
        Progress: { ...antdThemeConfig.components?.Progress, remainingColor: accent.rail },
      },
    };
  }, [antdThemeConfig, isDark]);

  return (
    <ConfigProvider theme={themeConfig} direction={direction}>
      {messageContextHolder}
      <Layout className={pageClass} dir={direction}>
        <div className="sub-aurora" aria-hidden="true">
          <span className="sub-aurora-grid" />
        </div>
        <Layout.Content className="sub-content">
          <Card className="sub-card">
            <SubHeader
              title={subTitle}
              sId={sId}
              email={clientEmail}
              lang={lang}
              onLangChange={onLangChange}
            />
            {announce && (
              <Alert
                type={announceType(announceColor)}
                showIcon
                title={
                  announceUrl ? (
                    <a href={announceUrl} target="_blank" rel="noopener noreferrer">
                      {announce}
                    </a>
                  ) : (
                    announce
                  )
                }
                action={
                  announceButton?.link ? (
                    <Button
                      size="small"
                      href={announceButton.link}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      {announceButton.text}
                    </Button>
                  ) : undefined
                }
                className="sub-announce"
              />
            )}
            <SubHero {...heroData} lang={lang} />
            {servers.length > 0 && (
              <div className="sub-servers">
                <div className="sub-servers-title">{t('subscription.serversTitle')}</div>
                {servers.map((s) => {
                  const used = s.upload + s.download;
                  const pct = totalByte > 0 ? Math.min(100, (used / totalByte) * 100) : 0;
                  return (
                    <div key={s.name} className="sub-server" dir="ltr">
                      <span className="sub-server-name" title={s.name}>
                        {s.name}
                      </span>
                      <Progress
                        percent={pct}
                        showInfo={false}
                        size="small"
                        className="sub-server-bar"
                      />
                      <span className="sub-server-used">
                        {SizeFormatter.sizeFormat(used)}
                        {totalByte > 0 ? ` / ${SizeFormatter.sizeFormat(totalByte)}` : ''}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
            {tabs.length > 0 && <Tabs className="sub-tabs" tabBarGutter={24} items={tabs} />}
            {(updateHours > 0 ||
              subSupportUrl ||
              webPageUrl ||
              hwidLimit > 0 ||
              refillDate > 0) && (
              <footer className="sub-footer">
                {updateHours > 0 && (
                  <span>
                    <ClockCircleOutlined />
                    {t('subscription.updateInterval', { hours: updateHours })}
                  </span>
                )}
                {subSupportUrl && (
                  <a href={subSupportUrl} target="_blank" rel="noopener noreferrer">
                    <CustomerServiceOutlined />
                    {t('subscription.support')}
                  </a>
                )}
                {webPageUrl && (
                  <a href={webPageUrl} target="_blank" rel="noopener noreferrer">
                    <LinkOutlined />
                    {t('subscription.webPage')}
                  </a>
                )}
                {hwidLimit > 0 && <span>{t('subscription.hwidLimit', { count: hwidLimit })}</span>}
                {refillDate > 0 && (
                  <span>
                    {t('subscription.refillDate')}:{' '}
                    {new Date(refillDate * 1000).toLocaleDateString()}
                  </span>
                )}
              </footer>
            )}
          </Card>
        </Layout.Content>
      </Layout>
    </ConfigProvider>
  );
}
