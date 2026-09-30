package terran

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var beforeEnrollmentConfigWrite func() error

func LoadEnrollment(paths Paths) (Enrollment, error) {
	data, _, err := readTrustedFile(paths.ConfigFile, "enrollment config", 1<<20, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Enrollment{}, Coded(CodeNotEnrolled, nextEnroll, err)
		}
		return Enrollment{}, Coded(CodeUnsafeState, nextState, err)
	}
	enrollment, err := decodeEnrollment(data)
	if err != nil {
		return Enrollment{}, Coded(CodeUnsafeState, nextState, err)
	}
	if enrollment.SchemaVersion != SchemaVersion || enrollment.RepositoryID == "" || enrollment.RepositoryPath == "" || enrollment.CommandCenterID == "" || enrollment.DisplayName == "" {
		return Enrollment{}, Coded(CodeUnsafeState, nextState, fmt.Errorf("invalid enrollment config"))
	}
	if (enrollment.OverlayID == "") != (enrollment.OverlayPath == "") || (enrollment.OverlayID != "" && (!skillNamePattern.MatchString(enrollment.OverlayID) || enrollment.OverlayID == enrollment.RepositoryID || !filepath.IsAbs(enrollment.OverlayPath) || filepath.Clean(enrollment.OverlayPath) != enrollment.OverlayPath)) {
		return Enrollment{}, Coded(CodeUnsafeState, nextState, fmt.Errorf("invalid enrollment overlay"))
	}
	for i, id := range enrollment.Holds {
		if _, _, _, err := ParseItemID(id); err != nil || (i > 0 && enrollment.Holds[i-1] >= id) {
			return Enrollment{}, Coded(CodeUnsafeState, nextState, fmt.Errorf("invalid enrollment holds"))
		}
	}
	return enrollment, nil
}

// Hold pins one item on this machine so plan and apply never touch it.
func Hold(id string) (Enrollment, error) { return setHold(id, true) }

// Unhold releases a hold; releasing an item that is not held succeeds.
func Unhold(id string) (Enrollment, error) { return setHold(id, false) }

func setHold(id string, hold bool) (Enrollment, error) {
	if _, _, _, err := ParseItemID(id); err != nil {
		return Enrollment{}, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return Enrollment{}, err
	}
	var result Enrollment
	err = withLock(paths.Lock, func() error {
		enrollment, err := LoadEnrollment(paths)
		if err != nil {
			return err
		}
		result, err = holdLocked(paths, enrollment, id, hold)
		return err
	})
	return result, err
}

// holdLocked adds or removes one hold and rewrites the enrollment; the caller
// holds the Terran lock.
func holdLocked(paths Paths, enrollment Enrollment, id string, hold bool) (Enrollment, error) {
	result := enrollment
	index := sort.SearchStrings(enrollment.Holds, id)
	held := index < len(enrollment.Holds) && enrollment.Holds[index] == id
	if held == hold {
		return result, nil
	}
	if hold {
		plan, err := planEnrolled(paths, enrollment, "all")
		if err != nil {
			return Enrollment{}, err
		}
		known := false
		for _, action := range plan.Actions {
			known = known || action.ID == id
		}
		if !known {
			return Enrollment{}, Coded(CodeUnknownItem, "run terran plan --json to list item ids", fmt.Errorf("unknown item %q", id))
		}
		result.Holds = append(append(append([]string(nil), enrollment.Holds[:index]...), id), enrollment.Holds[index:]...)
	} else {
		result.Holds = append(append([]string(nil), enrollment.Holds[:index]...), enrollment.Holds[index+1:]...)
	}
	return writeEnrollment(paths, result)
}

// ForgetResult reports which records Forget dropped for one item.
type ForgetResult struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Receipt       bool   `json:"receipt"`
	Held          bool   `json:"held"`
	// Backup is the private backup the dropped receipt entry referenced; it is
	// kept, and while it exists it can block adopting or replacing the item.
	Backup string `json:"backup,omitempty"`
}

