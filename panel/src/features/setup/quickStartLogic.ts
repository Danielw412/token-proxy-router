export interface QuickStartProgress {
  accounts: number;
  exposed: boolean;
  connected: boolean;
}

export const countDone = (progress: QuickStartProgress): number =>
  [progress.accounts > 0, progress.exposed, progress.connected].filter(Boolean).length;
