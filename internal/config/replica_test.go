package config

import (
	"net/netip"
	"reflect"
	"testing"
)

// notShared are the Config fields replicas may hold differently: per host, derived from a
// shared field, or deprecated and ignored. Every other field must change the fingerprint, so
// a new setting added to Config without being classified here or in sharedFields fails.
var notShared = map[string]string{
	"Server.Host":              "per host",
	"Server.Port":              "per host",
	"Server.ReadTimeout":       "constant",
	"Server.WriteTimeout":      "constant",
	"Database.Driver":          "the shared database itself",
	"Database.DSN":             "the shared database itself",
	"Database.DataDir":         "per host",
	"Database.MaxOpenConns":    "per host",
	"Database.MaxIdleConns":    "per host",
	"Database.ConnMaxLifetime": "per host",
	"Security.EncryptionKey":   "the fingerprint's own key, checked separately",
	"Security.HostCookies":     "derived from KY_APP_URL",
	"Security.SessionTTL":      "constant",
	"Security.TrustedProxies":  "per host",
	"SSO.KySignOnHMACSecret":   "deprecated, ignored",
	"Backup.Dir":               "per host",
	"Backup.Keep":              "per host",
}

func TestEveryConfigFieldIsClassified(t *testing.T) {
	base := Config{}
	base.Security.EncryptionKey = make([]byte, 32)
	base.SCIM.Enabled = true // the token counts only while SCIM is on
	want := ReplicaFingerprint(&base)
	v := reflect.ValueOf(&base).Elem()
	for i := range v.NumField() {
		section := v.Type().Field(i).Name
		for j := range v.Field(i).NumField() {
			name := section + "." + v.Field(i).Type().Field(j).Name
			cfg := base
			cfg.Security.EncryptionKey = append([]byte(nil), base.Security.EncryptionKey...)
			change(t, name, reflect.ValueOf(&cfg).Elem().Field(i).Field(j))
			shared := !reflect.DeepEqual(ReplicaFingerprint(&cfg), want)
			_, exempt := notShared[name]
			switch {
			case name == "Security.EncryptionKey":
			case shared && exempt:
				t.Errorf("%s is fingerprinted but listed as not shared", name)
			case !shared && !exempt:
				t.Errorf("%s is neither fingerprinted nor listed as per-host; classify it", name)
			}
		}
	}
}

func change(t *testing.T, name string, f reflect.Value) {
	t.Helper()
	switch f.Kind() {
	case reflect.String:
		f.SetString(f.String() + "x")
	case reflect.Bool:
		f.SetBool(!f.Bool())
	case reflect.Int, reflect.Int64:
		f.SetInt(f.Int() + 1)
	case reflect.Slice:
		switch f.Interface().(type) {
		case []byte:
			f.Index(0).SetUint(1)
		case []netip.Prefix:
			f.Set(reflect.ValueOf([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}))
		default:
			t.Fatalf("%s: no change rule for %s", name, f.Type())
		}
	default:
		t.Fatalf("%s: no change rule for %s", name, f.Type())
	}
}
