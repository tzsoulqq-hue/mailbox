import { Inbox } from 'lucide-react';
import { DashboardNavSection, type DashboardModuleRegistration } from '@/dashboard/module-kit';
import { MailboxPage } from './mailbox-page';

const registration: DashboardModuleRegistration = {
  manifest: {
    id: 'mailbox',
    nav: [
      {
        key: 'mailboxes',
        label: '邮箱管理',
        icon: 'mailbox',
        section: DashboardNavSection.DASHBOARD_NAV_SECTION_MAIN,
        required_services: ['mailbox'],
        order: 20
      }
    ]
  },
  icons: {
    mailbox: <Inbox size={17} />
  },
  views: {
    mailboxes: () => <MailboxPage />
  }
};

export default registration;
