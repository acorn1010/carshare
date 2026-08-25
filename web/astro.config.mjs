import react from '@astrojs/react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'astro/config';

export default defineConfig({
  output: 'static',
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
