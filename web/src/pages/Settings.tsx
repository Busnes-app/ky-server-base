import React from 'react';

interface SettingsProps {
  settings: any;
}

export const Settings: React.FC<SettingsProps> = ({ settings }) => {
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
    </div>
  );
};
