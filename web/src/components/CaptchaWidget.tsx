import React, { useEffect, useState } from 'react';
import { ShieldCheck, Loader2, AlertCircle } from 'lucide-react';
import { sha256Hex } from './sha256';

interface CaptchaProps {
  onToken: (token: string) => void;
}

interface Challenge {
  algorithm: string;
  salt: string;
  challenge: string;
  maxnumber: number;
  expires_at: number;
  signature: string;
}

export const CaptchaWidget: React.FC<CaptchaProps> = ({ onToken }) => {
  const [status, setStatus] = useState<'working' | 'done' | 'failed'>('working');
  const [progress, setProgress] = useState<number>(0);
  const [errorMsg, setErrorMsg] = useState<string>('');
  const [round, setRound] = useState<number>(0);

  // onToken must be stable (a state setter): a new function per render restarts the solve.
  useEffect(() => {
    let cancelled = false;
    let refresh: ReturnType<typeof setTimeout> | undefined;

    const runPoW = async () => {
      try {
        const resp = await fetch('/api/auth/pow-challenge');
        if (!resp.ok) throw new Error('Failed to fetch security challenge');
        const chal: Challenge = await resp.json();

        const max = chal.maxnumber;
        let solved = false;
        let lastYield = performance.now();

        for (let i = 1; i <= max; i++) {
          // Not crypto.subtle: it is missing outside secure contexts (plain HTTP on a LAN address).
          if (sha256Hex(`${chal.salt}${i}`) === chal.challenge) {
            const sol = {
              algorithm: chal.algorithm,
              salt: chal.salt,
              challenge: chal.challenge,
              number: i,
              maxnumber: chal.maxnumber,
              expires_at: chal.expires_at,
              signature: chal.signature,
            };
            const token = btoa(JSON.stringify(sol));
            setStatus('done');
            onToken(token);
            // Challenges expire after five minutes (internal/auth/pow.go); fetch the next one before then.
            refresh = setTimeout(() => setRound((r) => r + 1), 4 * 60_000);
            solved = true;
            break;
          }

          // Yield to the page about every 50 ms; a timer per small batch costs more than the hashing.
          if (i % 1000 === 0 && performance.now() - lastYield > 50) {
            setProgress(Math.round((i / max) * 100));
            await new Promise((r) => setTimeout(r, 0));
            if (cancelled) return;
            lastYield = performance.now();
          }
        }

        if (!solved && !cancelled) {
          throw new Error('Could not solve challenge');
        }
      } catch (err) {
        if (!cancelled) {
          setStatus('failed');
          setErrorMsg(err instanceof Error ? err.message : 'Security check failed');
        }
      }
    };

    runPoW();
    return () => {
      cancelled = true;
      clearTimeout(refresh);
    };
  }, [onToken, round]);

  return (
    <div
      style={{
        background: 'var(--panel)',
        border: '1px solid var(--line)',
        borderRadius: '6px',
        padding: '12px',
        margin: '12px 0',
      }}
    >
      {status === 'working' && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: 'var(--ink)' }}>
          <Loader2 size={16} className="animate-spin" style={{ color: 'var(--accent)' }} />
          <span>Verifying security check ({progress}%)...</span>
        </div>
      )}
      {status === 'done' && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: 'var(--success)' }}>
          <ShieldCheck size={16} />
          <span>Security check verified.</span>
        </div>
      )}
      {status === 'failed' && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: 'var(--danger)' }}>
          <AlertCircle size={16} />
          <span>{errorMsg}</span>
          <button
            type="button"
            className="btn-secondary"
            style={{ marginLeft: 'auto' }}
            onClick={() => {
              setStatus('working');
              setProgress(0);
              setRound((r) => r + 1);
            }}
          >
            Try again
          </button>
        </div>
      )}
    </div>
  );
};
