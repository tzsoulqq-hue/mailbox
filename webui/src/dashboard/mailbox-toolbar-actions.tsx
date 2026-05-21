import { Eye, EyeOff, Inbox, KeyRound, Plus, RefreshCcw } from 'lucide-react';
import type { ToolbarActionDescriptor } from '@/dashboard/module-kit';
import { actionKey, bulkMailboxActionCount, mailboxActions, type MailboxActionKey, type MailboxProviderTab } from './mailbox-utils';
import type { Mailbox, MailboxProviderActionCapability, MailboxProviderCapability } from './types';

type ProviderToolbarView = {
  value: MailboxProviderTab;
  capability?: MailboxProviderCapability;
  mailboxes: Mailbox[];
};

type ProviderToolbarProps = {
  busy: boolean;
  showSecrets: boolean;
  oauthing: string;
  inboxLoading: boolean;
  domainSyncing: boolean;
  onOAuth: (emailAddress?: string) => Promise<void>;
  onFetchInbox: () => Promise<void>;
  onSyncDomains: () => Promise<void>;
  onToggleSecrets: () => void;
};

type ToolbarActionFactory = (ctx: {
  action: MailboxProviderActionCapability;
  view: ProviderToolbarView;
  props: ProviderToolbarProps;
  openImport: (provider: MailboxProviderTab) => void;
}) => ToolbarActionDescriptor;

const toolbarActionFactories: Partial<Record<MailboxActionKey, ToolbarActionFactory>> = {
  [mailboxActions.importMailbox]: ({ view, openImport }) => ({
    id: 'import-mailbox',
    label: '导入邮箱',
    icon: <Plus className="size-4" />,
    onClick: () => openImport(view.value),
  }),
  [mailboxActions.runOAuth]: ({ action, view, props }) => {
    const count = bulkMailboxActionCount(view.mailboxes, action);
    return {
      id: 'run-oauth',
      label: '补 OAuth',
      icon: <KeyRound className="size-4" />,
      disabled: props.busy || !!props.oauthing || count === 0,
      onClick: () => void props.onOAuth(),
    };
  },
  [mailboxActions.fetchInbox]: ({ action, view, props }) => {
    const count = bulkMailboxActionCount(view.mailboxes, action);
    return {
      id: 'fetch-inbox',
      label: props.inboxLoading ? '收信中' : '拉取邮箱',
      icon: <Inbox className="size-4" />,
      disabled: props.busy || props.inboxLoading || count === 0,
      onClick: () => void props.onFetchInbox(),
    };
  },
  [mailboxActions.syncDomains]: ({ props }) => ({
    id: 'sync-domains',
    label: props.domainSyncing ? '同步中' : '同步域名',
    icon: <RefreshCcw className="size-4" />,
    disabled: props.busy || props.domainSyncing,
    onClick: () => void props.onSyncDomains(),
  }),
};

export function providerToolbarActions(view: ProviderToolbarView, props: ProviderToolbarProps, openImport: (provider: MailboxProviderTab) => void) {
  const actions = (view.capability?.actions || [])
    .map((action) => {
      const key = actionKey(action.action);
      return key ? toolbarActionFactories[key]?.({ action, view, props, openImport }) : undefined;
    })
    .filter((action): action is ToolbarActionDescriptor => !!action);
  return [...actions, secretsAction(props)];
}

function secretsAction(props: ProviderToolbarProps): ToolbarActionDescriptor {
  return {
    id: 'toggle-secrets',
    label: props.showSecrets ? '隐藏敏感信息' : '显示敏感信息',
    icon: props.showSecrets ? <EyeOff className="size-4" /> : <Eye className="size-4" />,
    onClick: props.onToggleSecrets,
  };
}
