import { useState } from 'react';
import type { ComponentType } from 'react';
import {
  Input,
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
  const [registerCount, setRegisterCount] = useState(1);
  const panelProps = providerPanelProps(props);
  const providerViews = providerDefinitions.map((definition) => ({
    ...definition,
    capability: capabilityForProvider(props.providerCapabilities, definition.value),
    mailboxes: props.mailboxes.filter((mailbox) => mailboxProviderValue(mailbox.provider) === definition.value),
  }));
  const activeView = providerViews.find((view) => view.value === activeProvider) || providerViews[0];
  const toolbarActions = providerToolbarActions(activeView, props, setImportProvider, registerCount);

  return (
    <Tabs value={activeProvider} onValueChange={(value) => setActiveProvider(value as MailboxProviderTab)} className="min-h-0 flex-1 overflow-hidden">
      <WorkspaceToolbar
        tabs={<ProviderTabs views={providerViews} />}
        meta={activeView.value === 'outlook' ? <RegisterCountInput value={registerCount} onChange={setRegisterCount} /> : undefined}
        actions={<ToolbarActionButtons actions={toolbarActions} />}
      />
      {providerViews.map(({ value, capability, mailboxes, Component }) => (
        <TabsContent key={value} value={value} className="mt-0 min-h-0 overflow-auto">
          <Component {...panelProps} mailboxes={mailboxes} capability={capability} />
        </TabsContent>
      ))}
      <MailboxImportSheet open={!!importProvider} provider={importProvider || activeProvider} busy={props.busy} onOpenChange={(open) => !open && setImportProvider(undefined)} onDone={props.onDone} onError={props.onError} />
    </Tabs>
  );
}

function RegisterCountInput({ value, onChange }: { value: number; onChange: (value: number) => void }) {
  return (
    <Input
      aria-label="注册数量"
      title="注册数量"
      type="number"
      min={1}
      max={5}
      step={1}
      value={value}
      onChange={(event) => onChange(Math.max(1, Math.min(5, Math.floor(Number(event.target.value) || 1))))}
      className="h-8 w-20"
    />
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
  registering: boolean;
  oauthing: string;
  manualRecoverying: string;
  inboxLoading: boolean;
  domainSyncing: boolean;
  runningWorkflowByEmail: Map<string, Job>;
  onSelect: (mailbox: Mailbox) => void;
  onOpenWorkflow: (job: Job) => void;
  onRegisterMailbox: (maxCount?: number) => Promise<void>;
  onOAuth: (emailAddress?: string) => Promise<void>;
  onManualRecovery: (emailAddress: string) => Promise<void>;
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
    registering: props.registering,
    oauthing: props.oauthing,
    manualRecoverying: props.manualRecoverying,
    inboxLoading: props.inboxLoading,
    domainSyncing: props.domainSyncing,
    runningWorkflowByEmail: props.runningWorkflowByEmail,
    onSelect: props.onSelect,
    onOpenWorkflow: props.onOpenWorkflow,
    onRegisterMailbox: props.onRegisterMailbox,
    onOAuth: props.onOAuth,
    onManualRecovery: props.onManualRecovery,
    onFetchInbox: props.onFetchInbox,
    onSyncDomains: props.onSyncDomains,
    onToggleSecrets: props.onToggleSecrets,
    onDelete: props.onDelete,
    onDone: props.onDone,
    onError: props.onError
  };
}
