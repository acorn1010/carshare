import { useEffect, useState } from 'react';
import { api, signIn, type Me } from './api';
import { Button } from './ui';
import { SearchPage } from './SearchPage';
import { TripsPage } from './TripsPage';
import { HostPage } from './HostPage';

export type Page = 'search' | 'trips' | 'host';

/** Shell for every page: header with auth state, then the screen. Session is
 * fetched once here and handed down. */
export function App({ page }: { readonly page: Page }) {
  const [me, setMe] = useState<Me | null | 'loading'>('loading');

  useEffect(() => {
    api.me().then(setMe, () => setMe(null));
  }, []);

  return (
    <div className="mx-auto flex min-h-screen max-w-6xl flex-col px-4 sm:px-6">
      <Header page={page} me={me} />
      <main className="flex-1 pb-16">
        {page === 'search' && <SearchPage me={me} />}
        {page === 'trips' && <TripsPage me={me} />}
        {page === 'host' && <HostPage me={me} />}
      </main>
      <footer className="border-t border-paper-300 py-6 text-xs text-paper-600">
        A demo of an open-source reservation engine. Double-booking is impossible by construction, try it: book the
        same car in two tabs.
      </footer>
    </div>
  );
}

function Header({ page, me }: { readonly page: Page; readonly me: Me | null | 'loading' }) {
  const tabs: readonly { readonly key: Page; readonly href: string; readonly label: string }[] = [
    { key: 'search', href: '/', label: 'Find a car' },
    { key: 'trips', href: '/trips', label: 'My trips' },
    { key: 'host', href: '/host', label: 'Host' },
  ];
  return (
    <header className="flex flex-wrap items-center gap-x-6 gap-y-3 py-5">
      <a href="/" className="flex items-center gap-2 text-xl font-extrabold tracking-tight text-paper-900">
        <span className="plate bg-pine-600 text-paper-50 text-base">CAR·SHARE</span>
      </a>
      <nav className="flex gap-1">
        {tabs.map((tab) => (
          <a
            key={tab.key}
            href={tab.href}
            className={`rounded-lg px-3 py-1.5 text-sm font-semibold transition-colors duration-75 ${
              page === tab.key ? 'bg-paper-900 text-paper-50' : 'text-paper-700 hover:bg-paper-200'
            }`}
          >
            {tab.label}
          </a>
        ))}
      </nav>
      <div className="ml-auto">
        {me === 'loading' ? null : me ? (
          <div className="flex items-center gap-3">
            <span className="hidden text-sm font-medium text-paper-700 sm:block">{me.display_name}</span>
            {me.avatar_url ? (
              <img src={me.avatar_url} alt="" referrerPolicy="no-referrer" className="size-8 rounded-full border border-paper-300" />
            ) : null}
            <Button tone="ghost" onClick={() => api.logout().then(() => window.location.reload())}>
              Sign out
            </Button>
          </div>
        ) : (
          <Button onClick={signIn}>Sign in with Google</Button>
        )}
      </div>
    </header>
  );
}
