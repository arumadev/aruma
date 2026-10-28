import type { ReactNode } from 'react';
import { DocumentsSection } from '@/features/documents/DocumentsSection';

export default function DocumentsLayout({ children }: { children: ReactNode }) {
  return <DocumentsSection>{children}</DocumentsSection>;
}
