import React from 'react';
import { Database } from 'lucide-react';

interface SettingsProps {
  settings: any;
}

export const Settings: React.FC<SettingsProps> = ({ settings }) => {
  return (
    <div className="dr-page">
      <div className="dr-header"><h1>System Settings & Architecture</h1></div>
      <p className="page-intro">Pluggable database drivers and instance configuration. Themes are under Appearance.</p>

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 480px), 1fr))', gap: '20px' }}>
        {/* Pluggable Database Card */}
        <div className="panel">
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '12px' }}>
            <Database size={20} style={{ color: 'var(--accent)' }} />
            <h3 style={{ fontSize: '16px' }}>Pluggable Database Layer</h3>
          </div>
          <p style={{ color: 'var(--ink)', fontSize: '13px', marginBottom: '16px' }}>
            Supports zero-CGO SQLite (default) and PostgreSQL clustering on demand.
          </p>

          <div style={{ background: 'var(--bg)', border: '1px solid var(--line)', borderRadius: '6px', padding: '16px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px', fontSize: '13px' }}>
              <span style={{ color: 'var(--ink)' }}>Active Storage Driver:</span>
              <span className="badge badge-accent font-mono">{settings?.db_driver || 'sqlite'}</span>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '13px' }}>
              <span style={{ color: 'var(--ink)' }}>Swapping Capability:</span>
              <span style={{ color: 'var(--success)' }}>Supported (PostgreSQL / SQLite)</span>
            </div>
          </div>
        </div>

      </div>
    </div>
  );
};
