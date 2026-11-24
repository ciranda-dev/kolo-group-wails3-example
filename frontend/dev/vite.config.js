// The window with dev/mock.js in place of the Go service, for design work and the window test:
// `npx vite --config dev/vite.config.js`, then open http://127.0.0.1:9246/.
// Only this config points at dev/mock.js, so the app build (../vite.config.js) never includes it.
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

// The dev server injects inline scripts that the content security policy of the page blocks.
const devWithoutPolicy = {
  name: 'dev-without-policy',
  apply: 'serve',
  transformIndexHtml: {
    order: 'pre',
    handler: (html) => html.replace(/\s*<meta http-equiv="Content-Security-Policy"[^>]*>/, ''),
  },
}

const mock = fileURLToPath(new URL('./mock.js', import.meta.url))

const useMock = {
  name: 'kolo-group-mock',
  enforce: 'pre',
  resolveId(source) {
    if (source === '@wailsio/runtime' || /\/bindings\/github\.com\/ciranda-dev\/kolo-group-wails3-example\/\w+\.js$/.test(source)) {
      return mock
    }
    return null
  },
}

export default defineConfig({
  root: fileURLToPath(new URL('..', import.meta.url)),
  server: {
    host: '127.0.0.1',
    port: Number(process.env.KOLO_GROUP_MOCK_PORT) || 9246,
    strictPort: true,
  },
  plugins: [useMock, react(), devWithoutPolicy],
})
