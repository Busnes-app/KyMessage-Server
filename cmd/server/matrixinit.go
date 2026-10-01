package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
)

func runMatrixInitCmd(args []string) {
	if err := runMatrixInit(args, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "matrix-init: %v\n", err)
		os.Exit(1)
	}
}

// runMatrixInit writes the Matrix configs and prints what the operator must do next. It
// prints file names, never their contents.
func runMatrixInit(args []string, getenv func(string) string, w io.Writer) error {
	fs := flag.NewFlagSet("matrix-init", flag.ContinueOnError)
	dir := fs.String("dir", "./matrix", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
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
	fmt.Fprintf(w, "  redirect URI  %s\n  scopes        %s\n\n", r.RedirectURI, strings.Join(r.Scopes, " "))
	fmt.Fprintf(w, "Synapse and MAS run as the owner of %s. Set:\n", res.Dir)
	fmt.Fprintf(w, "  KY_MATRIX_UID=%d\n  KY_MATRIX_GID=%d\n", os.Getuid(), os.Getgid())
	if os.Getuid() == 0 {
		fmt.Fprintln(w, "Warning: running as root makes the Matrix containers run as root; run matrix-init as an unprivileged user.")
	}
	return nil
}
