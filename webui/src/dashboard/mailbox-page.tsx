import { useState } from 'react';
import { Mail } from 'lucide-react';
import { AppDrawer, ToastMessage } from '@/dashboard/module-kit';
import { useMailboxActions } from './mailbox-actions';
import { useMailboxData } from './mailbox-data';
import { useMailboxEmailEventCache } from './mailbox-events';
import { MailboxDetails, MailboxPanel } from './mailboxes';
import { canRunProviderMailboxAction, mailboxActions } from './mailbox-utils';

export function MailboxPage() {
  const [selectedEmail, setSelectedEmail] = useState('');
  const [showSecrets, setShowSecrets] = useState(true);
  const data = useMailboxData(selectedEmail);
  const actions = useMailboxActions(data, showSecrets, setSelectedEmail);
  useMailboxEmailEventCache({ email: data.selected?.email_address, inboxQueryKey: actions.inboxQueryKey, enabled: !!data.selected?.email_address });

  return (
    <>
      <ToastMessage toast={actions.toast.toast} />
      <section className="workspace singlePaneWorkspace">
        <div className="panel">
          <MailboxPanel mailboxes={data.mailboxes} domains={data.domains} providerCapabilities={data.providerCapabilities} selected={selectedEmail} busy={data.busy} showSecrets={showSecrets} oauthing={actions.oauthing} inboxLoading={actions.inboxLoading} domainSyncing={actions.domainSyncing} runningWorkflowByEmail={data.runningByEmail} onSelect={(mailbox) => setSelectedEmail(mailbox.email_address)} onOpenWorkflow={() => actions.toast.showOK('请在工作流页查看任务详情')} onOAuth={actions.runOAuth} onFetchInbox={() => actions.fetchInbox()} onSyncDomains={actions.syncCloudflareDomains} onToggleSecrets={() => setShowSecrets((value) => !value)} onDelete={actions.deleteMailbox} onDone={actions.done} onError={actions.toast.showError} />
        </div>
      </section>
      <AppDrawer open={!!data.selected} title="邮箱详情" description="邮箱配置和收件箱" icon={<Mail size={16} />} size="wide" bodyClassName="p-3" onOpenChange={(open) => { if (!open) setSelectedEmail(''); }}>
        {data.selected && <MailboxDetails mailbox={data.selected} showSecrets={showSecrets} inboxResult={actions.inboxResult} inboxLoading={actions.inboxLoading} canFetchInbox={canRunProviderMailboxAction(data.providerCapabilities, data.selected, mailboxActions.fetchInbox)} onCopy={actions.toast.copyValue} onFetchInbox={actions.fetchInbox} onDelete={actions.deleteMailbox} />}
      </AppDrawer>
    </>
  );
}
