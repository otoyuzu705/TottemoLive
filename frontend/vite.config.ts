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

// `wails dev` は、起動後にViteの標準出力のパイプを閉じることがある(再ビルド・バインディング再生成の最中など)。
// その後にViteがログを書くと EPIPE の未処理エラーでViteごと落ちるので、標準出力・標準エラーの書き込みエラーは無視する
for (const stream of [process.stdout, process.stderr]) {
  stream.on('error', (e: NodeJS.ErrnoException) => {
    if (e.code !== 'EPIPE') throw e
  })
}

export default defineConfig({
  plugins: [svelte(), passPreviewToGo()],
})
