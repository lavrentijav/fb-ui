import { createContext, useContext } from 'react';
import type { ReactNode } from 'react';

// Embedded tells a page it is rendered inside another page's tabs, so it must
// not draw its own sidebar or content padding.
const EmbeddedContext = createContext(false);

export function EmbeddedProvider({ children }: { children: ReactNode }) {
  return <EmbeddedContext.Provider value={true}>{children}</EmbeddedContext.Provider>;
}

export function useEmbedded(): boolean {
  return useContext(EmbeddedContext);
}
