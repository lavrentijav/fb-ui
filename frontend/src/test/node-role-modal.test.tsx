import { fireEvent, screen, waitFor } from '@testing-library/react';
import { expect, test, vi } from 'vitest';

import NodeRoleModal from '@/pages/nodes/NodeRoleModal';
import { Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

function renderModal(role: 'node' | 'master') {
  const save = vi.fn(async () => new Msg(true, '', {}));
  renderWithProviders(
    <NodeRoleModal
      open
      target={role}
      record={{
        id: 7,
        name: 'msk1',
        scheme: 'https',
        address: 'msk1.example.com',
        port: 2053,
        subDomain: role === 'node' ? 'sub-msk1.example.com' : '',
      }}
      save={save}
      onOpenChange={() => {}}
    />,
  );
  return save;
}

function submit() {
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
}

// The row already knows one address for the panel; asking the operator to
// retype it as the other role's host is what a delete-and-recreate would do.
test('a node becomes a master with its panel address prefilled as the sub host', async () => {
  const save = renderModal('master');
  submit();
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith(
      7,
      expect.objectContaining({
        role: 'master',
        subDomain: 'msk1.example.com',
        subPort: 2096,
        subPath: '/sub/',
      }),
    ),
  );
});

// A node is reached with a credential, so submitting without one has to stop at
// the form rather than produce a node the panel cannot talk to.
test('a master cannot become a node without a credential', async () => {
  const save = renderModal('node');
  submit();
  await waitFor(() => expect(screen.getByText('Fill in what the new role needs')).not.toBeNull());
  expect(save).not.toHaveBeenCalled();

  fireEvent.change(screen.getByPlaceholderText("Token from the remote panel's Settings page"), {
    target: { value: 't0ken' },
  });
  submit();
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith(
      7,
      expect.objectContaining({ role: 'node', address: 'msk1.example.com', apiToken: 't0ken' }),
    ),
  );
});
