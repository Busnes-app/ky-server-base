import React, { useEffect, useState } from 'react';
import { Copy, Check } from 'lucide-react';

interface DirectoryUser {
  id: string;
  username: string;
  display_name: string;
  email: string;
  role: string;
  status: string;
  sso_provider: string;
  scim_managed: boolean;
}

const PAGE = 50;

const source = (u: DirectoryUser) =>
  u.scim_managed ? 'SCIM' : u.sso_provider === 'kysignon' ? 'KyIdentity' : u.sso_provider === 'local' ? 'Local' : u.sso_provider;

export const SCIMAdmin: React.FC = () => {
  const [users, setUsers] = useState<DirectoryUser[]>([]);
  const [total, setTotal] = useState<number>(0);
  const [offset, setOffset] = useState<number>(0);
  const [loading, setLoading] = useState<boolean>(true);
  const [scimToken, setScimToken] = useState<string>('');
  const [copied, setCopied] = useState<boolean>(false);
  const [loadError, setLoadError] = useState<string>('');

  useEffect(() => {
    fetch('/api/settings')
      .then((resp) => resp.json())
      .then((json) => setScimToken(json.extra_settings?.scim_token || ''))
      .catch(() => setScimToken(''));
  }, []);

  useEffect(() => {
    const fetchUsers = async () => {
      setLoading(true);
      try {
        const resp = await fetch(`/api/admin/users?offset=${offset}&limit=${PAGE}`);
        if (!resp.ok) {
          setLoadError(`Could not load the account list (HTTP ${resp.status}).`);
          return;
        }
        const data = await resp.json();
        setUsers(data.users || []);
        setTotal(data.total || 0);
        setLoadError('');
      } catch (err) {
        console.error('Failed to load the account list', err);
        setLoadError('Could not load the account list.');
      } finally {
        setLoading(false);
      }
    };

    fetchUsers();
  }, [offset]);

  const copyToken = () => {
    navigator.clipboard.writeText(scimToken);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="dr-page">
      <div className="dr-header"><h1>SCIM 2.0 Directory & Inbound Provisioning</h1></div>
      <p className="page-intro">RFC 7643 and RFC 7644 inbound automated provisioning for Okta, Microsoft Entra ID (Azure AD), and KyIdentity.</p>

      <div className="panel" style={{ marginBottom: '24px' }}>
        <h3 style={{ fontSize: '16px', marginBottom: '8px' }}>SCIM 2.0 Connection Details</h3>
        <p style={{ color: 'var(--ink)', fontSize: '13px', marginBottom: '16px' }}>
          Configure these credentials in your Enterprise Identity Provider SCIM application.
        </p>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '16px' }}>
          <div>
            <label style={{ display: 'block', fontSize: '12px', color: 'var(--ink)', marginBottom: '4px' }}>
              SCIM Base URL
            </label>
            <input
              type="text"
              readOnly
              value={`${window.location.origin}/scim/v2`}
              style={{ fontFamily: 'var(--font-mono)', fontSize: '13px' }}
            />
          </div>
          <div>
            <label style={{ display: 'block', fontSize: '12px', color: 'var(--ink)', marginBottom: '4px' }}>
              SCIM Bearer Token
            </label>
            {scimToken ? (
              <div style={{ display: 'flex', gap: '8px' }}>
                <input
                  type="password"
                  readOnly
                  value={scimToken}
                  style={{ fontFamily: 'var(--font-mono)', fontSize: '13px' }}
                />
                <button type="button" className="btn-secondary" onClick={copyToken}>
                  {copied ? <Check size={16} /> : <Copy size={16} />}
                </button>
              </div>
            ) : (
              <p style={{ color: 'var(--ink)', fontSize: '13px' }}>
                Set <code>KY_SCIM_TOKEN</code> in the server environment and give the same value to your identity provider.
                Without it the server makes a new token at every start.
              </p>
            )}
          </div>
        </div>
      </div>

      <div className="panel">
        <h3 style={{ fontSize: '16px', marginBottom: '16px' }}>Accounts ({total})</h3>

        {loading ? (
          <p style={{ color: 'var(--ink)', fontSize: '13px' }}>Loading directory...</p>
        ) : loadError ? (
          <p role="alert" style={{ color: 'var(--danger)', fontSize: '13px' }}>{loadError}</p>
        ) : users.length === 0 ? (
          <p style={{ color: 'var(--ink)', fontSize: '13px' }}>No accounts yet.</p>
        ) : (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '13px' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid var(--line)', textAlign: 'left', color: 'var(--ink)' }}>
                  <th style={{ padding: '8px 12px' }}>Username</th>
                  <th style={{ padding: '8px 12px' }}>Display Name</th>
                  <th style={{ padding: '8px 12px' }}>Email</th>
                  <th style={{ padding: '8px 12px' }}>Source</th>
                  <th style={{ padding: '8px 12px' }}>Role</th>
                  <th style={{ padding: '8px 12px' }}>Status</th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id} style={{ borderBottom: '1px solid var(--line)' }}>
                    <td style={{ padding: '10px 12px', fontFamily: 'var(--font-mono)', fontWeight: 600 }}>{u.username}</td>
                    <td style={{ padding: '10px 12px' }}>{u.display_name || '—'}</td>
                    <td style={{ padding: '10px 12px', color: 'var(--ink)' }}>{u.email || '—'}</td>
                    <td style={{ padding: '10px 12px' }}>{source(u)}</td>
                    <td style={{ padding: '10px 12px' }}>{u.role}</td>
                    <td style={{ padding: '10px 12px' }}>
                      <span className={`badge badge-${u.status === 'active' ? 'success' : 'danger'}`}>
                        {u.status === 'active' ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {total > PAGE && (
              <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginTop: '12px', fontSize: '13px' }}>
                <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                  Previous
                </button>
                <span>
                  {offset + 1}–{Math.min(offset + PAGE, total)} of {total}
                </span>
                <button type="button" className="btn-secondary" disabled={offset + PAGE >= total} onClick={() => setOffset(offset + PAGE)}>
                  Next
                </button>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
};
