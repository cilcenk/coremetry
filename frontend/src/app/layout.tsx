import type { Metadata } from 'next';
import { AuthProvider } from '@/components/AuthProvider';
import { AppShell } from '@/components/AppShell';
import '@/styles/globals.css';

export const metadata: Metadata = {
  title: 'Qmetry',
  description: 'Enterprise OpenTelemetry APM',
};

// Inline boot script applies the saved theme before paint, preventing the
// brief flash of the wrong palette (FOUC) on every navigation.
const themeBoot = `
  (function() {
    try {
      var t = localStorage.getItem('qmetry-theme');
      if (t !== 'light' && t !== 'dark') t = 'dark';
      document.documentElement.setAttribute('data-theme', t);
    } catch (e) {
      document.documentElement.setAttribute('data-theme', 'dark');
    }
  })();
`;

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" data-theme="dark">
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeBoot }} />
      </head>
      <body>
        <AuthProvider>
          <AppShell>{children}</AppShell>
        </AuthProvider>
      </body>
    </html>
  );
}