// Forget drops Terran's receipt entry and hold for one item without touching
// its destination or any backup; the next plan treats the item as unowned.
func Forget(id string) (ForgetResult, error) {
	if _, _, _, err := ParseItemID(id); err != nil {
		return ForgetResult{}, Coded(CodeUsage, "", err)
	}
	paths, err := ResolvePaths()
	if err != nil {
		return ForgetResult{}, err
	}
	result := ForgetResult{SchemaVersion: SchemaVersion, ID: id}
	err = withLock(paths.Lock, func() error {
		enrollment, err := LoadEnrollment(paths)
		if err != nil {
			return err
		}
		receipt, err := LoadReceipt(paths, enrollment)
		if errors.Is(err, os.ErrNotExist) {
			receipt = Receipt{}
		} else if err != nil {
			return fmt.Errorf("load receipt: %w", err)
		} else if receipt.RepositoryID != enrollment.RepositoryID || receipt.RepositoryPath != enrollment.RepositoryPath {
			return Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("receipt repository differs from enrollment"))
		}
		kept := receipt
		kept.Projections, kept.Managed, kept.JSONKeys = nil, nil, nil
		for _, entry := range receipt.Projections {
			if ItemID("skill", entry.Target, entry.Skill) == id {
				result.Receipt = true
			} else {
				kept.Projections = append(kept.Projections, entry)
			}
		}
		for _, entry := range receipt.Managed {
			if ItemID(entry.Kind, entry.Target, entry.Name) == id {
				result.Receipt, result.Backup = true, entry.Backup
			} else {
				kept.Managed = append(kept.Managed, entry)
			}
		}
		for _, entry := range receipt.JSONKeys {
			if ItemID("json-keys", entry.Target, entry.Key) == id {
				result.Receipt = true
			} else {
				kept.JSONKeys = append(kept.JSONKeys, entry)
			}
		}
		index := sort.SearchStrings(enrollment.Holds, id)
		result.Held = index < len(enrollment.Holds) && enrollment.Holds[index] == id
		if !result.Receipt && !result.Held {
			return Coded(CodeUnknownItem, "run terran plan --json to list item ids; forget only drops receipt-owned or held items", fmt.Errorf("unknown item %q", id))
		}
		if result.Receipt {
			data, err := marshalJSON(kept)
			if err != nil {
				return err
			}
			writeResult, writeErr := atomicPrivateJSONBytes(paths.Receipt, data, nil)
			if writeErr != nil {
				installed, _, verifyErr := readTrustedFile(paths.Receipt, "installed receipt", 4<<20, 0o600)
				if !writeResult.renamed || verifyErr != nil || !bytes.Equal(installed, data) {
					return writeErr
				}
			}
		}
		if result.Held {
			if _, err := holdLocked(paths, enrollment, id, false); err != nil {
				if result.Receipt {
					return Coded(CodePartialApply, "run terran unhold "+id, fmt.Errorf("receipt entry for %s was dropped but its hold was not released: %w", id, err))
				}
				return err
			}
		}
		return nil
	})
	return result, err
}

// writeEnrollment rewrites the enrollment config, typically with changed
// holds; the caller holds the Terran lock and has validated every hold id.
func writeEnrollment(paths Paths, result Enrollment) (Enrollment, error) {
	if len(result.Holds) == 0 {
		result.Holds = nil
	}
	data, err := marshalJSON(result)
	if err != nil {
		return Enrollment{}, err
	}
	writeResult, writeErr := atomicPrivateJSONBytes(paths.ConfigFile, data, nil)
	if writeErr != nil {
		committed := false
		if writeResult.renamed {
			installed, _, verifyErr := readTrustedFile(paths.ConfigFile, "installed enrollment config", 1<<20, 0o600)
			committed = verifyErr == nil && bytes.Equal(installed, data)
		}
		if !committed {
			return Enrollment{}, writeErr
		}
	}
	return result, nil
}

