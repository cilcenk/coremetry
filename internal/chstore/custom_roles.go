package chstore

import "context"

// Custom roles live under the "custom_roles" key in system_settings as
// a JSON blob — array of {name, pages}. The auth.Service owns marshal/
// unmarshal; chstore only stores the bytes so the column shape stays
// stable regardless of how the role struct evolves.
const customRolesKey = "custom_roles"

// GetCustomRolesRaw returns the saved JSON blob for operator-defined
// custom roles, or nil if none have been persisted yet.
func (s *Store) GetCustomRolesRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, customRolesKey)
}

// PutCustomRolesRaw overwrites the saved JSON blob. Caller marshals
// the typed slice so chstore stays untyped.
func (s *Store) PutCustomRolesRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, customRolesKey, raw)
}
