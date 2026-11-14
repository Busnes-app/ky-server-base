import React, { useEffect, useState } from 'react';
import { secureFetch } from '../api';

interface SettingsProps {
  settings: any;
  isAdmin?: boolean;
}

interface ExemptStatus {
  networks: string;
  environment: string;
  exempted: number;
}

const ROUTE = '/api/settings/rate-limit-networks';

// Office networks whose sign-ins skip the per-address rate limit. One network per line.
const ExemptNetworks: React.FC = () => {
  const [status, setStatus] = useState<ExemptStatus | null>(null);
  const [text, setText] = useState('');
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const show = (s: ExemptStatus) => {
    setStatus(s);
    setText(s.networks.split(',').filter(Boolean).join('\n'));
  };

  useEffect(() => {
    fetch(ROUTE)
      .then(async (resp) => (resp.ok ? show(await resp.json()) : setError(`Could not load the networks (HTTP ${resp.status}).`)))
      .catch(() => setError('Could not load the networks.'));
  }, []);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setMessage('');
    setError('');
    try {
      const resp = await secureFetch(ROUTE, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ networks: text.split(/[\s,]+/).filter(Boolean).join(',') }),
      });
      const body = await resp.json();
      if (!resp.ok) {
        setError(body.error || `Could not save the networks (HTTP ${resp.status}).`);
        return;
      }
      show(body);
      setMessage(body.networks ? 'Saved. These networks now skip the per-address limit.' : 'Saved. No network skips the limit.');
    } catch {
      setError('Could not save the networks.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="panel settings-card" aria-labelledby="exempt-heading" style={{ marginTop: '24px' }}>
      <h2 id="exempt-heading">Office networks</h2>
      <p className="settings-card-desc">
        Sign-in is limited per network address. Staff in one office usually share one address and can trip that limit together. Networks
        listed here skip it; per-account limits and the security check still apply.
      </p>
      <form onSubmit={save}>
        <label className="dr-field">
          <span>Networks that skip the per-address limit, one per line</span>
          <textarea
            className="dr-mono"
            rows={4}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="203.0.113.0/24"
            aria-describedby="exempt-hint"
            disabled={!status}
          />
        </label>
        <p id="exempt-hint" className="settings-card-desc">
          List only networks you control: everyone behind a listed address loses the limit, so never add guest Wi-Fi, a VPN provider or
          your reverse proxy. The widest range accepted is /16 (IPv4) or /48 (IPv6).
        </p>
        <button type="submit" disabled={saving || !status}>Save networks</button>
      </form>
      {message && <p role="status" style={{ marginTop: '12px', color: 'var(--success)' }}>{message}</p>}
      {error && <p role="alert" style={{ marginTop: '12px', color: 'var(--danger)' }}>{error}</p>}
      {status && (
        <dl className="settings-facts" style={{ marginTop: '16px' }}>
          {status.environment && (
            <>
              <dt>Also set by the server environment</dt>
              <dd className="font-mono">{status.environment.split(',').join(', ')}</dd>
            </>
          )}
          <dt>Requests that skipped the limit</dt>
          <dd>{status.exempted} since the last change or restart</dd>
        </dl>
      )}
    </section>
  );
};

export const Settings: React.FC<SettingsProps> = ({ settings, isAdmin }) => {
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>System Settings & Architecture</h1></div>
      <p className="page-intro">Pluggable database drivers and instance configuration. Themes are under Appearance.</p>

      <section className="panel settings-card" aria-labelledby="database-heading">
        <h2 id="database-heading">Pluggable Database Layer</h2>
        <p className="settings-card-desc">Supports zero-CGO SQLite (default) and PostgreSQL clustering on demand.</p>
        <dl className="settings-facts">
          <dt>Active Storage Driver</dt><dd className="font-mono">{settings?.db_driver || 'sqlite'}</dd>
          <dt>Swapping Capability</dt><dd>Supported (PostgreSQL / SQLite)</dd>
        </dl>
      </section>

      {isAdmin && <ExemptNetworks />}
    </div>
  );
};
