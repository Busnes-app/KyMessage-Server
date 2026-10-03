package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
)

func runMatrixInitCmd(args []string) {
	if err := runMatrixInit(args, os.Getenv, os.Getuid(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "matrix-init: %v\n", err)
		os.Exit(1)
	}
}

// runMatrixInit writes the Matrix configs and prints what the operator must do next. It
// prints file names, never their contents. uid is the caller's; the containers run as it.
func runMatrixInit(args []string, getenv func(string) string, uid int, w io.Writer) error {
	fs := flag.NewFlagSet("matrix-init", flag.ContinueOnError)
	dir := fs.String("dir", "./matrix", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if uid == 0 {
		return errors.New("refusing to run as root: Postgres, Synapse and MAS would run as root too; run it as the unprivileged user that will own the Matrix files")
	}
	in, err := matrixinit.InputFromEnv(getenv)
	if err != nil {
		return err
	}
	res, err := matrixinit.Run(in, *dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Matrix configuration in %s\n", res.Dir)
	for _, l := range []struct {
		label string
		paths []string
	}{{"created", res.Created}, {"kept", res.Kept}, {"rendered", res.Rendered}} {
		for _, p := range l.paths {
			fmt.Fprintf(w, "  %-9s %s\n", l.label, p)
		}
	}
	fmt.Fprintf(w, "Back up %s/secrets and synapse/signing.key; they are never regenerated.\n\n", res.Dir)
	r := res.Registration
	fmt.Fprintf(w, "Register the MAS client in KyIdentity and assign the users who may chat:\n")
	fmt.Fprintf(w, "  client type   %s\n  client ID     the KY_MATRIX_MAS_CLIENT_ID value\n", r.ClientType)
	fmt.Fprintf(w, "  redirect URI  %s\n", r.RedirectURI)
	fmt.Fprintf(w, "  back-channel logout URI  %s\n  scopes        %s\n\n", r.BackchannelLogoutURI, strings.Join(r.Scopes, " "))
	fmt.Fprintf(w, "Postgres, Synapse and MAS run as the owner of %s. Set:\n", res.Dir)
	fmt.Fprintf(w, "  KY_MATRIX_UID=%d\n  KY_MATRIX_GID=%d\n", uid, os.Getgid())
	fmt.Fprintf(w, "KyMessages' MAS admin client (offboarding and the console). Set:\n  KY_MATRIX_ADMIN_CLIENT_ID=%s\n\n", res.AdminClientID)
	if res.ClientSecretMissing {
		fmt.Fprintf(w, "Register this client in KyIdentity, save the secret it shows to %s (mode 0600), and run matrix-init again.\n",
			filepath.Join(res.Dir, matrixinit.ClientSecretFile))
		return nil
	}
	fmt.Fprintln(w, "If the stack is running, apply the new configs with: docker compose restart synapse mas element livekit")
	fmt.Fprintln(w, "Calls are enabled: set KY_MATRIX_RTC_HOST in .env, proxy that HTTPS/WebSocket host to livekit:7880, and forward TCP 7881 and UDP 7882 to KY_MATRIX_MEDIA_IP. HTTP tunnels carry signalling, not media.")
	if in.TurnHost != "" {
		fmt.Fprintln(w, "TURN/TLS: preserve the overlay chain and append docker-compose.turn.yml; forward external TCP 443 and UDP 30000-30100. After certificate renewal re-run matrix-init, then restart livekit only after success.")
	}
	fmt.Fprintln(w, "and, once per existing stack, the backup role:")
	fmt.Fprintln(w, "  docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -f /docker-entrypoint-initdb.d/kybackup-role.sql")
	return nil
}
