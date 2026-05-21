import { MailboxRecordList } from './mailbox-list';
import type { MailboxProviderPanelProps } from './mailbox-provider-types';
import { mailboxProviderConfig } from './mailbox-utils';

export function OutlookMailboxProviderPanel(props: MailboxProviderPanelProps) {
  const config = mailboxProviderConfig('outlook');
  return (
    <div className="min-h-0 p-3">
      <MailboxRecordList {...props} providerCapability={props.capability} showStatus={config.showStatus} emptyText={`暂无 ${config.label} 邮箱。`} />
    </div>
  );
}
