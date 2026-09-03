import type { Metadata } from 'next';
import { Geist, Geist_Mono } from 'next/font/google';
import './globals.css';

const geistSans = Geist({ variable: '--font-geist-sans', subsets: ['latin'] });
const geistMono = Geist_Mono({ variable: '--font-geist-mono', subsets: ['latin'] });

export const metadata: Metadata = {
  title: 'CloudNVR — Tableau de bord',
  description: 'Administration de votre NVR cloud multi-sites.',
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL || 'http://localhost:8880'),
  manifest: '/manifest.webmanifest',
  applicationName: 'CloudNVR',
  appleWebApp: { capable: true, title: 'CloudNVR', statusBarStyle: 'black-translucent' },
  icons: {
    icon: [{ url: '/favicon.svg', type: 'image/svg+xml' }, { url: '/icon-192.png', sizes: '192x192', type: 'image/png' }],
    apple: [{ url: '/apple-touch-icon.png', sizes: '180x180', type: 'image/png' }],
  },
  openGraph: {
    title: 'CloudNVR',
    description: 'Votre NVR cloud, simple et sécurisé',
    images: ['/og.png'],
  },
  twitter: {
    card: 'summary_large_image',
    title: 'CloudNVR',
    description: 'Votre NVR cloud, simple et sécurisé',
    images: ['/og.png'],
  },
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="fr"><body className={`${geistSans.variable} ${geistMono.variable}`}>{children}</body></html>;
}
