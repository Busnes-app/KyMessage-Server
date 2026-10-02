package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Busnes-app/ky_server_base/internal/backup"
)

func runRestoreMatrix(args []string) {
	r, err := parseRestoreMatrix(args, os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if err := r.Run(context.Background(), os.Stdout); err != nil {
		log.Fatalf("restore-matrix: %v", err)
	}
}

const restoreMatrixUsage = `Usage: docker compose run --rm restore-matrix [-skip-media]

Loads the restored Matrix databases and media into a fresh stack, once:
  1. As KY_MATRIX_UID (the user who ran matrix-init), run kymessages restore -to ./restored
     and move its data/ and matrix/ into the deployment directory, owned by that user.
  2. Copy the local backup directory to ./backups; ./backups/media must be readable by
     KY_MATRIX_UID (the app writes it as root, owner-only).
  3. Only if this stack is meant to be replaced: docker compose down -v, which deletes its
     Matrix database and media. Then docker compose up -d postgres.
  4. docker compose run --rm restore-matrix
  5. In the deployment directory, sudo chown -R root:root ./data ./backups: the app runs as
     root and refuses key files it does not own. Then docker compose up -d.
It refuses, changing nothing, unless both databases are empty, the media store is empty and
every dump and media file checks out.`

// parseRestoreMatrix takes the defaults the restore-matrix Compose service mounts.
func parseRestoreMatrix(args []string, out io.Writer) (backup.MatrixRestore, error) {
	var r backup.MatrixRestore
	fs := flag.NewFlagSet("restore-matrix", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&r.MatrixDir, "matrix", "/matrix", "restored matrix-init directory (holds dumps/ and secrets/)")
	fs.StringVar(&r.MediaDir, "media", "/media", "Synapse's media store; must be empty")
	fs.StringVar(&r.BackupDir, "backups", "/app/backups", "local backup directory holding media/")
	fs.StringVar(&r.DataDir, "data", "/app/data", "restored data directory holding media.key")
	fs.StringVar(&r.DBHost, "db-host", "postgres", "Postgres host or host:port")
	fs.BoolVar(&r.SkipMedia, "skip-media", false, "restore the databases only (no media backup survived)")
	fs.Usage = func() {
		fmt.Fprintln(out, restoreMatrixUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return r, err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return r, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return r, nil
}
