package cmd

import (
	"fmt"
	"strings"

	"github.com/gtsteffaniak/filebrowser/backend/common/errors"
	"github.com/gtsteffaniak/filebrowser/backend/common/settings"
	"github.com/gtsteffaniak/filebrowser/backend/database/access"
	"github.com/gtsteffaniak/go-logger/logger"
)

// applyConfigAccessRules applies the declarative access rules from the config.
// The config authoritatively owns the rules at the paths it declares; rules
// created elsewhere (e.g. the admin UI) at other paths are left alone. A bad
// declaration fails startup: silently skipping an access rule would leave a
// path more open than the config states.
func applyConfigAccessRules() {
	if store.Access == nil || len(settings.Config.Access.Rules) == 0 {
		return
	}
	defaultSource := ""
	if len(settings.Config.Server.Sources) > 0 {
		defaultSource = settings.Config.Server.Sources[0].Path
	}
	for _, r := range settings.Config.Access.Rules {
		sourcePath := defaultSource
		if r.Source != "" {
			sourcePath = ""
			for _, src := range settings.Config.Server.Sources {
				if r.Source == src.Path || r.Source == src.Name {
					sourcePath = src.Path
					break
				}
			}
			if sourcePath == "" {
				logger.Fatalf("access rule for path %q references unknown source %q", r.Path, r.Source)
			}
		}
		if sourcePath == "" {
			logger.Fatalf("access rule for path %q: no source configured", r.Path)
		}
		indexPath := r.Path
		if !strings.HasPrefix(indexPath, "/") {
			indexPath = "/" + indexPath
		}
		if err := store.Access.SetRule(sourcePath, indexPath, r.DenyAll, r.AllowUsers, r.AllowGroups, r.DenyUsers, r.DenyGroups); err != nil {
			logger.Fatalf("failed to apply access rule for source %q path %q: %v", sourcePath, indexPath, err)
		}
		logger.Infof("Applied config access rule: source=%s path=%s denyAll=%v allow(users=%v groups=%v) deny(users=%v groups=%v)",
			sourcePath, indexPath, r.DenyAll, r.AllowUsers, r.AllowGroups, r.DenyUsers, r.DenyGroups)
	}
}

// validateAccessRules migrates old-style access rules (without trailing slashes) to new format
func validateAccessRules() {
	if store.Access == nil {
		return
	}
	// Get all sources
	for sourcePath := range settings.Config.Server.SourceMap {
		// Get all rules for this source
		rules, err := store.Access.GetAllRules(sourcePath)
		if err != nil {
			logger.Errorf("Failed to get access rules for source %s: %v", sourcePath, err)
			continue
		}

		// Check if there are any rules that need migration
		needsMigration := false
		for oldPath := range rules {
			if oldPath != "/" && !strings.HasSuffix(oldPath, "/") {
				needsMigration = true
				break
			}
		}

		if !needsMigration {
			continue
		}

		migratedCount := 0
		for oldPath, rule := range rules {
			// Check if this path needs migration (doesn't have trailing slash and isn't root)
			if oldPath != "/" && !strings.HasSuffix(oldPath, "/") {
				// Create the new path with trailing slash
				newPath := oldPath + "/"

				// Migrate the rule to the new path
				if err := migrateAccessRule(sourcePath, oldPath, newPath, rule); err != nil {
					logger.Errorf("Failed to migrate rule from %s to %s: %v", oldPath, newPath, err)
					continue
				}

				// Remove the old rule
				if err := removeOldAccessRule(sourcePath, oldPath); err != nil {
					logger.Errorf("Failed to remove old rule %s: %v", oldPath, err)
					continue
				}

				migratedCount++
			}
		}

		// After migration, clear cache
		if migratedCount > 0 {
			logger.Infof("Migrated %d access rules for source %s", migratedCount, sourcePath)
		}
	}
}

// migrateAccessRule creates a new access rule with the new path format
func migrateAccessRule(sourcePath, oldPath, newPath string, rule access.FrontendAccessRule) error {
	// Add deny users
	for _, user := range rule.Deny.Users {
		if err := store.Access.DenyUser(sourcePath, newPath, user); err != nil && err != errors.ErrExist {
			return fmt.Errorf("failed to add deny user %s: %w", user, err)
		}
	}

	// Add deny groups
	for _, group := range rule.Deny.Groups {
		if err := store.Access.DenyGroup(sourcePath, newPath, group); err != nil && err != errors.ErrExist {
			return fmt.Errorf("failed to add deny group %s: %w", group, err)
		}
	}

	// Add allow users
	for _, user := range rule.Allow.Users {
		if err := store.Access.AllowUser(sourcePath, newPath, user); err != nil && err != errors.ErrExist {
			return fmt.Errorf("failed to add allow user %s: %w", user, err)
		}
	}

	// Add allow groups
	for _, group := range rule.Allow.Groups {
		if err := store.Access.AllowGroup(sourcePath, newPath, group); err != nil && err != errors.ErrExist {
			return fmt.Errorf("failed to add allow group %s: %w", group, err)
		}
	}

	// Add deny all rule if needed
	if rule.DenyAll {
		if err := store.Access.DenyAll(sourcePath, newPath); err != nil && err != errors.ErrExist {
			return fmt.Errorf("failed to add deny all rule: %w", err)
		}
	}

	return nil
}

// removeOldAccessRule removes the old access rule by directly accessing the internal storage
func removeOldAccessRule(sourcePath, oldPath string) error {
	// Access the internal storage directly to remove the old rule
	// We need to use the unnormalized path since that's how it was stored originally
	store.Access.RemoveRuleByPath(sourcePath, oldPath)
	return nil
}
