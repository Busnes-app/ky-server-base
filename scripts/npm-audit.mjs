import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const advisory = 'https://github.com/advisories/GHSA-fc5q-39xc-vjpf';
const expires = Date.parse('2026-12-01T00:00:00Z');

// Temporary build-tool exception, not a dependency fix. See docs/NPM-AUDIT.md.
// Production dependencies, other advisories/versions, invalid reports and expiry fail closed.
export function assessAudit(report, lock, now = Date.now()) {
  if (report?.auditReportVersion !== 2 || !report.vulnerabilities ||
      typeof report.vulnerabilities !== 'object' || Array.isArray(report.vulnerabilities) || Object.hasOwn(report, 'error') ||
      !lock?.packages || typeof lock.packages !== 'object' || Array.isArray(lock.packages) || !Number.isFinite(now)) {
    throw new Error('Invalid npm audit report, lockfile or clock');
  }
  const findings = report.vulnerabilities;
  function excepted(name, ancestors = new Set()) {
    if (ancestors.has(name) || now >= expires) return false;
    const item = findings[name];
    if (!item || item.severity !== 'high' || !Array.isArray(item.nodes) || !item.nodes.length ||
        !item.nodes.every(path => lock.packages[path]?.dev === true) ||
        !Array.isArray(item.via) || !item.via.length) return false;
    const seen = new Set([...ancestors, name]);
    return item.via.every(cause => {
      if (typeof cause === 'string') return excepted(cause, seen);
      return name === 'picomatch' && cause?.name === 'picomatch' &&
        cause.url === advisory && cause.severity === 'high' && cause.range === '<=4.0.7' &&
        item.nodes.every(path => lock.packages[path].version === '4.0.7');
    });
  }
  const blocked = [], accepted = [];
  for (const [name, item] of Object.entries(findings)) {
    if (!item || !['info', 'low', 'moderate', 'high', 'critical'].includes(item.severity)) {
      throw new Error('Invalid npm vulnerability entry');
    }
    if (item.severity !== 'high' && item.severity !== 'critical') continue;
    (excepted(name) ? accepted : blocked).push(name);
  }
  return { blocked, accepted };
}

function main() {
  const cwd = fileURLToPath(new URL('../web/', import.meta.url));
  const lock = JSON.parse(readFileSync(new URL('../web/package-lock.json', import.meta.url), 'utf8'));
  // Runtime findings never use an exception, including this advisory.
  const runtime = spawnSync('npm', ['audit', '--omit=dev', '--audit-level=high'], { cwd, stdio: 'inherit' });
  if (runtime.error || runtime.status !== 0) throw new Error('Production npm audit failed');
  const audit = spawnSync('npm', ['audit', '--include=dev', '--json'], { cwd, encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 });
  if (audit.error || ![0, 1].includes(audit.status)) throw new Error('Full npm audit could not complete');
  const result = assessAudit(JSON.parse(audit.stdout), lock);
  if (result.accepted.length) {
    console.warn('TEMPORARY: accepting build-only picomatch 4.0.7, GHSA-fc5q-39xc-vjpf, until 2026-12-01 UTC. The package remains vulnerable; see docs/NPM-AUDIT.md.');
  }
  if (result.blocked.length) throw new Error(`Unaccepted high/critical npm findings: ${JSON.stringify(result.blocked)}`);
  console.log('npm audit gate passed: production clean; no unaccepted high/critical findings.');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { main(); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
