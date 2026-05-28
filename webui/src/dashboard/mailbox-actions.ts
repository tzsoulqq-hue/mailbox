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
    enabled: false,
    initialData: null
  });
  const [oauthing, setOAuthing] = useState('');
  const [registering, setRegistering] = useState(false);
  const [manualRecoverying, setManualRecoverying] = useState('');
  const [inboxLoading, setInboxLoading] = useState(false);
  const [storedInboxLoading, setStoredInboxLoading] = useState(false);
  const [domainSyncing, setDomainSyncing] = useState(false);

  useEffect(() => { if (data.loadError) toast.showError(data.loadError); }, [data.loadError, toast.showError]);

  useEffect(() => {
    if (!selectedEmail) return;
    let cancelled = false;
    setStoredInboxLoading(true);
    api<InboxResult>(`/api/mailboxes/${encodeURIComponent(selectedEmail)}/inbox?limit=20`)
      .then((result) => {
        if (!cancelled) queryClient.setQueryData(selectedInboxKey, result);
      })
      .catch((err) => {
        if (!cancelled) toast.showError(err);
      })
      .finally(() => {
        if (!cancelled) setStoredInboxLoading(false);
      });
    return () => { cancelled = true; };
  }, [selectedEmail, selectedInboxKey, queryClient, toast.showError]);

  async function runOAuth(emailAddress = '') {
    setOAuthing(emailAddress || '*');
    try {
      const target = normalizeUiEmail(emailAddress);
      const mailbox = target ? data.mailboxes.find((item) => normalizeUiEmail(item.email_address) === target) : null;
      if (target && mailbox && (mailbox.manual_recovery_required || mailbox.auth_status === 'AUTH_FAILED' || mailbox.auth_status === 'NEEDS_MANUAL_VERIFICATION')) {
        const resp = await api<{ started: boolean; launch_command: string; error_message: string }>('/api/mailboxes/oauth-local', { method: 'POST', body: JSON.stringify({ email_address: target }) });
        if (resp.started && resp.launch_command && navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(resp.launch_command).catch(() => undefined);
        }
        if (resp.started && resp.launch_command) {
          window.prompt('Local Camoufox OAuth command copied. Run this in PowerShell:', resp.launch_command);
        }
        toast.showToast(!resp.started || resp.error_message ? 'error' : 'ok', resp.error_message || 'Local Camoufox OAuth command copied');
        await data.invalidate();
        return;
      }
      const resp = await api<{ started: boolean; job_id: string; error_message: string }>('/api/mailboxes/oauth', { method: 'POST', body: JSON.stringify({ email_address: emailAddress, only_missing: !emailAddress, limit: 100 }) });
      toast.showToast(!resp.started || resp.error_message ? 'error' : 'ok', resp.error_message || (!resp.started ? 'OAuth start failed' : `OAuth submitted ${short(resp.job_id)}`));
      await data.invalidate();
    } catch (err) {
      toast.showError(err);
    } finally {
      setOAuthing('');
    }
  }

  async function registerMailbox(maxCount = 1) {
    setRegistering(true);
    try {
      const resp = await api<{ started: boolean; job_id: string; operation_id?: string; error_message: string }>('/api/mailboxes/register', {
        method: 'POST',
        body: JSON.stringify({ max_count: Math.max(1, Math.min(5, Math.floor(maxCount || 1))) })
      });
      const id = resp.job_id || resp.operation_id || '';
      toast.showToast(!resp.started || resp.error_message ? 'error' : 'ok', resp.error_message || (!resp.started ? '邮箱注册启动失败' : `邮箱注册已提交 ${short(id)}`));
      await data.invalidate();
    } catch (err) {
      toast.showError(err);
    } finally {
      setRegistering(false);
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
      toast.showToast(resp.failed_count > 0 ? 'error' : 'ok', `${target ? `${showSecrets ? target : maskEmail(target)} ` : ''}收信完成，${resp.message_count} 封邮件`);
      await data.invalidate();
    } catch (err) {
      toast.showError(err);
    } finally {
      setInboxLoading(false);
    }
  }

  async function startManualRecovery(emailAddress: string) {
    const target = normalizeUiEmail(emailAddress);
    if (!target) return;
    setManualRecoverying(target);
    try {
      const resp = await api<{ started: boolean; session_id: string; proxy_country: string; proxy_session: string; local_proxy_url: string; recovery_url: string; launch_command: string; instruction: string; error_message: string }>('/api/mailboxes/recovery', {
        method: 'POST',
        body: JSON.stringify({ email_address: target })
      });
      const detail = resp.proxy_country ? ` (${resp.proxy_country}${resp.proxy_session ? ' / ' + short(resp.proxy_session) : ''})` : '';
      if (resp.started && resp.launch_command && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(resp.launch_command).catch(() => undefined);
      }
      if (resp.started && resp.launch_command) {
        window.prompt('Camoufox recovery command copied. Run this in PowerShell:', resp.launch_command);
      }
      toast.showToast(!resp.started || resp.error_message ? 'error' : 'ok', resp.error_message || `Manual recovery is ready; local launch command copied${detail}`);
      await data.invalidate();
      return;
    } catch (err) {
      toast.showError(err);
    } finally {
      setManualRecoverying('');
    }
  }

  async function syncCloudflareDomains() {
    setDomainSyncing(true);
    try {
      const resp = await api<{ synced_count?: number; error_message?: string }>('/api/mailbox-domains', {
        method: 'POST',
        body: JSON.stringify({ provider: 'MAILBOX_PROVIDER_CLOUDFLARE' })
      });
      toast.showToast(resp.error_message ? 'error' : 'ok', resp.error_message || `Cloudflare 域名已同步 ${resp.synced_count || 0}`);
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

  return { toast, inboxResult: inboxQuery.data ?? null, inboxQueryKey: selectedInboxKey, oauthing, registering, manualRecoverying, inboxLoading: storedInboxLoading || inboxQuery.isFetching || inboxLoading, domainSyncing, registerMailbox, runOAuth, startManualRecovery, fetchInbox, syncCloudflareDomains, deleteMailbox, done };
}
