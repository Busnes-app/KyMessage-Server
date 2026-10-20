package api

import (
	"context"
	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"log"
	"reflect"
)

// ApplyInstaller compares desired configuration with the server's immutable
// startup snapshot. A committed configuration awaiting restart stays pending.
// The setup socket answers any error with a generic 409, so the reason is logged here.
// Bundles carry file references, never secrets, so no error names a secret.
func (s *Server) ApplyInstaller(ctx context.Context, raw []byte, check bool) (applysetup.Report, error) {
	r, err := s.applyInstaller(ctx, raw, check)
	if err != nil {
		log.Printf("[INSTALLER] setup refused: %v", err)
	}
	return r, err
}

func (s *Server) applyInstaller(ctx context.Context, raw []byte, check bool) (applysetup.Report, error) {
	var b applysetup.Common
	if err := applysetup.DecodeRaw(raw, &b); err != nil {
		return applysetup.Report{}, err
	}
	want := *s.config
	if err := config.ApplyInstallerConfiguration(&want, b); err != nil {
		return applysetup.Report{}, err
	}
	r, err := s.store.ApplyInstaller(ctx, b, check)
	if err != nil {
		return r, err
	}
	if !reflect.DeepEqual(want.SSO, s.config.SSO) || want.Backup != s.config.Backup || want.SCIM != s.config.SCIM {
		r.Pending = append(r.Pending, applysetup.Object{Kind: "activation", Name: "restart"})
	}
	return r, nil
}
