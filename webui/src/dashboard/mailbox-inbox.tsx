import { Inbox } from 'lucide-react';
import { Button, compactToast } from '@/dashboard/module-kit';
import {
  formatUnix,
  mask,
  maskPreview
} from '@/dashboard/module-kit';
import { formatEmailList, maskEmail } from './email-utils';
import { messageSignals, signalKindName, signalLabel, verificationCodeForMessage } from './mailbox-signal-utils';
import type { InboxMessage, InboxResult, Mailbox } from './types';

export function MailboxInboxSection({ mailbox, result, showSecrets, loading, canFetch, onFetch }: {
  mailbox: Mailbox;
  result?: InboxResult | null;
  showSecrets: boolean;
  loading: boolean;
  canFetch: boolean;
  onFetch: (emailAddress?: string) => Promise<void>;
}) {
  const messages = result?.messages || [];
  return (
    <section className="grid gap-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">收件箱</h3>
        {canFetch && (
          <Button variant="outline" size="sm" disabled={loading} onClick={() => onFetch(mailbox.email_address)}>
            <Inbox />{loading ? '刷新中' : '刷新'}
          </Button>
        )}
      </div>
      {result?.error_message && <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-2 text-sm text-destructive">{compactToast(result.error_message)}</div>}
      <div className="grid gap-2">
        {messages.map((message, index) => (
          <InboxMessageRow message={message} showSecrets={showSecrets} key={`${message.mailbox_email}-${message.id || index}`} />
        ))}
        {!result && <InboxEmpty text={loading ? '正在读取收件箱。' : '暂无邮件。'} />}
        {result && !result.error_message && messages.length === 0 && <InboxEmpty text="当前邮箱没有新邮件。" />}
      </div>
    </section>
  );
}

function InboxMessageRow({ message, showSecrets }: {
  message: InboxMessage;
  showSecrets: boolean;
}) {
  return (
    <article className="grid gap-1 rounded-lg border p-2">
      <div className="flex items-baseline justify-between gap-2">
        <strong className="truncate text-sm" title={message.subject}>{message.subject || '-'}</strong>
        <span className="shrink-0 text-xs text-muted-foreground">{formatUnix(message.received_at_unix)}</span>
      </div>
      <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="truncate">发件人 {showSecrets ? (message.from_address || '-') : maskEmail(message.from_address)}</span>
        <MessageSignalStrip message={message} showSecrets={showSecrets} />
      </div>
      <div className="truncate text-xs text-muted-foreground" title={formatEmailList(message.recipients, true)}>
        收件人 {formatEmailList(message.recipients, showSecrets)}
      </div>
      <p className="line-clamp-3 text-xs text-muted-foreground">{showSecrets ? (message.body_preview || '-') : maskPreview(message.body_preview || '-')}</p>
    </article>
  );
}

function MessageSignalStrip({ message, showSecrets }: {
  message: InboxMessage;
  showSecrets: boolean;
}) {
  const signals = messageSignals(message);
  const fallbackCode = verificationCodeForMessage(message);
  if (signals.length === 0 && fallbackCode) {
    return <em className="rounded-full bg-emerald-50 px-2 py-0.5 font-mono not-italic text-emerald-700">验证码 {showSecrets ? fallbackCode : mask(fallbackCode)}</em>;
  }
  if (signals.length === 0) return null;
  return (
    <span className="flex shrink-0 items-center gap-1">
      {signals.map((signal, index) => {
        const kind = signalKindName(signal.kind);
        const code = kind === 'otp' && signal.code ? ` ${showSecrets ? signal.code : mask(signal.code)}` : '';
        return (
          <em className="rounded-full bg-muted px-2 py-0.5 not-italic" key={`${kind}-${signal.code || signal.label || index}`}>
            {signalLabel(signal)}{code}
          </em>
        );
      })}
    </span>
  );
}

function InboxEmpty({ text }: { text: string }) {
  return <div className="rounded-lg border bg-muted/30 p-4 text-center text-sm text-muted-foreground">{text}</div>;
}
