import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const policy = `default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'`
const frontend = fileURLToPath(new URL('..', import.meta.url))

test('the page carries the content security policy and no inline code', () => {
  const html = readFileSync(join(frontend, 'index.html'), 'utf8')
  assert.ok(html.includes(`<meta http-equiv="Content-Security-Policy" content="${policy}" />`))
  assert.ok(!/<script(?![^>]*\bsrc=)|<style|\sstyle=|\son\w+=/.test(html), 'inline code')
})
