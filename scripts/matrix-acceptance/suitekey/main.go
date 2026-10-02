// Command suitekey makes the acceptance harness's throwaway suite recovery key. It prints the
// public key in base64 (what the ceremony page shows) and writes two of three custodian shares,
// one per line, 0600, to the file it is given. Harness only; never part of a deployment.
package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/Busnes-app/ky-primitives/recoverykey"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: suitekey <shares-file>")
		os.Exit(2)
	}
	priv, err := recoverykey.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shares, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Non-consecutive shares, as the restore tests use.
	if err := os.WriteFile(os.Args[1], []byte(shares[0].String()+"\n"+shares[2].String()+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().Bytes()))
}
