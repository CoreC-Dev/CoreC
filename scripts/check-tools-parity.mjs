#!/usr/bin/env node
// check-tools-parity.mjs — the one place that compares the two tool surfaces.
//
// corec-mcp (Go) and the dashboard's AI assistant (TypeScript) are two clients of
// the same REST control plane. Each can only see its own repository, so neither
// can notice the other renaming a verb: cmd/corec-mcp/tools_manifest.json is
// generated from Go, and the dashboard's tools.test.ts asserts its own list.
// This script reads both and fails when they disagree.
//
// Usage:
//   node scripts/check-tools-parity.mjs                       # sibling checkout
//   COREC_DASHBOARD_DIR=/path/to/CoreC-Dashboard node scripts/check-tools-parity.mjs
//   node scripts/check-tools-parity.mjs --skip-if-missing      # for CI without the sibling
//
// Exit: 0 in step, 1 drifted, 2 could not evaluate (missing input and --skip-if-missing
// not given). 2 is distinct from 1 on purpose: "I could not look" must never be
// reported as "it is fine", and it must not be reported as a drift either.

import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const corecRoot = resolve(here, '..')
const skipIfMissing = process.argv.includes('--skip-if-missing')

const manifestPath = join(corecRoot, 'cmd/corec-mcp/tools_manifest.json')
const dashboardRoot = process.env.COREC_DASHBOARD_DIR
  ? resolve(process.env.COREC_DASHBOARD_DIR)
  : resolve(corecRoot, '../CoreC-Dashboard')
const dashboardTools = join(dashboardRoot, 'src/features/assistant/tools.ts')

function unavailable(why) {
  if (skipIfMissing) {
    // Said out loud: a skipped check that prints nothing is indistinguishable
    // from a passing one in a CI log.
    console.log(`  ⏭️  tools parity skipped: ${why}`)
    process.exit(0)
  }
  console.error(`  ⚠️  tools parity could not run: ${why}`)
  console.error('     (pass --skip-if-missing to treat this as a skip instead of a failure)')
  process.exit(2)
}

if (!existsSync(manifestPath)) {
  unavailable(`${manifestPath} missing — generate it with: go test ./cmd/corec-mcp -run TestToolsManifest -update`)
}
if (!existsSync(dashboardTools)) {
  unavailable(`${dashboardTools} missing — set COREC_DASHBOARD_DIR to the CoreC-Dashboard checkout`)
}

const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
const mcp = new Set(manifest.tools.map((t) => t.name))
const mcpWrites = new Set(manifest.tools.filter((t) => t.write).map((t) => t.name))

// Read the dashboard's tool names and their class without importing TypeScript:
// the literals below are the shape tools.ts uses, and a parse failure is itself a
// failure (a silent empty set would make the check vacuously pass).
const ts = readFileSync(dashboardTools, 'utf8')
const nameRe = /name:\s*'([a-z_]+)'/g
const ui = new Set()
for (const m of ts.matchAll(nameRe)) ui.add(m[1])

// toolClass: 'write' appears once per write tool, after its name; slice the
// function body per tool by splitting on the name marker and take the first class.
const uiWrites = new Set()
const blocks = ts.split(/(?=name:\s*'[a-z_]+')/)
for (const b of blocks) {
  const nm = b.match(/name:\s*'([a-z_]+)'/)
  if (!nm) continue
  if (/toolClass:\s*'write'/.test(b)) uiWrites.add(nm[1])
}

if (ui.size === 0) {
  console.error('  ❌ tools parity: parsed zero dashboard tools — the regex no longer matches tools.ts')
  process.exit(1)
}

// Tools the dashboard adds on top of MCP: the control plane's dry-run config
// validator and config reader. They are not control verbs, so corec-mcp does
// not carry them. Kept as an explicit allowlist so adding another is a decision.
const uiOnly = new Set(['get_configs', 'validate_configs'])

// MCP verbs the dashboard deliberately folds into an existing one.
// read_tag(driver, tag) is GET /tags?driver=&tag=, which list_tags already takes
// (it exposes the same `tag` argument) -- so the chat box is not missing the
// capability, only the second name for it. Listed rather than silently filtered:
// if it ever stops being covered, this comment is wrong and so is the check, and
// both are easier to notice here than in a catch-all further down.
const coveredBy = { read_tag: 'list_tags' }

const problems = []

for (const n of mcp) {
  if (ui.has(n)) continue
  const via = coveredBy[n]
  if (via && ui.has(via)) continue
  problems.push(
    via
      ? `${n}: covered only if '${via}' exposes the same arguments — check its schema`
      : `${n}: in corec-mcp but the dashboard does not offer it`,
  )
}
for (const n of ui) {
  if (!mcp.has(n) && !uiOnly.has(n)) {
    problems.push(`${n}: offered by the dashboard but not in corec-mcp (and not on the uiOnly allowlist)`)
  }
}
for (const n of uiWrites) {
  if (!mcpWrites.has(n)) problems.push(`${n}: a write verb in the dashboard but read-only/absent in corec-mcp`)
}
for (const n of mcpWrites) {
  if (ui.has(n) && !uiWrites.has(n)) problems.push(`${n}: a write verb in corec-mcp but read-class in the dashboard`)
}

if (problems.length > 0) {
  console.error(`  ❌ tools parity: ${problems.length} difference(s)`)
  for (const p of problems) console.error(`     - ${p}`)
  console.error('     Fix both sides (and tools_manifest.json) in the same change.')
  process.exit(1)
}

const exact = [...ui].filter((n) => mcp.has(n)).sort()
const extra = [...ui].filter((n) => !mcp.has(n)).sort()
const folded = [...mcp].filter((n) => !ui.has(n) && coveredBy[n]).sort()
console.log(
  `  ✅ tools parity: ${mcp.size} MCP tools, ${ui.size} dashboard tools, ` +
    `${exact.length} shared` +
    (folded.length ? `, ${folded.map((n) => `${n}->${coveredBy[n]}`).join(', ')}` : '') +
    (extra.length ? `, dashboard-only: ${extra.join(', ')}` : ''),
)
// exact + folded must be all of MCP: if a name were neither shared nor folded,
// the problems list above would already have failed the run.
if (exact.length + folded.length !== mcp.size) {
  console.error('  ❌ tools parity: summary arithmetic is inconsistent')
  process.exit(1)
}
