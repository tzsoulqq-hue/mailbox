import { useEffect, useState } from 'react';
import { Trash2 } from 'lucide-react';
import {
  ActionButtonGroup,
  KVList,
  StatusBadge,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger
} from '@/dashboard/module-kit';
import type { ActionButtonDescriptor, KVDescriptor } from '@/dashboard/module-kit';
import {
  mask,
} from '@/dashboard/module-kit';
import { maskEmail } from './email-utils';
import { mailboxStatusText } from './labels';
import { MailboxInboxSection } from './mailbox-inbox';
import { MailboxOtpPanel } from './otp-panel';
import { latestOtpForEmail } from './mailbox-signal-utils';
import { authStatus, mailboxProviderConfig, tokenText } from './mailbox-utils';
import type { InboxResult, LatestOtp, Mailbox } from './types';

export function MailboxDetails({ mailbox, showSecrets, inboxResult, inboxLoading, canFetchInbox, onCopy, onFetchInbox, onDelete }: {
  mailbox: Mailbox;
  showSecrets: boolean;
  inboxResult?: InboxResult | null;
  inboxLoading: boolean;
  canFetchInbox: boolean;
  onCopy: (label: string, value: string) => void;
  onFetchInbox: (emailAddress?: string) => Promise<void>;
  onDelete: (mailbox: Mailbox) => Promise<void>;
}) {
  const [activeTab, setActiveTab] = useState<'overview' | 'inbox'>('overview');
  const inboxMessageCount = inboxResult?.messages?.length || 0;
  const latestOtp = latestOtpForEmail(inboxResult ? {
    results: [inboxResult],
    mailbox_count: 1,
    fetched_count: 1,
    failed_count: inboxResult.error_message ? 1 : 0,
    message_count: inboxMessageCount
  } : null, [], mailbox.email_address);

  useEffect(() => {
    setActiveTab('overview');
  }, [mailbox.email_address]);

  return (
    <Tabs value={activeTab} onValueChange={(value) => setActiveTab(value as 'overview' | 'inbox')} className="min-h-0 flex-1">
      <TabsList variant="line" className="w-full">
        <TabsTrigger value="overview">概览</TabsTrigger>
        <TabsTrigger value="inbox">收件箱 {inboxMessageCount}</TabsTrigger>
      </TabsList>
      <TabsContent value="overview" className="min-h-0 overflow-auto">
        <MailboxOverview mailbox={mailbox} showSecrets={showSecrets} latestOtp={latestOtp} onCopy={onCopy} onDelete={onDelete} />
      </TabsContent>
      <TabsContent value="inbox" className="min-h-0 overflow-auto">
        <MailboxInboxSection mailbox={mailbox} result={inboxResult} showSecrets={showSecrets} loading={inboxLoading} canFetch={canFetchInbox} onFetch={onFetchInbox} />
      </TabsContent>
    </Tabs>
  );
}

function MailboxOverview({ mailbox, showSecrets, latestOtp, onCopy, onDelete }: {
  mailbox: Mailbox;
  showSecrets: boolean;
  latestOtp: LatestOtp | null;
  onCopy: (label: string, value: string) => void;
  onDelete: (mailbox: Mailbox) => Promise<void>;
}) {
  const providerConfig = mailboxProviderConfig(mailbox.provider);
  const fields: KVDescriptor[] = [{
    id: 'email',
    label: '邮箱',
    value: showSecrets ? mailbox.email_address : maskEmail(mailbox.email_address),
    copyValue: mailbox.email_address,
    copyDisabled: !mailbox.email_address,
    masked: !showSecrets,
  }];
  if (providerConfig.showStatus) fields.push({
    id: 'password',
    label: '密码',
    value: showSecrets ? mailbox.password : mask(mailbox.password),
    copyValue: mailbox.password,
    copyDisabled: !mailbox.password,
    masked: !showSecrets,
    mono: true,
  }, {
    id: 'oauth',
    label: 'OAuth',
    value: mailboxStatusText(authStatus(mailbox)),
  }, {
    id: 'token',
    label: 'Token',
    value: tokenText(mailbox),
  }, {
    id: 'refresh-token',
    label: 'Refresh',
    value: showSecrets ? mailbox.refresh_token : mask(mailbox.refresh_token),
    copyValue: mailbox.refresh_token,
    copyDisabled: !mailbox.refresh_token,
    masked: !showSecrets,
    mono: true,
  }, {
    id: 'access-token',
    label: 'Access',
    value: showSecrets ? mailbox.access_token : mask(mailbox.access_token),
    copyValue: mailbox.access_token,
    copyDisabled: !mailbox.access_token,
    masked: !showSecrets,
    mono: true,
  });
  fields.push({
    id: 'latest-otp',
    label: '验证码',
    value: showSecrets ? (latestOtp?.otp || '-') : mask(latestOtp?.otp || ''),
    copyValue: latestOtp?.otp || '',
    copyDisabled: !latestOtp?.otp,
    masked: !showSecrets,
    mono: true,
  });
  const actions: ActionButtonDescriptor[] = [{
    id: 'delete-mailbox',
    label: '删除邮箱',
    icon: <Trash2 />,
    variant: 'destructive',
    onClick: () => void onDelete(mailbox),
  }];

  return (
    <section className="grid gap-3">
      <div className="grid gap-2 rounded-lg border p-3">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <strong className="block truncate text-sm">{showSecrets ? mailbox.email_address : maskEmail(mailbox.email_address)}</strong>
          </div>
          {providerConfig.showStatus && <StatusBadge status={authStatus(mailbox)} />}
        </div>
        <MailboxOtpPanel latestOtp={latestOtp} showSecrets={showSecrets} loading={false} onCopy={onCopy} />
      </div>
      <div>
        <KVList items={fields} onCopy={onCopy} />
      </div>
      <ActionButtonGroup actions={actions} />
    </section>
  );
}
