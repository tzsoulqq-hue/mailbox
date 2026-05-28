import { AlertCircle, KeyRound, Mail, ShieldAlert, Trash2 } from 'lucide-react';
import {
  RecordActionButtons,
  RecordActions,
  RecordCard,
  RecordIdentity,
  RecordList,
  RecordMain,
  RecordMeta,
  RecordTop,
  StatusBadge,
  type RowActionDescriptor
} from '@/dashboard/module-kit';
import { WorkflowActionButton } from '@/dashboard/modules/workflow/sdk';
import { maskEmail, normalizeUiEmail } from './email-utils';
import { authStatus, canRunMailboxAction, domainForEmail, mailboxActions, mailboxProviderValue, providerAction, uniqueStrings } from './mailbox-utils';
import type { Job, Mailbox, MailboxProviderCapability } from './types';

export function MailboxRecordList({ mailboxes, emptyText, providerCapability, showStatus, selected, busy, showSecrets, oauthing, manualRecoverying, runningWorkflowByEmail, onSelect, onOpenWorkflow, onOAuth, onManualRecovery, onDelete }: {
  mailboxes: Mailbox[];
  emptyText: string;
  providerCapability?: MailboxProviderCapability;
  showStatus?: boolean;
  selected?: string;
  busy: boolean;
  showSecrets: boolean;
  oauthing: string;
  manualRecoverying: string;
  runningWorkflowByEmail: Map<string, Job>;
  onSelect: (mailbox: Mailbox) => void;
  onOpenWorkflow: (job: Job) => void;
  onOAuth: (emailAddress?: string) => Promise<void>;
  onManualRecovery: (emailAddress: string) => Promise<void>;
  onDelete: (mailbox: Mailbox) => Promise<void>;
}) {
  return (
    <RecordList className="wideRecordList" emptyText={emptyText}>
      {mailboxes.map((mailbox) => (
        <MailboxCard
          key={mailbox.email_address}
          mailbox={mailbox}
          selected={selected === mailbox.email_address}
          busy={busy}
          showSecrets={showSecrets}
          oauthing={oauthing}
          manualRecoverying={manualRecoverying}
          showStatus={showStatus ?? true}
          providerCapability={providerCapability}
          currentWorkflow={runningWorkflowByEmail.get(normalizeUiEmail(mailbox.email_address))}
          onSelect={onSelect}
          onOpenWorkflow={onOpenWorkflow}
          onOAuth={onOAuth}
          onManualRecovery={onManualRecovery}
          onDelete={onDelete}
        />
      ))}
    </RecordList>
  );
}

export function MailboxDomainGroups(props: {
  mailboxes: Mailbox[];
  configuredDomains: string[];
  providerCapability?: MailboxProviderCapability;
  showStatus?: boolean;
  emptyDomainsText: string;
  emptyDomainText: string;
  selected?: string;
  busy: boolean;
  showSecrets: boolean;
  oauthing: string;
  manualRecoverying: string;
  runningWorkflowByEmail: Map<string, Job>;
  onSelect: (mailbox: Mailbox) => void;
  onOpenWorkflow: (job: Job) => void;
  onOAuth: (emailAddress?: string) => Promise<void>;
  onManualRecovery: (emailAddress: string) => Promise<void>;
  onDelete: (mailbox: Mailbox) => Promise<void>;
}) {
  const configuredDomains = props.configuredDomains.map((domain) => domain.toLowerCase());
  const allDomains = uniqueStrings([...configuredDomains, ...props.mailboxes.map((mailbox) => domainForEmail(mailbox.email_address)).filter(Boolean)]);
  if (allDomains.length === 0) {
    return <div className="p-6 text-center text-sm text-muted-foreground">{props.emptyDomainsText}</div>;
  }
  return (
    <div className="grid min-h-0 flex-1 content-start gap-4 overflow-auto">
      {allDomains.map((domain) => {
        const domainMailboxes = props.mailboxes.filter((mailbox) => domainForEmail(mailbox.email_address) === domain);
        const configured = configuredDomains.includes(domain);
        return (
          <section className="grid gap-2" key={domain}>
            <div className="flex min-h-8 items-center justify-between border-b text-sm">
              <strong>{domain}</strong>
              <span className="text-xs text-muted-foreground">{configured ? '已配置' : '自动归组'}</span>
            </div>
            <MailboxRecordList {...props} mailboxes={domainMailboxes} emptyText={props.emptyDomainText} />
          </section>
        );
      })}
    </div>
  );
}

