import assert from 'node:assert/strict';
import { test } from 'node:test';
import { assessAudit } from './npm-audit.mjs';

const now = Date.parse('2026-11-18T00:00:00Z');
function fixture() {
  return {
    report: { auditReportVersion: 2, vulnerabilities: {
      picomatch: { severity: 'high', nodes: ['node_modules/picomatch'], via: [{
        name: 'picomatch', severity: 'high', range: '<=4.0.7',
        url: 'https://github.com/advisories/GHSA-fc5q-39xc-vjpf',
      }] },
      vite: { severity: 'high', nodes: ['node_modules/vite'], via: ['picomatch'] },
    } },
    lock: { packages: {
      'node_modules/picomatch': { version: '4.0.7', dev: true },
      'node_modules/vite': { version: '8.3.8', dev: true },
    } },
  };
}

test('accepts only the documented development-only finding and its dependency chain', () => {
  const { report, lock } = fixture();
  assert.deepEqual(assessAudit(report, lock, now), { blocked: [], accepted: ['picomatch', 'vite'] });
});

test('blocks production exposure, different versions, advisories, severity and expiry', () => {
  const changes = [
    ({ lock }) => { lock.packages['node_modules/picomatch'].dev = false; },
    ({ lock }) => { lock.packages['node_modules/picomatch'].version = '4.0.8'; },
    ({ report }) => { report.vulnerabilities.picomatch.via[0].url += '-unknown'; },
    ({ report }) => { report.vulnerabilities.picomatch.via[0].severity = 'critical'; },
    ({ report }) => { report.vulnerabilities.picomatch.severity = 'critical'; },
    ({ report }) => { report.vulnerabilities.picomatch.via[0].range = '<=4.0.8'; },
    ({ report }) => { report.vulnerabilities.picomatch.via.push({ name: 'picomatch', url: 'unknown' }); },
  ];
  for (const change of changes) {
    const data = fixture(); change(data);
    assert.deepEqual(assessAudit(data.report, data.lock, now).blocked, ['picomatch', 'vite']);
  }
  const { report, lock } = fixture();
  lock.packages['node_modules/vite'].dev = false;
  assert.deepEqual(assessAudit(report, lock, now).blocked, ['vite']);
  assert.deepEqual(assessAudit(report, lock, Date.parse('2026-12-01T00:00:00Z')).blocked, ['picomatch', 'vite']);
});

test('blocks unrelated findings, missing records and cyclic chains', () => {
  const { report, lock } = fixture();
  report.vulnerabilities.other = { severity: 'high', nodes: ['node_modules/vite'], via: ['missing'] };
  assert.deepEqual(assessAudit(report, lock, now).blocked, ['other']);
  report.vulnerabilities.vite.via = ['vite'];
  assert.deepEqual(assessAudit(report, lock, now).blocked, ['vite', 'other']);
  delete lock.packages['node_modules/picomatch'];
  assert.deepEqual(assessAudit(report, lock, now).blocked, ['picomatch', 'vite', 'other']);
});

test('accepts a clean report and rejects malformed or failed registry responses', () => {
  const clean = { auditReportVersion: 2, vulnerabilities: {} };
  assert.deepEqual(assessAudit(clean, { packages: {} }, now), { blocked: [], accepted: [] });
  assert.throws(() => assessAudit(clean, { packages: [] }, now));
  assert.throws(() => assessAudit(clean, { packages: {} }, NaN));
  for (const report of [null, {}, { error: 'registry unavailable' }, { auditReportVersion: 2, vulnerabilities: {}, error: 'registry failed' }, { auditReportVersion: 2, vulnerabilities: [] }, { auditReportVersion: 2, vulnerabilities: { x: {} } }]) {
    assert.throws(() => assessAudit(report, { packages: {} }, now));
  }
});
