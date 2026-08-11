package cpak

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/types"
)

type updateTransaction struct {
	Previous  types.Application `json:"previous"`
	Updated   types.Application `json:"updated"`
	Committed bool              `json:"committed"`
}

func (c *Cpak) transactionPath(app types.Application) string {
	return filepath.Join(c.Options.StorePath, "transactions", "updates", app.CpakId+".json")
}

func (c *Cpak) writeUpdateTransaction(transaction updateTransaction) error {
	path := c.transactionPath(transaction.Previous)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".update.partial-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (c *Cpak) beginUpdateTransaction(previous, updated types.Application) (updateTransaction, error) {
	transaction := updateTransaction{Previous: previous, Updated: updated}
	return transaction, c.writeUpdateTransaction(transaction)
}

func (c *Cpak) commitUpdateTransaction(transaction updateTransaction) error {
	transaction.Committed = true
	return c.writeUpdateTransaction(transaction)
}

func (c *Cpak) finishUpdateTransaction(transaction updateTransaction) error {
	if err := os.Remove(c.transactionPath(transaction.Previous)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RecoverUpdateTransactions restores incomplete updates before an application
// can observe a mixed store state.
func (c *Cpak) RecoverUpdateTransactions() error {
	directory := filepath.Join(c.Options.StorePath, "transactions", "updates")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var transaction updateTransaction
		if err := json.Unmarshal(data, &transaction); err != nil {
			return fmt.Errorf("decode update transaction %s: %w", path, err)
		}
		if transaction.Committed {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		if err := c.replaceApplication(transaction.Updated, transaction.Previous); err != nil {
			return fmt.Errorf("restore update transaction %s: %w", path, err)
		}
		if err := c.createExports(transaction.Previous); err != nil {
			return fmt.Errorf("restore exports for %s: %w", transaction.Previous.Name, err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
