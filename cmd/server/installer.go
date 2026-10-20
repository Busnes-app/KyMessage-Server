package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		return explainRefusal(err, b)
	}
	if r.Service != b.Service || r.SetupID != b.SetupID {
		return errors.New("product setup report identity mismatch")
	}
	return r.Write(os.Stdout)
}

// explainRefusal adds the reason the socket's generic 409 hides: it reruns the server's
// configuration check here, in the container environment the server shares.
func explainRefusal(err error, b applysetup.Common) error {
	if cfg, cerr := config.LoadFromEnv(); cerr == nil {
		if cerr = config.ApplyInstallerConfiguration(cfg, b); cerr != nil {
			return fmt.Errorf("%w: %v", err, cerr)
		}
	}
	return fmt.Errorf("%w (a refusal's reason is in the server log: [INSTALLER] setup refused)", err)
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
