import { useMemo } from 'react';
import { api, useQuery, useQueryClient } from '@/dashboard/module-kit';
import { latestJobMap, useJobEventCache } from '@/dashboard/modules/workflow/sdk';
import { mailboxWorkflowEmail } from './mailbox-utils';
import type { Job, JobSnapshot, Mailbox, MailboxDomain, MailboxProviderCapability } from './types';

const mailboxQueryKeys = {
  mailboxes: ['mailbox', 'mailboxes'] as const,
  domains: ['mailbox', 'domains'] as const,
  providerCapabilities: ['mailbox', 'provider-capabilities'] as const,
  runningJobs: ['mailbox', 'running-jobs'] as const
};

export function useMailboxData(selectedEmail: string) {
  const queryClient = useQueryClient();
  const mailboxesQuery = useQuery({ queryKey: mailboxQueryKeys.mailboxes, queryFn: () => api<Mailbox[]>('/api/mailboxes?limit=500') });
  const domainsQuery = useQuery({ queryKey: mailboxQueryKeys.domains, queryFn: () => api<MailboxDomain[]>('/api/mailbox-domains') });
  const providerCapabilitiesQuery = useQuery({ queryKey: mailboxQueryKeys.providerCapabilities, queryFn: () => api<MailboxProviderCapability[]>('/api/mailbox-provider-capabilities') });
  const runningJobsQuery = useQuery({ queryKey: mailboxQueryKeys.runningJobs, queryFn: () => api<JobSnapshot[]>('/api/jobs?limit=200&status=RUNNING') });
  const mailboxes = Array.isArray(mailboxesQuery.data) ? mailboxesQuery.data : [];
  const runningJobs = snapshotsToJobs(runningJobsQuery.data || []);
  const selected = mailboxes.find((mailbox) => mailbox.email_address === selectedEmail) || null;
  const runningByEmail = useMemo(() => latestJobMap(runningJobs.filter(mailboxWorkflowEmail), mailboxWorkflowEmail), [runningJobs]);

  useJobEventCache({
    lists: [{ queryKey: mailboxQueryKeys.runningJobs, include: isRunningSnapshot, limit: 200 }],
    onEvent: (event) => {
      if (isMailboxSnapshot(event.snapshot) && isTerminalSnapshot(event.snapshot)) void queryClient.invalidateQueries({ queryKey: mailboxQueryKeys.mailboxes });
    }
  });

  return {
    mailboxes,
    selected,
    runningByEmail,
    domains: Array.isArray(domainsQuery.data) ? domainsQuery.data : [],
    providerCapabilities: Array.isArray(providerCapabilitiesQuery.data) ? providerCapabilitiesQuery.data : [],
    busy: mailboxesQuery.isLoading || domainsQuery.isLoading || providerCapabilitiesQuery.isLoading,
    loadError: mailboxesQuery.error || domainsQuery.error || providerCapabilitiesQuery.error || runningJobsQuery.error,
    invalidate: () => invalidateMailboxQueries(queryClient)
  };
}

export type MailboxData = ReturnType<typeof useMailboxData>;

function invalidateMailboxQueries(queryClient: ReturnType<typeof useQueryClient>) {
  return Promise.all(Object.values(mailboxQueryKeys).map((queryKey) => queryClient.invalidateQueries({ queryKey })));
}

function snapshotsToJobs(snapshots: JobSnapshot[]) {
  return (Array.isArray(snapshots) ? snapshots : []).map((snapshot) => snapshot.job).filter((job): job is Job => !!job);
}

function isRunningSnapshot(snapshot: JobSnapshot) {
  return snapshot.job?.status === 'RUNNING';
}

function isTerminalSnapshot(snapshot?: JobSnapshot) {
  return !!snapshot?.job?.status && snapshot.job.status !== 'RUNNING';
}

function isMailboxSnapshot(snapshot?: JobSnapshot) {
  return snapshot?.job?.action === 'MAILBOX_OAUTH' || snapshot?.job?.action === 'REGISTER_MAILBOX';
}
