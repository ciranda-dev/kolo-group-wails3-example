// Checks the sources without any dependency: no em dash anywhere; every copy key that the code asks for exists; and the
// window keeps its privacy rules: no HTML from strings, no code from strings, no address outside the app, no camera or
// microphone of the browser, and the page carries the content security policy and nothing that policy would block.
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { strings } from '../src/i18n.js'

const root = join(fileURLToPath(import.meta.url), '..', '..')
const files = []
const walk = (dir) => {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) {
      walk(path)
    } else if (/\.(jsx?|mjs|css|html|json)$/.test(name)) {
      files.push(path)
    }
  }
}
for (const dir of ['src', 'dev', 'scripts']) {
  walk(join(root, dir))
}
files.push(join(root, 'index.html'))

// Words that must not be in the window or its mock, built from pieces so this file passes itself.
const banned = [
  ['HTML from a string', new RegExp(['inner' + 'HTML', 'outer' + 'HTML', 'dangerously' + 'Set', 'insertAdjacent' + 'HTML', 'document\\.' + 'write'].join('|'))],
  ['code from a string', new RegExp(['\\bev' + 'al\\(', 'new ' + 'Function\\('].join('|'))],
  ['the camera or microphone of the browser', new RegExp(['getUser' + 'Media', 'media' + 'Devices', 'getDisplay' + 'Media'].join('|'))],
  ['an address outside the app', /https?:\/\/(?!127\.0\.0\.1[:/]|www\.w3\.org\/)/],
]

const policy = /<meta http-equiv="Content-Security-Policy" content="default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'" \/>/

const problems = []
const used = new Set()
for (const file of files) {
  const text = readFileSync(file, 'utf8')
  const name = relative(root, file)
  const shipped = (name.startsWith('src/') && !name.endsWith('.test.js')) || name.startsWith('dev/mock') || name.endsWith('.html')
  text.split('\n').forEach((line, i) => {
    if (line.includes(String.fromCharCode(0x2014))) {
      problems.push(`${name}:${i + 1}: em dash`)
    }
    if (shipped) {
      for (const [what, pattern] of banned) {
        if (pattern.test(line)) {
          problems.push(`${name}:${i + 1}: ${what}`)
        }
      }
    }
  })
  if (name.endsWith('.html')) {
    if (!policy.test(text)) {
      problems.push(`${name}: no content security policy, or not the one of the other Kolo apps`)
    }
    if (/<script(?![^>]*\bsrc=)|<style|\sstyle=|\son\w+=/.test(text)) {
      problems.push(`${name}: inline script, style or handler (the content security policy would block it)`)
    }
  }
  if (!shipped || !name.startsWith('src/')) {
    continue
  }
  for (const m of text.matchAll(/'([a-z][\w]*(?:\.[\w-]+)+)'/g)) {
    used.add(m[1])
  }
}

const keys = Object.keys(strings.en)
const has = (key) => keys.some((k) => k === key || k.startsWith(`${key}.`))
for (const key of used) {
  if (/^(chat|conv|settings|start|relay|crash|invite|members|member|room|status|create|policy|join|history|posters|sidebar|net)\./.test(key) && !has(key) && !/\.(js|jsx|css)$/.test(key)) {
    problems.push(`copy key '${key}' is used but not in src/i18n.js`)
  }
}

if (problems.length > 0) {
  console.error(problems.join('\n'))
  process.exit(1)
}
console.log(`lint: ${files.length} files, ${keys.length} copy keys, no problems`)
