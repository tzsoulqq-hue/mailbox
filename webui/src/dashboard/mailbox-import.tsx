import { useState, type FormEventHandler } from 'react';
import { Plus } from 'lucide-react';
import {
  ActionButtonGroup,
  api,
  ControlledInputFieldList,
  ControlledTextareaField,
  errorText,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
  SegmentedControl,
  useForm
} from '@/dashboard/module-kit';
import type { ActionButtonDescriptor, Control, ControlledInputFieldDescriptor } from '@/dashboard/module-kit';
import { mailboxProviderConfig, parseMailboxBatch, type MailboxProviderTab } from './mailbox-utils';
import type { Mailbox } from './types';

type FormState = {
  email: string;
  password: string;
  refresh_token: string;
  access_token: string;
  home_country: string;
  home_ip: string;
  proxy_profile: string;
};
type ImportMode = 'single' | 'batch';
const importModeOptions = [
  { value: 'single', label: '单个' },
  { value: 'batch', label: '批量' },
] satisfies { value: ImportMode; label: string }[];

export function MailboxImportSheet({ open, provider, busy, onOpenChange, onDone, onError }: {
  open: boolean;
  provider: MailboxProviderTab;
  busy: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: (message: string) => void;
  onError: (message: string) => void;
}) {
  const [mode, setMode] = useState<ImportMode>('single');
  const [working, setWorking] = useState(false);
  const singleForm = useForm<FormState>({ defaultValues: { email: '', password: '', refresh_token: '', access_token: '', home_country: '', home_ip: '', proxy_profile: '' } });
  const batchForm = useForm<{ batchText: string }>({ defaultValues: { batchText: '' } });
  const importConfig = mailboxProviderConfig(provider).import;
  const singleEmail = singleForm.watch('email');
  const batchText = batchForm.watch('batchText');
  const activeFormId = mode === 'single' ? 'mailbox-import-single' : 'mailbox-import-batch';
  if (!importConfig) return null;
  const credentialFields = importConfig.credentialFields;
  const footerActions: ActionButtonDescriptor[] = [{
    id: 'close',
    label: '关闭',
    variant: 'outline',
    onClick: () => onOpenChange(false),
  }, {
    id: 'submit',
    label: '入池',
    icon: <Plus />,
    type: 'submit',
    form: activeFormId,
    disabled: busy || working || (mode === 'single' ? !singleEmail.trim() : !batchText.trim()),
  }];

  function payload(email: string, password: string, values: Partial<FormState> = singleForm.getValues()) {
    return {
      email,
      provider,
      password: credentialFields.includes('password') ? password : '',
      refresh_token: credentialFields.includes('refresh_token') ? values.refresh_token || '' : '',
      access_token: credentialFields.includes('access_token') ? values.access_token || '' : '',
      home_country: values.home_country || '',
      home_ip: values.home_ip || '',
      proxy_profile: values.proxy_profile || '',
      auth_status: ''
    };
  }

  async function saveSingle(values: FormState) {
    setWorking(true);
    try {
      const resp = await api<Mailbox>('/api/mailboxes', { method: 'POST', body: JSON.stringify(payload(values.email, values.password, values)) });
      singleForm.reset({ email: '', password: '', refresh_token: '', access_token: '', home_country: '', home_ip: '', proxy_profile: '' });
      onDone(`邮箱已入池: ${resp.email_address}`);
    } catch (err) {
      onError(errorText(err));
    } finally {
      setWorking(false);
    }
  }

  async function saveBatch(values: { batchText: string }) {
    const batch = parseMailboxBatch(values.batchText, provider);
    if (batch.items.length === 0) {
      onError(batch.errors.length ? `批量入池失败：${batch.errors[0]}` : '没有可入池邮箱');
      return;
    }
    setWorking(true);
    let success = 0;
    const failures = [...batch.errors];
    try {
      for (const item of batch.items) {
        try {
          await api<Mailbox>('/api/mailboxes', { method: 'POST', body: JSON.stringify(payload(item.email, item.password, item)) });
          success += 1;
        } catch (err) {
          failures.push(`${item.email}: ${errorText(err)}`);
        }
      }
      if (success > 0) {
        batchForm.reset({ batchText: '' });
        onDone(`批量入池成功 ${success}${failures.length ? `，失败 ${failures.length}` : ''}`);
      } else {
        onError(`批量入池失败：${failures.slice(0, 3).join('；')}`);
      }
    } finally {
      setWorking(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-[min(460px,100vw)] p-0 sm:max-w-none">
        <SheetHeader className="border-b">
          <SheetTitle>导入邮箱</SheetTitle>
          <SheetDescription>{importConfig.description}</SheetDescription>
        </SheetHeader>
        <div className="grid gap-3 p-4">
          <SegmentedControl value={mode} options={importModeOptions} onChange={setMode} />
          {mode === 'single' ? (
            <SingleMailboxForm formId="mailbox-import-single" control={singleForm.control} fields={credentialFields} onSubmit={singleForm.handleSubmit(saveSingle)} />
          ) : (
            <form id="mailbox-import-batch" onSubmit={batchForm.handleSubmit(saveBatch)}>
              <ControlledTextareaField
                control={batchForm.control}
                name="batchText"
                className="min-h-32 resize-y"
                placeholder={importConfig.batchPlaceholder}
              />
            </form>
          )}
        </div>
        <SheetFooter className="border-t">
          <ActionButtonGroup className="grid gap-2" actions={footerActions} />
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

function SingleMailboxForm({ formId, control, fields: credentialFields, onSubmit }: {
  formId: string;
  control: Control<FormState>;
  fields: string[];
  onSubmit: FormEventHandler<HTMLFormElement>;
}) {
  const fields: ControlledInputFieldDescriptor<FormState>[] = [{
    id: 'email',
    name: 'email',
    label: '邮箱',
    placeholder: '邮箱',
    inputId: 'mailbox-import-email',
  }, {
    id: 'password',
    name: 'password',
    label: '密码',
    placeholder: '邮箱密码，可空',
    type: 'password',
    inputId: 'mailbox-import-password',
    visible: credentialFields.includes('password'),
  }, {
    id: 'refresh-token',
    name: 'refresh_token',
    label: 'Refresh token',
    placeholder: 'Refresh token，可空',
    type: 'password',
    inputId: 'mailbox-import-refresh-token',
    visible: credentialFields.includes('refresh_token'),
  }, {
    id: 'access-token',
    name: 'access_token',
    label: 'Access token',
    placeholder: 'Access token，可空',
    type: 'password',
    inputId: 'mailbox-import-access-token',
    visible: credentialFields.includes('access_token'),
  }, {
    id: 'home-country',
    name: 'home_country',
    label: 'Home country',
    placeholder: 'HK / JP / DE',
    inputId: 'mailbox-import-home-country',
  }, {
    id: 'home-ip',
    name: 'home_ip',
    label: 'Home IP',
    placeholder: 'Original registration IP',
    inputId: 'mailbox-import-home-ip',
  }, {
    id: 'proxy-profile',
    name: 'proxy_profile',
    label: 'Proxy profile',
    placeholder: 'outlook',
    inputId: 'mailbox-import-proxy-profile',
  }];

  return (
    <form id={formId} className="grid gap-2" onSubmit={onSubmit}>
      <ControlledInputFieldList control={control} fields={fields} />
    </form>
  );
}
