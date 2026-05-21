import { useEffect, useMemo, useState } from 'react';
import { api, short, useQuery, useQueryClient, useToastMessage } from '@/dashboard/module-kit';
import { maskEmail, normalizeUiEmail } from './email-utils';
import type { MailboxData } from './mailbox-data';
import type { InboxResponse, InboxResult, Mailbox } from './types';

export const mailboxInboxQueryKey = (email: string) => ['mailbox', 'inbox', normalizeUiEmail(email)] as const;

export function useMailboxActions(data: MailboxData, showSecrets: boolean, setSelectedEmail: (value: string | ((prev: string) => string)) => void) {
  const toast = useToastMessage();
  const queryClient = useQueryClient();
  const selectedEmail = normalizeUiEmail(data.selected?.email_address || '');
  const selectedInboxKey = useMemo(() => mailboxInboxQueryKey(selectedEmail), [selectedEmail]);
  const inboxQuery = useQuery<InboxResult | null>({
    queryKey: selectedInboxKey,
    queryFn: () => api<InboxResult>(`/api/mailboxes/${encodeURIComponent(selectedEmail)}/inbox?limit=20`),
    enabled: !!selectedEmail,
    initialData: null
  });
  const [oauthing, setOAuthing] = useState('');
  const [inboxLoading, setInboxLoading] = useState(false);
  const [domainSyncing, setDomainSyncing] = useState(false);

  useEffect(() => { if (data.loadError) toast.showError(data.loadError); }, [data.loadError, toast.showError]);

  async function runOAuth(emailAddress = '') {
    setOAuthing(emailAddress || '*');
    try {
      const resp = await api<{ started: boolean; job_id: string; error_message: string }>('/api/mailboxes/oauth', { method: 'POST', body: JSON.stringify({ email_address: emailAddress, only_missing: !emailAddress, limit: 100 }) });
      toast.showToast(!resp.started || resp.error_message ? 'error' : 'ok', resp.error_message || (!resp.started ? 'OAuth 流程启动失败' : `OAuth 流程已提交: ${short(resp.job_id)}`));
      await data.invalidate();
    } catch (err) {
      toast.showError(err);
    } finally {
      setOAuthing('');
    }
  }

  async function fetchInbox(emailAddress = '') {
    setInboxLoading(true);
    try {
      const target = emailAddress.trim();
      const resp = await api<InboxResponse>('/api/mailboxes/inbox', { method: 'POST', body: JSON.stringify({ limit_per_mailbox: 10, max_mailboxes: target ? 1 : 200, email_address: target }) });
      for (const result of resp.results || []) {
        const email = result.mailbox?.email_address || result.messages?.[0]?.mailbox_email || target;
        if (email) queryClient.setQueryData(mailboxInboxQueryKey(email), result);
      }
      toast.showToast(resp.failed_count > 0 ? 'error' : 'ok', `${target ? `${showSecrets ? target : maskEmail(target)} ` : ''}收信完成：${resp.message_count} 封邮件`);
      await data.invalidate();
    } catch (err) {
      toast.showError(err);
    } finally {
      setInboxLoading(false);
    }
  }

  async function syncCloudflareDomains() {
    setDomainSyncing(true);
    try {
      const resp = await api<{ synced_count?: number; error_message?: string }>('/api/mailbox-domains', {
        method: 'POST',
        body: JSON.stringify({ provider: 'MAILBOX_PROVIDER_CLOUDFLARE' })
      });
      toast.showToast(resp.error_message ? 'error' : 'ok', resp.error_message || `Cloudflare 域名已同步: ${resp.synced_count || 0}`);
      await data.invalidate();
    } catch (err) {
      toast.showError(err);
    } finally {
      setDomainSyncing(false);
    }
  }

  async function deleteMailbox(mailbox: Mailbox) {
    if (!window.confirm(`删除邮箱 ${showSecrets ? mailbox.email_address : maskEmail(mailbox.email_address)}？`)) return;
    await api(`/api/mailboxes/${encodeURIComponent(mailbox.email_address)}`, { method: 'DELETE' });
    setSelectedEmail((prev) => prev === mailbox.email_address ? '' : prev);
    toast.showOK('邮箱已删除');
    await data.invalidate();
  }

  async function done(message: string) {
    toast.showOK(message);
    await data.invalidate();
  }

  return { toast, inboxResult: inboxQuery.data ?? null, inboxQueryKey: selectedInboxKey, oauthing, inboxLoading: inboxQuery.isFetching || inboxLoading, domainSyncing, runOAuth, fetchInbox, syncCloudflareDomains, deleteMailbox, done };
}
