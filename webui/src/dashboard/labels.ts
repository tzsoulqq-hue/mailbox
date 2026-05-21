import type { DisplayLabelMap } from './types';

const mailboxStatusLabels: DisplayLabelMap = {
  AUTHORIZED: '已授权',
  OAUTH_PENDING: '待 OAuth',
  AUTH_FAILED: '认证失败',
  NEEDS_MANUAL_VERIFICATION: '需人工验证'
};

export function mailboxStatusText(status: string) {
  return mailboxStatusLabels[status] || status || '-';
}
