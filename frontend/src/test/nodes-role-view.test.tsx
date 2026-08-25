import { fireEvent, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, test, vi } from 'vitest';

import NodesPage from '@/pages/nodes/NodesPage';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

afterEach(() => {
  vi.restoreAllMocks();
});

function mockPanel() {
  vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) => {
    if (url.includes('/api/nodes/list')) {
      return new Msg(true, '', [
        {
          id: 1,
          name: 'node-msk1',
          enable: true,
          status: 'online',
          scheme: 'https',
          address: 'msk1.example.com',
          port: 2053,
        },
      ]);
    }
    if (url.includes('/api/peers/list')) {
      return new Msg(true, '', [
        { id: 2, name: 'master-fi1', subDomain: 'fi1.example.com', subPort: 2096, enable: true },
      ]);
    }
    if (url.includes('/api/peers/identity')) {
      return new Msg(true, '', { alg: 'ed25519', key: 'ab12' });
    }
    return new Msg(true, '', null);
  });
  vi.spyOn(HttpUtil, 'post').mockImplementation(async () => new Msg(true, '', {}));
}

function renderNodes(path: string) {
  mockPanel();
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <NodesPage />
    </MemoryRouter>,
  );
}

test('shows managed nodes by default and masters under the role param', async () => {
  const nodesView = renderNodes('/nodes');
  expect(await screen.findByText('node-msk1')).not.toBeNull();
  expect(screen.queryByText('master-fi1')).toBeNull();
  nodesView.unmount();

  renderNodes('/nodes?role=master');
  expect(await screen.findByText('master-fi1')).not.toBeNull();
  expect(screen.queryByText('node-msk1')).toBeNull();
});

test('the role switch moves between the two halves of one page', async () => {
  renderNodes('/nodes');
  expect(await screen.findByText('node-msk1')).not.toBeNull();

  fireEvent.click(screen.getByText('Master panels'));

  expect(await screen.findByText('master-fi1')).not.toBeNull();
  await waitFor(() => expect(screen.queryByText('node-msk1')).toBeNull());
});
