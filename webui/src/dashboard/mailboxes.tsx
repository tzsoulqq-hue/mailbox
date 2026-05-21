import { useState } from 'react';
import type { ComponentType } from 'react';
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  ToolbarActionButtons,
  WorkspaceToolbar
} from '@/dashboard/module-kit';
import { CloudflareMailboxProviderPanel } from './mailbox-provider-cloudflare';
import { MailboxImportSheet } from './mailbox-import';
import { OutlookMailboxProviderPanel } from './mailbox-provider-outlook';
import type { MailboxProviderPanelProps } from './mailbox-provider-types';
import { providerToolbarActions } from './mailbox-toolbar-actions';
import { capabilityForProvider, mailboxProviderConfigs, mailboxProviderValue, type MailboxProviderTab } from './mailbox-utils';
import type { Job, Mailbox, MailboxDomain, MailboxProviderCapability } from './types';
export { MailboxDetails } from './mailbox-details';

const providerComponents: Record<MailboxProviderTab, ComponentType<MailboxProviderPanelProps>> = {
  outlook: OutlookMailboxProviderPanel,
  cloudflare: CloudflareMailboxProviderPanel,
};

const providerDefinitions = mailboxProviderConfigs.map((config) => ({
  value: config.value,
  label: config.label,
  Component: providerComponents[config.value],
})) satisfies ProviderDefinition[];

export function MailboxPanel(props: MailboxPanelProps) {
  const [activeProvider, setActiveProvider] = useState<MailboxProviderTab>('outlook');
  const [importProvider, setImportProvider] = useState<MailboxProviderTab>();
  const panelProps = providerPanelProps(props);
  const providerViews = providerDefinitions.map((definition) => ({
    ...definition,
    capability: capabilityForProvider(props.providerCapabilities, definition.value),
    mailboxes: props.mailboxes.filter((mailbox) => mailboxProviderValue(mailbox.provider) === definition.value),
  }));
  const activeView = providerViews.find((view) => view.value === activeProvider) || providerViews[0];
  const toolbarActions = providerToolbarActions(activeView, props, setImportProvider);

  return (
    <Tabs value={activeProvider} onValueChange={(value) => setActiveProvider(value as MailboxProviderTab)} className="min-h-0 flex-1">
      <WorkspaceToolbar
        tabs={<ProviderTabs views={providerViews} />}
        actions={<ToolbarActionButtons actions={toolbarActions} />}
      />
      {providerViews.map(({ value, capability, mailboxes, Component }) => (
        <TabsContent key={value} value={value} className="min-h-0">
          <Component {...panelProps} mailboxes={mailboxes} capability={capability} />
        </TabsContent>
      ))}
      <MailboxImportSheet open={!!importProvider} provider={importProvider || activeProvider} busy={props.busy} onOpenChange={(open) => !open && setImportProvider(undefined)} onDone={props.onDone} onError={props.onError} />
    </Tabs>
  );
}

function ProviderTabs({ views }: { views: ProviderView[] }) {
  return (
    <TabsList variant="line" className="h-8">
      {views.map((view) => (
        <TabsTrigger key={view.value} value={view.value} className="gap-1.5 px-2">
          {view.capability?.display_name || view.label}
        </TabsTrigger>
      ))}
    </TabsList>
  );
}

type ProviderDefinition = {
  value: MailboxProviderTab;
  label: string;
  Component: ComponentType<MailboxProviderPanelProps>;
};

type ProviderView = {
  value: MailboxProviderTab;
  label: string;
  capability?: MailboxProviderCapability;
  mailboxes: Mailbox[];
  Component: ComponentType<MailboxProviderPanelProps>;
};

type MailboxPanelProps = {
  mailboxes: Mailbox[];
  domains: MailboxDomain[];
  providerCapabilities: MailboxProviderCapability[];
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

function providerPanelProps(props: MailboxPanelProps): Omit<MailboxProviderPanelProps, 'mailboxes' | 'capability'> {
  return {
    domains: props.domains,
    selected: props.selected,
    busy: props.busy,
    showSecrets: props.showSecrets,
    oauthing: props.oauthing,
    inboxLoading: props.inboxLoading,
    domainSyncing: props.domainSyncing,
    runningWorkflowByEmail: props.runningWorkflowByEmail,
    onSelect: props.onSelect,
    onOpenWorkflow: props.onOpenWorkflow,
    onOAuth: props.onOAuth,
    onFetchInbox: props.onFetchInbox,
    onSyncDomains: props.onSyncDomains,
    onToggleSecrets: props.onToggleSecrets,
    onDelete: props.onDelete,
    onDone: props.onDone,
    onError: props.onError
  };
}
