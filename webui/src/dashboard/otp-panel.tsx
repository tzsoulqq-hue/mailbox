import { Copy } from 'lucide-react';
import { Button, buttonHint, formatUnix, mask, maskPreview } from '@/dashboard/module-kit';
import type { LatestOtp } from './types';

export function MailboxOtpPanel({ latestOtp, showSecrets, loading, onCopy }: {
  latestOtp: LatestOtp | null;
  showSecrets: boolean;
  loading: boolean;
  onCopy: (label: string, value: string) => void;
}) {
  const hasOtp = !!latestOtp?.otp;
  const subject = latestOtp?.subject || 'Latest OTP';
  const displaySubject = showSecrets ? subject : maskPreview(subject);
  const code = latestOtp?.otp || '';
  return (
    <div className={`grid min-h-[68px] grid-cols-[minmax(0,1fr)_34px] items-center gap-2 rounded-lg border p-2 ${hasOtp ? 'border-emerald-200 bg-emerald-50' : 'bg-muted/30'}`} role="status" aria-live="polite">
      <div className="grid min-w-0 gap-1">
        <span className="text-xs font-semibold text-muted-foreground">{loading ? '正在拉取 OTP' : '最近 OTP'}</span>
        <strong className={`truncate text-xl leading-tight ${hasOtp ? 'font-mono text-emerald-700' : ''}`}>
          {hasOtp ? (showSecrets ? code : mask(code)) : '暂无 OTP'}
        </strong>
        <small className="truncate text-xs text-muted-foreground" title={displaySubject}>
          {hasOtp ? `${formatUnix(latestOtp?.received_at_unix || 0)} · ${displaySubject}` : '点击拉取 OTP 后在这里显示最新验证码'}
        </small>
      </div>
      <Button className="copyButton" {...buttonHint('复制 OTP')} disabled={!hasOtp} onClick={() => onCopy('OTP', code)}>
        <Copy size={14} />
      </Button>
    </div>
  );
}
