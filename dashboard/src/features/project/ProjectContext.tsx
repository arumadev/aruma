'use client';

import { createContext, useContext } from 'react';
import type { Project } from '@/lib/api/types';

export interface ProjectContextValue {
  project: Project;
  /** Replaces the project after a write that returned it. */
  setProject: (project: Project) => void;
}

export const ProjectContext = createContext<ProjectContextValue | null>(null);

/** The project of the current /project/* page. */
export function useProject(): ProjectContextValue {
  const value = useContext(ProjectContext);
  if (!value) throw new Error('useProject must be used inside ProjectFrame');
  return value;
}
