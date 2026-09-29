import type { ReactNode } from 'react';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { useTemplates } from '@/api/queries/useTemplates';
import { makeTestQueryClient } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';

afterEach(() => {
  vi.restoreAllMocks();
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeTestQueryClient()}>{children}</QueryClientProvider>;
}

// The server binds these bodies as JSON; sent as a form, applying a template
// failed with "invalid character 'o' in literal null".
describe('inbound templates', () => {
  it('posts apply and create as JSON', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', []));
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', []));
    const { result } = renderHook(() => useTemplates(), { wrapper });
    await waitFor(() => expect(result.current.loading).toBe(false));

    await result.current.apply(1, [10, 11]);
    await result.current.create('RU entry', '', [44, 45]);

    const json = { headers: { 'Content-Type': 'application/json' } };
    expect(post).toHaveBeenCalledWith('/panel/api/templates/apply/1', { nodeIds: [10, 11] }, json);
    expect(post).toHaveBeenCalledWith(
      '/panel/api/templates/fromInbounds',
      { name: 'RU entry', remark: '', inboundIds: [44, 45] },
      json,
    );
  });
});
