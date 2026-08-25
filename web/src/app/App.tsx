import { useEffect, useState, type MouseEvent } from 'react';
import { api, signIn, type Me } from './api';
import { Button } from './ui';
import { SearchPage } from './SearchPage';
import { TripsPage } from './TripsPage';
import { HostPage } from './HostPage';

export type Page = 'search' | 'trips' | 'host';

const PAGE_PATHS: Record<Page, string> = { search: '/', trips: '/trips', host: '/host' };

function pageForPath(path: string): Page {
  if (path.startsWith('/trips')) {
    return 'trips';
  }
  if (path.startsWith('/host')) {
    return 'host';
  }
  return 'search';
}

// The last confirmed session, so a returning user's name renders in the first
// paint instead of popping in after /v1/me answers. The fetch still confirms.
const ME_HINT_KEY = 'carshare_me_hint';

function hintedMe(): Me | null {
  try {
    const raw = sessionStorage.getItem(ME_HINT_KEY);
    return raw ? (JSON.parse(raw) as Me) : null;
  } catch {
    return null;
  }
}

function rememberMe(me: Me | null): void {
  try {
    if (me) {
      sessionStorage.setItem(ME_HINT_KEY, JSON.stringify(me));
    } else {
      sessionStorage.removeItem(ME_HINT_KEY);
    }
  } catch {
    // Private mode; the hint is only a nicety.
  }
}

/** Shell for every page. Navigation swaps pages client-side (pushState), so a
 * tab click never reloads the document and the session survives it. Direct
 * loads of /trips and /host still work, each static page mounts this shell
 * with its own starting page. */
export function App({ page: initialPage }: { readonly page: Page }) {
  const [page, setPage] = useState<Page>(initialPage);
  const [me, setMe] = useState<Me | null | 'loading'>(() => hintedMe() ?? 'loading');

  useEffect(() => {
    api.me().then(
      (confirmed) => {
        setMe(confirmed);
        rememberMe(confirmed);
      },
      () => {
        setMe(null);
        rememberMe(null);
      },
    );
  }, []);

  useEffect(() => {
    const onPop = () => setPage(pageForPath(window.location.pathname));
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  const navigate = (to: Page) => {
    if (to !== page) {
      window.history.pushState({}, '', PAGE_PATHS[to]);
      setPage(to);
    }
  };

  return (
    <div className="mx-auto flex min-h-screen max-w-6xl flex-col px-4 sm:px-6">
      <Header page={page} me={me} onNavigate={navigate} />
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

function Header({ page, me, onNavigate }: {
  readonly page: Page;
  readonly me: Me | null | 'loading';
  readonly onNavigate: (to: Page) => void;
}) {
  const tabs: readonly { readonly key: Page; readonly label: string }[] = [
    { key: 'search', label: 'Find a car' },
    { key: 'trips', label: 'My trips' },
    { key: 'host', label: 'Host' },
  ];
  const follow = (event: MouseEvent, to: Page) => {
    // Plain left clicks stay in the shell; modified clicks keep their
    // open-in-new-tab meaning because the href is real.
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) {
      return;
    }
    event.preventDefault();
    onNavigate(to);
  };
  return (
    <header className="flex min-h-18 flex-wrap items-center gap-x-6 gap-y-3 py-4">
      <a href="/" onClick={(event) => follow(event, 'search')} className="flex items-center gap-2 text-xl font-extrabold tracking-tight text-paper-900">
        <span className="plate bg-pine-600 text-paper-50 text-base">CAR·SHARE</span>
      </a>
      <nav className="flex gap-1">
        {tabs.map((tab) => (
          <a
            key={tab.key}
            href={PAGE_PATHS[tab.key]}
            onClick={(event) => follow(event, tab.key)}
            className={`rounded-lg px-3 py-1.5 text-sm font-semibold transition-colors duration-75 ${
              page === tab.key ? 'bg-paper-900 text-paper-50' : 'text-paper-700 hover:bg-paper-200 active:bg-paper-300'
            }`}
          >
            {tab.label}
          </a>
        ))}
      </nav>
      <div className="ml-auto flex h-10 items-center">
        {me === 'loading' ? (
          <span className="h-10 w-40 animate-pulse rounded-lg bg-paper-200 motion-reduce:animate-none" />
        ) : me ? (
          <div className="flex items-center gap-3">
            <span className="hidden text-sm font-medium text-paper-700 sm:block">{me.display_name}</span>
            {me.avatar_url ? (
              <img
                src={me.avatar_url}
                alt=""
                width="32"
                height="32"
                referrerPolicy="no-referrer"
                className="size-8 rounded-full border border-paper-300"
              />
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
