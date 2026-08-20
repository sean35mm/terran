package terran

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var beforeEnrollmentConfigWrite func() error

func LoadEnrollment(paths Paths) (Enrollment, error) {
	var enrollment Enrollment
	if err := readTrustedStateStrict(paths.ConfigFile, "enrollment config", &enrollment, 1<<20); err != nil {
		return Enrollment{}, err
	}
	if enrollment.SchemaVersion != SchemaVersion || enrollment.RepositoryID == "" || enrollment.RepositoryPath == "" || enrollment.CommandCenterID == "" || enrollment.DisplayName == "" {
		return Enrollment{}, fmt.Errorf("invalid enrollment config")
	}
	return enrollment, nil
}

func Enroll(repo, name string, replace bool) (Enrollment, bool, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return Enrollment{}, false, err
	}
	loaded, err := LoadManifest(repo)
	if err != nil {
		return Enrollment{}, false, err
	}
	explicitName := name != ""
	if !explicitName {
		name, err = os.Hostname()
		if err != nil || validateDisplayName(name) != nil {
			name = "command-center"
		}
	}
	if err := validateDisplayName(name); err != nil {
		return Enrollment{}, false, err
	}
	var result Enrollment
	changed := false
	err = withLock(paths.Lock, func() error {
		var emptyReceipt bool
		existing, loadErr := LoadEnrollment(paths)
		if loadErr == nil {
			if existing.RepositoryPath == loaded.Repository && existing.RepositoryID == loaded.Manifest.ID {
				result = existing
				return nil
			}
			if !replace {
				return fmt.Errorf("a different repository is enrolled; use --replace")
			}
			receipt, receiptErr := LoadReceipt(paths)
			if receiptErr == nil && (len(receipt.Projections) != 0 || len(receipt.Instructions) != 0) {
				return fmt.Errorf("cannot replace enrollment while managed skills or instructions remain; decommission them or migrate ownership first")
			}
			if receiptErr != nil && !errors.Is(receiptErr, os.ErrNotExist) {
				return fmt.Errorf("read receipt before replacement: %w", receiptErr)
			}
			emptyReceipt = receiptErr == nil
		} else if !errors.Is(loadErr, os.ErrNotExist) {
			return fmt.Errorf("read enrollment: %w", loadErr)
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		if loadErr == nil {
			id = existing.CommandCenterID
		}
		result = Enrollment{SchemaVersion, loaded.Manifest.ID, loaded.Repository, id, name}
		var retiredReceipt string
		if emptyReceipt {
			retiredReceipt, err = retireEmptyReceipt(paths)
			if err != nil {
				return err
			}
		}
		if beforeEnrollmentConfigWrite != nil {
			if err := beforeEnrollmentConfigWrite(); err != nil {
				return errors.Join(err, restoreRetiredReceipt(paths, retiredReceipt))
			}
		}
		configBytes, err := marshalJSON(result)
		if err != nil {
			return errors.Join(err, restoreRetiredReceipt(paths, retiredReceipt))
		}
		writeResult, writeErr := atomicPrivateJSONBytes(paths.ConfigFile, configBytes, nil)
		if writeErr != nil {
			committed := false
			if writeResult.renamed {
				installed, _, verifyErr := readTrustedFile(paths.ConfigFile, "installed enrollment config", 1<<20, 0o600)
				committed = verifyErr == nil && bytes.Equal(installed, configBytes)
			}
			if !committed {
				return errors.Join(writeErr, restoreRetiredReceipt(paths, retiredReceipt))
			}
		}
		if retiredReceipt != "" {
			if err := os.Remove(retiredReceipt); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove retired empty receipt: %w", err)
			}
			if err := syncDirectory(filepath.Dir(retiredReceipt)); err != nil {
				return fmt.Errorf("sync retired empty receipt removal: %w", err)
			}
		}
		changed = true
		return nil
	})
	return result, changed, err
}

func retireEmptyReceipt(paths Paths) (string, error) {
	receipt, err := LoadReceipt(paths)
	if err != nil {
		return "", fmt.Errorf("validate empty receipt before replacement: %w", err)
	}
	if len(receipt.Projections) != 0 || len(receipt.Instructions) != 0 {
		return "", fmt.Errorf("cannot replace enrollment while managed skills or instructions remain; decommission them or migrate ownership first")
	}
	if err := validateTrustedStateFile(paths.Receipt, "receipt.json"); err != nil {
		return "", err
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	retired := paths.Receipt + ".retiring-" + id
	if err := os.Rename(paths.Receipt, retired); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(paths.Receipt)); err != nil {
		restoreErr := os.Rename(retired, paths.Receipt)
		return "", errors.Join(err, restoreErr)
	}
	return retired, nil
}

func restoreRetiredReceipt(paths Paths, retired string) error {
	if retired == "" {
		return nil
	}
	if err := os.Rename(retired, paths.Receipt); err != nil {
		return fmt.Errorf("restore empty receipt after enrollment failure: %w", err)
	}
	if err := syncDirectory(filepath.Dir(paths.Receipt)); err != nil {
		return fmt.Errorf("sync restored empty receipt: %w", err)
	}
	return nil
}

func validateDisplayName(name string) error {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return fmt.Errorf("display name must be 1-128 valid, non-whitespace-surrounded characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("display name must not contain control characters")
		}
	}
	return nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cc-" + hex.EncodeToString(b), nil
}
