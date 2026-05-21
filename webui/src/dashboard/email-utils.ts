import { mask } from '@/dashboard/module-kit';

export function maskEmail(value: string) {
  if (!value) return '-';
  const [local, domain] = value.split('@');
  if (!local || !domain) return mask(value);
  return `${local.slice(0, 2)}***@${domain}`;
}

export function formatEmailList(values: string[] | undefined, showSecrets: boolean) {
  const list = values || [];
  if (list.length === 0) return '-';
  return list.map((value) => showSecrets ? value : maskEmail(value)).join(', ');
}

export function normalizeUiEmail(value: string) {
  return String(value || '').trim().toLowerCase();
}

export function canonicalUiEmail(value: string) {
  const normalized = normalizeUiEmail(value);
  const [local, domain] = normalized.split('@');
  if (!local || !domain) return normalized;
  return `${local.split('+')[0]}@${domain}`;
}
