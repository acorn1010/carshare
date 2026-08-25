import react from '@astrojs/react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'astro/config';

export default defineConfig({
  output: 'static',
  // Cold navigations prefetch on hover, the same setting foony.io runs. Warm
  // navigation never reloads at all, the shell swaps pages client-side.
  prefetch: {
    prefetchAll: true,
    defaultStrategy: 'hover',
  },
  integrations: [react()],
  vite: {
    plugins: [tailwindcss()],
    server: {
      // Local dev talks straight to the deployed API, same paths the Worker
      // proxies in production.
      proxy: {
        '/v1': { target: 'https://cars-api.foony.com', changeOrigin: true, cookieDomainRewrite: 'localhost' },
      },
    },
  },
});
