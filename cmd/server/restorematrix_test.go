package main

import (
	"io"
	"testing"
)

func TestRestoreMatrixFlags(t *testing.T) {
	r, err := parseRestoreMatrix(nil, io.Discard)
	if err != nil || r.MatrixDir != "/matrix" || r.MediaDir != "/media" || r.BackupDir != "/app/backups" || r.DataDir != "/app/data" || r.DBHost != "postgres" || r.SkipMedia {
		t.Fatalf("defaults %+v %v", r, err)
	}
	for _, args := range [][]string{{"extra"}, {"-bogus"}} {
		if _, err := parseRestoreMatrix(args, io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
