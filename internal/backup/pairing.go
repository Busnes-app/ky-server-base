package backup

import (
	"context"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// pairingChanges lets the library seal and validate pairing state before the adapter
// commits both existing rows together. Neither a failed refresh nor an unpair can
// publish a URL with another pairing's credential.
type pairingChanges struct {
	recoveryclient.Settings
	values  map[string]string
	deleted []string
}

func (p *pairingChanges) Set(key, value string) error {
	p.values[key] = value
	return nil
}

func (p *pairingChanges) Delete(key string) error {
	p.deleted = append(p.deleted, key)
	return nil
}

// StorePairing retains the library's sealed-token format and atomically replaces the
// URL and credential. The recovery key must already be pinned.
func StorePairing(ctx context.Context, s store.SettingsStore, sealer recoveryclient.Sealer, url, token string) error {
	p := &pairingChanges{Settings: Settings(ctx, s), values: make(map[string]string)}
	if err := recoveryclient.StorePairing(p, sealer, url, token); err != nil {
		return err
	}
	return s.UpdateSettings(ctx, p.values)
}

// ClearPairing removes both rows together, retaining the key pin and receipts.
func ClearPairing(ctx context.Context, s store.SettingsStore) error {
	p := &pairingChanges{Settings: Settings(ctx, s)}
	if err := recoveryclient.ClearPairing(p); err != nil {
		return err
	}
	return s.UpdateSettings(ctx, nil, p.deleted...)
}
