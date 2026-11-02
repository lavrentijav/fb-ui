import { lazy, Suspense } from 'react';
import { createBrowserRouter, Navigate, type RouteObject } from 'react-router';
import { Spin } from 'antd';

import PanelLayout from '@/layouts/PanelLayout';

const IndexPage = lazy(() => import('@/pages/index/IndexPage'));
const InboundsPage = lazy(() => import('@/pages/inbounds/InboundsPage'));
const ClientsPage = lazy(() => import('@/pages/clients/ClientsPage'));
const GroupsPage = lazy(() => import('@/pages/groups/GroupsPage'));
const SubscriptionsPage = lazy(() => import('@/pages/subscriptions/SubscriptionsPage'));
const VirtualNetworksPage = lazy(() => import('@/pages/vnet/VirtualNetworksPage'));
const NotificationsPage = lazy(() => import('@/pages/notifications/NotificationsPage'));
const WebhooksPage = lazy(() => import('@/pages/webhooks/WebhooksPage'));
const AuditPage = lazy(() => import('@/pages/audit/AuditPage'));
const NodesPage = lazy(() => import('@/pages/nodes/NodesPage'));
const NetworkPage = lazy(() => import('@/pages/network/NetworkPage'));
const FiltersPage = lazy(() => import('@/pages/filters/FiltersPage'));
const HostsPage = lazy(() => import('@/pages/hosts/HostsPage'));
const CertsPage = lazy(() => import('@/pages/certs/CertsPage'));
const EdgePage = lazy(() => import('@/pages/edge/EdgePage'));
const TemplatesPage = lazy(() => import('@/pages/templates/TemplatesPage'));
const SettingsPage = lazy(() => import('@/pages/settings/SettingsPage'));
const XrayPage = lazy(() => import('@/pages/xray/XrayPage'));
const ApiDocsPage = lazy(() => import('@/pages/api-docs/ApiDocsPage'));

function withSuspense(node: React.ReactNode) {
  return (
    <Suspense
      fallback={
        <div
          style={{
            display: 'flex',
            justifyContent: 'center',
            alignItems: 'center',
            minHeight: '60vh',
          }}
        >
          <Spin size="large" />
        </div>
      }
    >
      {node}
    </Suspense>
  );
}

const routes: RouteObject[] = [
  {
    path: '/',
    element: <PanelLayout />,
    children: [
      { index: true, element: withSuspense(<IndexPage />) },
      { path: 'inbounds', element: withSuspense(<InboundsPage />) },
      { path: 'clients', element: withSuspense(<ClientsPage />) },
      { path: 'groups', element: withSuspense(<GroupsPage />) },
      { path: 'subscriptions', element: withSuspense(<SubscriptionsPage />) },
      { path: 'vnet', element: withSuspense(<VirtualNetworksPage />) },
      { path: 'nodes', element: withSuspense(<NodesPage />) },
      { path: 'peers', element: <Navigate to="/nodes?role=master" replace /> },
      { path: 'cascades', element: withSuspense(<NetworkPage />) },
      { path: 'network', element: <Navigate to="/cascades" replace /> },
      { path: 'filters', element: withSuspense(<FiltersPage />) },
      { path: 'hosts', element: withSuspense(<HostsPage />) },
      { path: 'certs', element: withSuspense(<CertsPage />) },
      { path: 'edge', element: withSuspense(<EdgePage />) },
      { path: 'templates', element: withSuspense(<TemplatesPage />) },
      { path: 'settings', element: withSuspense(<SettingsPage />) },
      { path: 'notifications', element: withSuspense(<NotificationsPage />) },
      { path: 'webhooks', element: withSuspense(<WebhooksPage />) },
      { path: 'audit', element: withSuspense(<AuditPage />) },
      { path: 'xray', element: withSuspense(<XrayPage />) },
      { path: 'outbound', element: <Navigate to="/xray#outbound" replace /> },
      { path: 'routing', element: <Navigate to="/xray#routing" replace /> },
      { path: 'api-docs', element: withSuspense(<ApiDocsPage />) },
    ],
  },
];

function computeBasename() {
  const raw = (typeof window !== 'undefined' && window.X_UI_BASE_PATH) || '/';
  const trimmed = raw.replace(/\/+$/, '');
  return `${trimmed}/panel`;
}

export const router = createBrowserRouter(routes, {
  basename: computeBasename(),
});
