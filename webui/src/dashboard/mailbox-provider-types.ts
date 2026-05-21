import type { Job, Mailbox, MailboxDomain, MailboxProviderCapability } from './types';

export type MailboxProviderPanelProps = {
  mailboxes: Mailbox[];
  domains: MailboxDomain[];
  capability?: MailboxProviderCapability;
  selected?: string;
  busy: boolean;
  showSecrets: boolean;
  oauthing: string;
  inboxLoading: boolean;
  domainSyncing: boolean;
  runningWorkflowByEmail: Map<string, Job>;
  onSelect: (mailbox: Mailbox) => void;
  onOpenWorkflow: (job: Job) => void;
  onOAuth: (emailAddress?: string) => Promise<void>;
  onFetchInbox: () => Promise<void>;
  onSyncDomains: () => Promise<void>;
  onToggleSecrets: () => void;
  onDelete: (mailbox: Mailbox) => Promise<void>;
  onDone: (message: string) => void;
  onError: (message: string) => void;
};
