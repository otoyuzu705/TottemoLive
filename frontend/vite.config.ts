import { defineConfig, type Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// 開発時、/preview/{id}.wav はGo側(AssetServerのHandler)が配信する。
// Viteの index.html フォールバックに取られないよう、404を返してWailsにGo側のハンドラへ回させる。
const passPreviewToGo = (): Plugin => ({
  name: 'pass-preview-to-go',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      if (req.url?.startsWith('/preview/')) {
        res.statusCode = 404
        res.end()
        return
      }
      next()
    })
  },
})

export default defineConfig({
  plugins: [svelte(), passPreviewToGo()],
})
