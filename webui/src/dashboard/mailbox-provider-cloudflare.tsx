import { MailboxDomainGroups } from './mailbox-list';
import { mailboxProviderConfig } from './mailbox-utils';
import type { MailboxProviderPanelProps } from './mailbox-provider-types';

export function CloudflareMailboxProviderPanel(props: MailboxProviderPanelProps) {
  const config = mailboxProviderConfig('cloudflare');
  const configuredDomains = props.domains
    .filter((domain) => mailboxProviderConfig(domain.provider).value === config.value)
    .map((domain) => domain.domain);
  return (
    <div className="min-h-0 p-3">
      <MailboxDomainGroups
        {...props}
        providerCapability={props.capability}
        configuredDomains={configuredDomains}
        showStatus={config.showStatus}
        emptyDomainsText="域名未配置；邮件到达后会按 recipient 自动归组。"
        emptyDomainText="这个域名下暂无邮件地址，收到邮件后会自动出现。"
      />
    </div>
  );
}
