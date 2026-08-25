/**
 * Serves the static site and passes API paths through to the backend, so the
 * browser sees a single origin: the session cookie stays first-party and no
 * CORS exists anywhere. OAuth redirects must reach the browser untouched,
 * which is why the subrequest uses redirect: 'manual'.
 */
const API_ORIGIN = 'https://cars-api.foony.com';

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname.startsWith('/v1/') || url.pathname === '/health') {
      const upstream = new URL(url.pathname + url.search, API_ORIGIN);
      return fetch(new Request(upstream, request), { redirect: 'manual' });
    }
    return env.ASSETS.fetch(request);
  },
};
