package store

import (
	"database/sql"
	"fmt"
)

// GetProviderKeyCiphertext returns the encrypted upstream key material for a
// provider. It is used by the service/proxy layers to decrypt the upstream key.
func (s *Store) GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error) {
	row := s.db.QueryRow(`
		SELECT key_ciphertext, key_nonce
		FROM providers WHERE id = ?`, providerID)
	if err := row.Scan(&ciphertext, &nonce); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, fmt.Errorf("store: get provider key %q: %w", providerID, ErrNotFound)
		}
		return nil, nil, fmt.Errorf("store: get provider key %q: %w", providerID, err)
	}
	return
}

// UpdateProviderKeyCiphertext overwrites the encrypted upstream key for a
// provider and updates the display mask. Called by the App layer after
// encrypting the user's upstream key.
func (s *Store) UpdateProviderKeyCiphertext(providerID string, ciphertext, nonce []byte, masked string) error {
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE providers SET key_ciphertext=?, key_nonce=?, key_masked=?, updated_at=?
			WHERE id=?`,
			ciphertext, nonce, masked, now, providerID)
		if err != nil {
			return fmt.Errorf("store: update provider key %q: %w", providerID, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update provider key %q: %w", providerID, ErrNotFound)
		}
		return nil
	})
}