// EnrollmentMissing distinguishes an absent enrollment from an unsafe or
// malformed enrollment path without creating state.
func EnrollmentMissing(paths Paths) (bool, error) {
	if _, err := os.Lstat(paths.ConfigFile); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := os.Lstat(paths.ConfigDir); errors.Is(err, os.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	if err := validateTrustedDirectory(paths.ConfigDir, "enrollment directory"); err != nil {
		return false, err
	}
	return true, nil
}

// Enroll records the primary catalog and, when overlay is non-empty, a private
// overlay catalog. Re-enrolling the same primary may rename the Command Center
// or add an overlay; an omitted overlay keeps the enrolled one unless replace
// is set. Holds survive re-enrollment of the same primary.
func Enroll(repo, name, overlay string, replace bool) (Enrollment, bool, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return Enrollment{}, false, err
	}
	loaded, err := LoadManifest(repo)
	if err != nil {
		return Enrollment{}, false, err
	}
	var overlayID, overlayPath string
	if overlay != "" {
		overlayLoaded, err := LoadManifest(overlay)
		if err != nil {
			return Enrollment{}, false, err
		}
		if _, err := combineCatalogs(loaded, &overlayLoaded); err != nil {
			return Enrollment{}, false, err
		}
		overlayID, overlayPath = overlayLoaded.Manifest.ID, overlayLoaded.Repository
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
		sameRepository := loadErr == nil && existing.RepositoryPath == loaded.Repository && existing.RepositoryID == loaded.Manifest.ID
		if sameRepository {
			result = existing
			if explicitName {
				result.DisplayName = name
			}
			if overlay != "" || replace {
				result.OverlayID, result.OverlayPath = overlayID, overlayPath
			}
			if result.OverlayID != existing.OverlayID || result.OverlayPath != existing.OverlayPath {
				if err := refuseOwnedOverlayChange(paths, existing); err != nil {
					return err
				}
			}
			if result.DisplayName == existing.DisplayName && result.OverlayID == existing.OverlayID && result.OverlayPath == existing.OverlayPath {
				return nil
			}
		} else if loadErr == nil {
			if !replace {
				return fmt.Errorf("a different repository is enrolled; use --replace")
			}
			receipt, receiptErr := LoadReceipt(paths, existing)
			if receiptErr == nil && (len(receipt.Projections) != 0 || len(receipt.Managed) != 0) {
				return fmt.Errorf("cannot replace enrollment while managed skills, instructions, or configs remain; decommission them or migrate ownership first")
			}
			if receiptErr != nil && !errors.Is(receiptErr, os.ErrNotExist) {
				return fmt.Errorf("read receipt before replacement: %w", receiptErr)
			}
			emptyReceipt = receiptErr == nil
		} else if !errors.Is(loadErr, os.ErrNotExist) {
			return fmt.Errorf("read enrollment: %w", loadErr)
		}
		if !sameRepository {
			id, err := randomID()
			if err != nil {
				return err
			}
			if loadErr == nil {
				id = existing.CommandCenterID
			}
			result = Enrollment{
				SchemaVersion:   SchemaVersion,
				RepositoryID:    loaded.Manifest.ID,
				RepositoryPath:  loaded.Repository,
				CommandCenterID: id,
				DisplayName:     name,
				OverlayID:       overlayID,
				OverlayPath:     overlayPath,
			}
		}
		var retiredReceipt string
		if emptyReceipt {
			retired, err := retireEmptyReceipt(paths, existing)
			if err != nil {
				return err
			}
			retiredReceipt = retired
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

// refuseOwnedOverlayChange blocks changing or removing an enrolled overlay
// while the receipt still owns items from it; otherwise those items could no
// longer be verified or removed safely.
func refuseOwnedOverlayChange(paths Paths, existing Enrollment) error {
	if existing.OverlayID == "" {
		return nil
	}
	receipt, err := LoadReceipt(paths, existing)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("read receipt before overlay change: %w", err)
	}
	owned := false
	for _, projection := range receipt.Projections {
		owned = owned || projection.Catalog == existing.OverlayID
	}
	for _, managed := range receipt.Managed {
		owned = owned || managed.Catalog == existing.OverlayID
	}
	for _, entry := range receipt.JSONKeys {
		owned = owned || entry.Catalog == existing.OverlayID
	}
	if owned {
		return Coded(CodeRepositoryMismatch, "remove the overlay-owned items from the "+existing.OverlayID+" catalog and run terran apply before changing the overlay", fmt.Errorf("overlay %s still owns managed items", existing.OverlayID))
	}
	return nil
}

func retireEmptyReceipt(paths Paths, existing Enrollment) (string, error) {
	receipt, err := LoadReceipt(paths, existing)
	if err != nil {
		return "", fmt.Errorf("validate empty receipt before replacement: %w", err)
	}
	if len(receipt.Projections) != 0 || len(receipt.Managed) != 0 || len(receipt.JSONKeys) != 0 {
		return "", fmt.Errorf("cannot replace enrollment while managed skills, instructions, or configs remain; decommission them or migrate ownership first")
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
