'use client';
import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { Spinner } from '@/components/Spinner';

// /errors got renamed to /anomalies — exceptions are one of
// several anomaly signals now (log-pattern spikes, new errors,
// metric deviations). Keep the old route as a silent redirect so
// shared links and bookmarks don't 404.
export default function ErrorsRedirectPage() {
  const router = useRouter();
  useEffect(() => { router.replace('/anomalies'); }, [router]);
  return <Spinner />;
}
