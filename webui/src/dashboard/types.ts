import type { EmailInboxMessage, EmailMailbox, EmailSignal, FetchMailboxInboxResult } from '@/proto/email';
import type { FetchMailboxInboxesResponse, MailboxDomain } from '@/proto/mailbox_service';
import type { Job, JobSnapshot } from '@/dashboard/modules/workflow/sdk';

export type { EmailSignal, Job, JobSnapshot, MailboxDomain };

export type MailboxProviderActionCapability = {
  action: string | number;
  required_mailbox_fields?: string[];
  required_auth_statuses?: string[];
  bulk_supported?: boolean;
};

export type MailboxProviderCapability = {
  provider: string | number;
  key: string;
  display_name: string;
  actions: MailboxProviderActionCapability[];
  retention_policy?: {
    scope: string | number;
    max_messages: number;
  };
};

export type Mailbox = EmailMailbox;

export type InboxMessage = EmailInboxMessage & {
  otp?: string;
};

export type InboxResult = Omit<FetchMailboxInboxResult, 'mailbox' | 'messages'> & {
  mailbox?: Mailbox;
  messages?: InboxMessage[];
};

export type InboxResponse = Omit<FetchMailboxInboxesResponse, 'results' | 'operation_id'> & {
  operation_id?: string;
  results?: InboxResult[];
};

export type LatestOtp = {
  otp: string;
  subject: string;
  received_at_unix: number;
};

export type DisplayLabelMap = Record<string, string>;