function MailboxCard({ mailbox, selected, busy, showSecrets, oauthing, manualRecoverying, showStatus, providerCapability, currentWorkflow, onSelect, onOpenWorkflow, onOAuth, onManualRecovery, onDelete }: {
  mailbox: Mailbox;
  selected: boolean;
  busy: boolean;
  showSecrets: boolean;
  oauthing: string;
  manualRecoverying: string;
  showStatus: boolean;
  providerCapability?: MailboxProviderCapability;
  currentWorkflow?: Job;
  onSelect: (mailbox: Mailbox) => void;
  onOpenWorkflow: (job: Job) => void;
  onOAuth: (emailAddress?: string) => Promise<void>;
  onManualRecovery: (emailAddress: string) => Promise<void>;
  onDelete: (mailbox: Mailbox) => Promise<void>;
}) {
  const displayEmail = showSecrets ? mailbox.email_address : maskEmail(mailbox.email_address);
  const isOAuthing = oauthing === mailbox.email_address || oauthing === '*';
  const isManualRecoverying = manualRecoverying === mailbox.email_address;
  const oauthAction = providerAction(providerCapability, mailboxActions.runOAuth);
  const canOAuth = canRunMailboxAction(mailbox, oauthAction);
  const canManualRecovery = mailboxProviderValue(mailbox.provider) === 'outlook';
  const oauthLabel = '补 OAuth';
  const rowActions: RowActionDescriptor[] = [{
    id: 'delete-mailbox',
    label: '删除邮箱',
    icon: <Trash2 className="size-4" />,
    disabled: busy || !!oauthing,
    kind: 'danger',
    onClick: () => void onDelete(mailbox),
  }];

  if (!currentWorkflow && canManualRecovery) {
    rowActions.unshift({
      id: 'manual-recovery',
      label: isManualRecoverying ? '恢复启动中' : '人工恢复',
      icon: <ShieldAlert className="size-4" />,
      disabled: busy || !!manualRecoverying,
      onClick: () => void onManualRecovery(mailbox.email_address),
    });
  }

  if (!currentWorkflow && canOAuth) {
    rowActions.unshift({
      id: 'run-oauth',
      label: isOAuthing ? 'OAuth 提交中' : oauthLabel,
      icon: <KeyRound className="size-4" />,
      disabled: busy || !!oauthing,
      onClick: () => void onOAuth(mailbox.email_address),
    });
  }

  return (
    <RecordCard selected={selected} onClick={() => onSelect(mailbox)}>
      <RecordMain>
        <RecordTop>
          <RecordIdentity
            icon={<Mail className="size-4" />}
            title={<span title={displayEmail}>{displayEmail}</span>}
            subtitle={domainForEmail(mailbox.email_address) || '邮箱'}
          />
          {showStatus && <StatusBadge status={authStatus(mailbox)} />}
        </RecordTop>
        {mailbox.last_error && (
          <RecordMeta className="grid-cols-1">
            <span className="flex min-w-0 items-center gap-1 truncate text-xs text-destructive" title={mailbox.last_error}>
              <AlertCircle className="size-3.5 shrink-0" />
              {mailbox.last_error}
            </span>
          </RecordMeta>
        )}
      </RecordMain>
      <RecordActions className="rowActions">
        <div className="rowActionsMain">
          {currentWorkflow && <WorkflowActionButton job={currentWorkflow} onOpen={onOpenWorkflow} />}
          <RecordActionButtons actions={rowActions} />
        </div>
      </RecordActions>
    </RecordCard>
  );
}
