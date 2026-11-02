/** "Show emails" is global: emails stay blurred everywhere until it is turned on. */

import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface PrivacyState {
  showEmails: boolean;
  setShowEmails: (value: boolean) => void;
}

export const usePrivacyStore = create<PrivacyState>()(
  persist(
    (set) => ({
      showEmails: false,
      setShowEmails: (showEmails) => set({ showEmails }),
    }),
    { name: 'tpr-privacy' }
  )
);
