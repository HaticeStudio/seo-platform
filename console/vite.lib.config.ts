import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Library build for embedding in a host admin shell.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist/lib',
    lib: {
      entry: 'src/index.ts',
      name: 'SeoConsole',
      fileName: 'seo-console',
      formats: ['es'],
    },
    rollupOptions: {
      external: ['react', 'react-dom', 'react/jsx-runtime'],
    },
  },
})
