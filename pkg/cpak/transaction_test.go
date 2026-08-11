package cpak

import (
	"os"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestRecoverUpdateTransactionsRestoresPreparedRecord(t *testing.T) {
	c := newTestCpak(t)
	previous := types.Application{CpakId: "previous", Name: "previous", Origin: testOrigin, ParsedBinaries: []string{"/usr/bin/previous"}}
	updated := types.Application{CpakId: "updated", Name: "updated", Origin: testOrigin, ParsedBinaries: []string{"/usr/bin/updated"}}
	seedApplication(t, c, updated)
	transaction, err := c.beginUpdateTransaction(previous, updated)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RecoverUpdateTransactions(); err != nil {
		t.Fatal(err)
	}
	apps := storedApplications(t, c)
	if len(apps) != 1 || apps[0].CpakId != previous.CpakId {
		t.Fatalf("expected previous record after recovery, got %+v", apps)
	}
	if _, err := os.Stat(c.transactionPath(transaction.Previous)); !os.IsNotExist(err) {
		t.Fatalf("expected completed transaction to be removed: %v", err)
	}
}
