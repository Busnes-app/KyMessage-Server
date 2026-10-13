package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func runInstaller(args []string) error {
	opts, err := applysetup.Parse(args)
	if err != nil {
		return err
	}
	if opts.Capabilities {
		return applysetup.WriteCapabilities(os.Stdout, "kymessage", "kyidentity-oidc", "directory-webhook", "dedicated-admin", "local-backup-config", "check", "restart")
	}
	dir := os.Getenv("KY_DATA_DIR")
	if dir == "" {
		dir = "./data"
	}
	socket, err := config.InstallerSocket(dir)
	if err != nil {
		return err
	}
	if opts.Restart {
		return applysetup.RequestRestart(context.Background(), socket)
	}
	var b applysetup.Common
	if err = applysetup.Decode(opts.File, &b); err != nil {
		return err
	}
	if err = b.Validate("kymessage", "/api/sso/kyidentity/callback"); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return errors.New("invalid setup bundle")
	}
	r, err := applysetup.CallLocal(context.Background(), socket, raw, opts.Check)
	if err != nil {
		return err
	}
	if r.Service != b.Service || r.SetupID != b.SetupID {
		return errors.New("product setup report identity mismatch")
	}
	return r.Write(os.Stdout)
}

func loadInstallerConfiguration(ctx context.Context, cfg *config.Config, st store.Store) error {
	raw, err := st.Settings().GetSetting(ctx, store.InstallerConfigKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var b applysetup.Common
	if err = applysetup.DecodeRaw([]byte(raw), &b); err != nil {
		return err
	}
	return config.ApplyInstallerConfiguration(cfg, b)
}
